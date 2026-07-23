// Package sourcesync coordinates chunking and embeddings with the durable
// SQLite sync store. HTTP and MCP use this same service.
package sourcesync

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/Wrath-y/local-rag/internal/chunk"
	"github.com/Wrath-y/local-rag/internal/config"
	"github.com/Wrath-y/local-rag/internal/observe"
	"github.com/Wrath-y/local-rag/internal/provider"
	"github.com/Wrath-y/local-rag/internal/store"
)

type Service struct {
	Store    *store.Store
	Chunker  chunk.Chunker
	Embedder provider.EmbedProvider
	Config   config.SyncConfig
	identity store.SyncIdentity
	wake     chan struct{}
	stop     chan struct{}
	once     sync.Once
}

func New(st *store.Store, ch chunk.Chunker, embedder provider.EmbedProvider, cfg *config.Config) *Service {
	sc := config.SyncConfig{Enabled: true, Workers: 1, MaxSnapshotBytes: 16 << 20, MaxAttempts: 3}
	identity := store.SyncIdentity{Canonicalization: "utf8-trim-crlf-v1", Chunker: "fixed-v1", EmbeddingModel: "default"}
	if cfg != nil {
		sc = cfg.Sync
		identity.Chunker = cfg.Chunk.Strategy + "-v1"
		identity.EmbeddingModel = cfg.Embedding.Provider + ":" + cfg.Embedding.Model
	}
	return &Service{Store: st, Chunker: ch, Embedder: embedder, Config: sc, identity: identity, wake: make(chan struct{}, 1), stop: make(chan struct{})}
}
func (s *Service) Start() error {
	if s == nil || !s.Config.Enabled {
		return nil
	}
	if err := s.Store.RecoverSyncTasks(); err != nil {
		return err
	}
	if err := s.Store.CleanupSyncStaging(s.Config.StagingRetentionHours); err != nil {
		return err
	}
	s.once.Do(func() {
		for i := 0; i < s.Config.Workers; i++ {
			go s.worker()
		}
	})
	s.Wake()
	return nil
}
func (s *Service) Close() {
	if s != nil {
		select {
		case <-s.stop:
		default:
			close(s.stop)
		}
	}
}
func (s *Service) Wake() {
	if s == nil {
		return
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
}
func (s *Service) Submit(snapshot store.SyncSnapshot, key string) (store.SyncTask, bool, error) {
	if !s.Config.Enabled {
		return store.SyncTask{}, false, storeErr("disabled", "incremental sync is disabled")
	}
	if snapshot.Identity.Canonicalization == "" {
		snapshot.Identity = s.identity
	}
	task, replayed, err := s.Store.SubmitSync(snapshot, key, s.Config.MaxSnapshotBytes)
	if err == nil {
		if replayed {
			observe.SyncEventsTotal.WithLabelValues("idempotent_replay").Inc()
		} else {
			observe.SyncEventsTotal.WithLabelValues("submitted").Inc()
		}
		s.Wake()
	} else if store.SyncErrorCode(err) == "active_task_conflict" || store.SyncErrorCode(err) == "idempotency_conflict" {
		observe.SyncEventsTotal.WithLabelValues("conflict").Inc()
	}
	return task, replayed, err
}
func (s *Service) Retry(source, id string) (store.SyncTask, error) {
	if !s.Config.Enabled {
		return store.SyncTask{}, storeErr("disabled", "incremental sync is disabled")
	}
	task, err := s.Store.RetrySyncTask(source, id, s.Config.MaxAttempts)
	if err == nil {
		observe.SyncEventsTotal.WithLabelValues("retry").Inc()
		s.Wake()
	} else if store.SyncErrorCode(err) == "retry_limit" {
		observe.SyncEventsTotal.WithLabelValues("retry_limit").Inc()
	}
	return task, err
}
func storeErr(code, msg string) error { return &store.SyncError{Code: code, Message: msg} }
func (s *Service) worker() {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-s.wake:
			for s.ProcessNext(context.Background()) {
			}
		case <-ticker.C:
			for s.ProcessNext(context.Background()) {
			}
		}
	}
}

