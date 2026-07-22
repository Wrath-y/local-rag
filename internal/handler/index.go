package handler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Wrath-y/local-rag/internal/observe"
	"github.com/Wrath-y/local-rag/internal/provider"
	"github.com/Wrath-y/local-rag/internal/store"
)

const rebuildBatchSize = 32

type IndexState string

const (
	IndexStateNormal     IndexState = "normal"
	IndexStateRebuilding IndexState = "rebuilding"
	IndexStateFailed     IndexState = "failed"
	IndexStateReadOnly   IndexState = "read-only"
)

// IndexStatusResponse is the stable API contract for rebuild polling.
type IndexStatusResponse struct {
	State         IndexState `json:"state"`
	TaskID        string     `json:"task_id,omitempty"`
	StartedAt     *time.Time `json:"started_at,omitempty"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
	Processed     int        `json:"processed"`
	Total         int        `json:"total"`
	Progress      float64    `json:"progress"`
	ErrorCategory string     `json:"error_category,omitempty"`
	Error         string     `json:"error,omitempty"`
}

type indexRebuildCoordinator struct {
	mu       sync.RWMutex
	status   IndexStatusResponse
	stores   *StoreLifecycle
	embedder provider.EmbedProvider
	dims     int
}

func newIndexRebuildCoordinator(stores *StoreLifecycle, embedder provider.EmbedProvider, dims int) *indexRebuildCoordinator {
	if dims <= 0 && embedder != nil {
		dims = embedder.Dims()
	}
	return &indexRebuildCoordinator{
		stores:   stores,
		embedder: embedder,
		dims:     dims,
		status:   IndexStatusResponse{State: IndexStateNormal},
	}
}

func (r *indexRebuildCoordinator) Status() IndexStatusResponse {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.status
}

// Start admits one asynchronous rebuild. A concurrent request receives the
// active task instead of creating another job.
func (r *indexRebuildCoordinator) Start() (IndexStatusResponse, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.status.State == IndexStateRebuilding {
		return r.status, false, nil
	}
	if r.embedder == nil || r.dims <= 0 {
		return r.status, false, fmt.Errorf("index rebuild is unavailable: embedding provider is not configured")
	}
	if err := r.stores.BeginRebuild(); err != nil {
		if errors.Is(err, ErrRebuildInProgress) {
			return r.status, false, nil
		}
		return r.status, false, err
	}
	now := time.Now().UTC()
	r.status = IndexStatusResponse{
		State:     IndexStateRebuilding,
		TaskID:    uuid.NewString(),
		StartedAt: &now,
	}
	observe.IndexRebuildActive.Set(1)
	observe.IndexRebuildProgress.Set(0)
	observe.IndexRebuildTotal.WithLabelValues("submitted").Inc()
	go r.run(r.status.TaskID, now)
	return r.status, true, nil
}

func (r *indexRebuildCoordinator) run(taskID string, started time.Time) {
	defer r.stores.EndRebuild()
	defer observe.IndexRebuildActive.Set(0)
	defer observe.IndexRebuildDuration.Observe(time.Since(started).Seconds())
	suffix := strings.ReplaceAll(taskID, "-", "")
	shadowName := "vec_chunks_rebuild_" + suffix
	retiredName := "vec_chunks_retired_" + suffix
	failedName := "vec_chunks_failed_" + suffix
	slog.Info("index rebuild started", "task_id", taskID)

	var snapshot []store.ChunkSnapshot
	err := r.stores.WithStore(func(st *store.Store) error {
		var snapshotErr error
		snapshot, snapshotErr = st.SnapshotChunks()
		return snapshotErr
	})
	if err != nil {
		r.fail(taskID, "snapshot")
		return
	}
	r.setTotal(taskID, len(snapshot))

	err = r.stores.WithStore(func(st *store.Store) error { return st.CreateShadowIndex(shadowName) })
	if err != nil {
		r.fail(taskID, "shadow_storage")
		return
	}
	shadowCreated := true
	defer func() {
		if shadowCreated {
			_ = r.stores.WithStore(func(st *store.Store) error { return st.DropIndex(shadowName) })
		}
	}()

	for start := 0; start < len(snapshot); start += rebuildBatchSize {
		end := start + rebuildBatchSize
		if end > len(snapshot) {
			end = len(snapshot)
		}
		batch := snapshot[start:end]
		texts := make([]string, len(batch))
		for i, chunk := range batch {
			texts[i] = chunk.Text
		}
		vectors, embedErr := r.embedder.Embed(context.Background(), texts)
		if embedErr != nil || len(vectors) != len(batch) {
			r.fail(taskID, "embedding")
			return
		}
		insertErr := r.stores.WithStore(func(st *store.Store) error { return st.InsertShadowVectors(shadowName, batch, vectors) })
		if insertErr != nil {
			r.fail(taskID, "embedding")
			return
		}
		r.setProcessed(taskID, end)
	}

	err = r.stores.WithStore(func(st *store.Store) error { return st.ValidateShadowIndex(shadowName, snapshot) })
	if err != nil {
		r.fail(taskID, "validation")
		return
	}
	err = r.stores.WithExclusiveStore(func(st *store.Store) error {
		if err := st.ActivateShadowIndex(shadowName, retiredName); err != nil {
			return err
		}
		shadowCreated = false
		integrity, err := st.IntegrityCheck()
		if err != nil || integrity != "ok" {
			if err == nil {
				err = fmt.Errorf("integrity check returned %q", integrity)
			}
			if rollbackErr := st.RollbackActiveIndex(retiredName, failedName); rollbackErr != nil {
				return fmt.Errorf("integrity failure: %w; rollback failed: %v", err, rollbackErr)
			}
			return err
		}
		return nil
	})
	if err != nil {
		r.fail(taskID, "cutover")
		return
	}
	r.complete(taskID)
}

func (r *indexRebuildCoordinator) setTotal(taskID string, total int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.status.TaskID == taskID && r.status.State == IndexStateRebuilding {
		r.status.Total = total
		if total == 0 {
			r.status.Progress = 1
			observe.IndexRebuildProgress.Set(1)
		}
	}
}

func (r *indexRebuildCoordinator) setProcessed(taskID string, processed int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.status.TaskID != taskID || r.status.State != IndexStateRebuilding {
		return
	}
	if processed > r.status.Processed {
		r.status.Processed = processed
	}
	if r.status.Total > 0 {
		r.status.Progress = float64(r.status.Processed) / float64(r.status.Total)
		if r.status.Progress > 1 {
			r.status.Progress = 1
		}
		observe.IndexRebuildProgress.Set(r.status.Progress)
	}
}

func (r *indexRebuildCoordinator) complete(taskID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.status.TaskID != taskID {
		return
	}
	now := time.Now().UTC()
	r.status.State = IndexStateNormal
	r.status.Processed = r.status.Total
	r.status.Progress = 1
	r.status.CompletedAt = &now
	r.status.ErrorCategory = ""
	r.status.Error = ""
	observe.IndexRebuildProgress.Set(1)
	observe.IndexRebuildTotal.WithLabelValues("success").Inc()
	slog.Info("index rebuild completed", "task_id", taskID, "processed", r.status.Processed)
}

func (r *indexRebuildCoordinator) fail(taskID, category string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.status.TaskID != taskID {
		return
	}
	now := time.Now().UTC()
	r.status.State = IndexStateFailed
	r.status.CompletedAt = &now
	r.status.ErrorCategory = category
	r.status.Error = "rebuild failed during " + category
	observe.IndexRebuildTotal.WithLabelValues("failure").Inc()
	slog.Warn("index rebuild failed", "task_id", taskID, "category", category)
}

// IndexRebuild starts a coordinator-backed asynchronous rebuild.
func (h *Handler) IndexRebuild(c *gin.Context) {
	status, started, err := h.indexRebuild.Start()
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
		return
	}
	if !started {
		c.JSON(http.StatusConflict, gin.H{"error": "index rebuild already in progress", "task": status})
		return
	}
	c.JSON(http.StatusAccepted, status)
}

// IndexStatus returns the latest durable-in-process rebuild result.
func (h *Handler) IndexStatus(c *gin.Context) {
	c.JSON(http.StatusOK, h.indexRebuild.Status())
}