// ProcessNext is exported for deterministic adapter tests.
func (s *Service) ProcessNext(ctx context.Context) bool {
	task, snapshot, ok, err := s.Store.ClaimNextSyncTask()
	if err != nil {
		slog.Error("claim sync task", "err", err)
		return false
	}
	if !ok {
		return false
	}
	started := time.Now()
	if task.StartedAt != nil {
		observe.SyncDuration.WithLabelValues("queue").Observe(task.StartedAt.Sub(task.CreatedAt).Seconds())
	}
	if err := s.process(ctx, task, snapshot); err != nil {
		slog.Warn("sync task failed", "task_id", task.ID, "source", task.Source, "error", err)
		_ = s.Store.FailSyncTask(task, err)
		observe.SyncTasksTotal.WithLabelValues("failed").Inc()
	} else {
		observe.SyncTasksTotal.WithLabelValues("succeeded").Inc()
		slog.Info("sync task succeeded", "task_id", task.ID, "source", task.Source, "duration", time.Since(started))
	}
	observe.SyncDuration.WithLabelValues("processing").Observe(time.Since(started).Seconds())
	return true
}
func (s *Service) process(ctx context.Context, task store.SyncTask, snapshot store.SyncSnapshot) error {
	for i := range snapshot.Documents {
		d := &snapshot.Documents[i]
		if len(d.Chunks) > 0 {
			continue
		}
		if s.Chunker == nil {
			return fmt.Errorf("sync chunker unavailable")
		}
		chunks, err := s.Chunker.Chunk(d.Content, snapshot.Source)
		if err != nil {
			return fmt.Errorf("chunk document %s: %w", d.ID, err)
		}
		if len(chunks) == 0 {
			return storeErr("validation", "document produced no chunks")
		}
		d.Chunks = make([]store.SyncChunk, len(chunks))
		for n, c := range chunks {
			d.Chunks[n] = store.SyncChunk{Key: fmt.Sprintf("%08d", n), Content: c.Text, ParentText: c.ParentText, ParentID: c.ParentID, Title: first(c.Title, d.Title), URI: first(c.URI, d.URI), Location: c.Location}
		}
	}
	if snapshot.Identity.Canonicalization == "" {
		snapshot.Identity = s.identity
	}
	diff, err := s.Store.PrepareSyncDiff(snapshot)
	if err != nil {
		return err
	}
	observe.SyncDiffChunks.WithLabelValues("unchanged").Add(float64(diff.Chunks.Unchanged))
	observe.SyncDiffChunks.WithLabelValues("added").Add(float64(diff.Chunks.Added))
	observe.SyncDiffChunks.WithLabelValues("changed").Add(float64(diff.Chunks.Changed))
	observe.SyncDiffChunks.WithLabelValues("deleted").Add(float64(diff.Chunks.Deleted))
	for _, d := range snapshot.Documents {
		var todo []store.SyncChunk
		for _, c := range d.Chunks {
			if diff.EmbedKeys[store.ChunkKey(snapshot.Source, d.ID, c.Key)] {
				todo = append(todo, c)
			}
		}
		if len(todo) == 0 {
			continue
		}
		if s.Embedder == nil {
			return fmt.Errorf("sync embedder unavailable")
		}
		texts := make([]string, len(todo))
		for i, c := range todo {
			texts[i] = c.Content
		}
		vectors, err := s.Embedder.Embed(ctx, texts)
		if err != nil {
			return fmt.Errorf("embed document %s: %w", d.ID, err)
		}
		if len(vectors) != len(todo) {
			return fmt.Errorf("embedding count mismatch")
		}
		for i, c := range todo {
			if err := s.Store.StageSyncEmbedding(task.ID, snapshot.Source, d.ID, c, vectors[i]); err != nil {
				return err
			}
		}
	}
	promotionStarted := time.Now()
	report, err := s.Store.PromoteSync(task, snapshot, diff)
	observe.SyncDuration.WithLabelValues("promotion").Observe(time.Since(promotionStarted).Seconds())
	if err == nil {
		observe.SyncChunksTotal.WithLabelValues("reused").Add(float64(report.ReusedChunks))
		observe.SyncChunksTotal.WithLabelValues("embedded").Add(float64(report.EmbeddedChunks))
		observe.SyncChunksTotal.WithLabelValues("deleted").Add(float64(report.DeletedChunks))
	}
	return err
}
func first(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
