package handler

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/gin-gonic/gin"

	"github.com/Wrath-y/local-rag/internal/config"
	"github.com/Wrath-y/local-rag/internal/management"
	"github.com/Wrath-y/local-rag/internal/store"
)

// ListSources returns all indexed sources with chunk counts.
func (h *Handler) ListSources(c *gin.Context) {
	sources, err := h.management.ListSources()
	if err != nil {
		if err == ErrRebuildInProgress {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"sources": sources})
}

// DeleteSource removes all chunks for a given source (query param: source=xxx).
func (h *Handler) DeleteSource(c *gin.Context) {
	source := c.Query("source")
	if source == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "source query param is required"})
		return
	}
	n, err := h.management.DeleteSource(source)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": n})
}

// Reset removes all chunks from the store.
func (h *Handler) Reset(c *gin.Context) {
	if h.deps.Stores.Rebuilding() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": ErrRebuildInProgress.Error()})
		return
	}
	if c.Query("confirm") != "true" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "confirm must be true"})
		return
	}
	submission, err := h.management.Reset(management.ResetRequest{Confirm: true})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	task, found, err := h.management.Tasks().Wait(context.Background(), submission.TaskID)
	if err != nil || !found || task.State != management.TaskSucceeded {
		errorText := "reset failed"
		if err != nil {
			errorText = err.Error()
		} else if found && task.Error != "" {
			errorText = task.Error
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": errorText})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// Stats returns basic store statistics.
func (h *Handler) Stats(c *gin.Context) {
	var stats map[string]interface{}
	err := h.deps.Stores.WithStore(func(st *store.Store) error {
		var statsErr error
		stats, statsErr = st.Stats()
		return statsErr
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	hookSnapshot := h.hookObservations.Snapshot()
	stats["hook_observability"] = gin.H{
		"total_enabled_attempts": hookSnapshot.TotalEnabledAttempts,
		"outcomes":               hookSnapshot.Outcomes,
		"latest":                 hookSnapshot.Latest,
		"verbose_enabled":        h.verboseEnabled,
		"reset_on_restart":       true,
	}
	c.JSON(http.StatusOK, stats)
}

// Export downloads the SQLite database file as a versioned zip archive.
func (h *Handler) Export(c *gin.Context) {
	temporary, err := os.CreateTemp(filepath.Dir(h.deps.Config.Storage.DBPath), ".rag-http-export-*.zip")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "export failed: " + err.Error()})
		return
	}
	archivePath := temporary.Name()
	if err := temporary.Close(); err != nil {
		_ = os.Remove(archivePath)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "export failed: " + err.Error()})
		return
	}
	_ = os.Remove(archivePath)
	defer os.Remove(archivePath)
	if _, err := h.management.Export(management.ArchiveRequest{Path: archivePath}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "export failed: " + err.Error()})
		return
	}
	zipData, err := os.ReadFile(archivePath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "export failed: " + err.Error()})
		return
	}

	c.Header("Content-Disposition", "attachment; filename=rag-export.zip")
	c.Data(http.StatusOK, "application/zip", zipData)
}

func (h *Handler) createDatabaseSnapshot() (string, func(), error) {
	temporaryDir, err := os.MkdirTemp(filepath.Dir(h.deps.Config.Storage.DBPath), ".rag-export-")
	if err != nil {
		return "", nil, fmt.Errorf("create snapshot directory: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(temporaryDir) }
	snapshotPath := filepath.Join(temporaryDir, BackupDatabaseEntryName)
	if err := h.deps.Stores.WithStore(func(st *store.Store) error { return st.Snapshot(snapshotPath) }); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("create database snapshot: %w", err)
	}
	return snapshotPath, cleanup, nil
}

func (h *Handler) backupPackageMetadata() BackupPackageMetadata {
	metadata := BackupPackageMetadata{}
	if h.deps.Config != nil {
		metadata.Embedding = embeddingSummary(h.deps.Config.Embedding)
	}
	_ = h.deps.Stores.WithStore(func(st *store.Store) error {
		stats, err := st.Stats()
		if err != nil {
			return err
		}
		if totalChunks, ok := stats["total_chunks"].(int); ok {
			metadata.ChunkCount = totalChunks
		}
		return nil
	})
	return metadata
}

func embeddingSummary(embedding config.EmbeddingConfig) EmbeddingSummary {
	return EmbeddingSummary{
		Provider:   embedding.Provider,
		Model:      embedding.Model,
		Dimensions: embedding.Dims,
	}
}

// Import replaces the database from a confirmed uploaded backup package.
func (h *Handler) Import(c *gin.Context) {
	if h.deps.Stores.Rebuilding() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": ErrRebuildInProgress.Error()})
		return
	}
	if c.PostForm("confirm") != "true" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "confirm must be true"})
		return
	}
	file, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "file is required: " + err.Error()})
		return
	}
	staged, err := os.CreateTemp(filepath.Dir(h.deps.Config.Storage.DBPath), ".rag-import-upload-*.zip")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "stage import: " + err.Error()})
		return
	}
	stagedPath := staged.Name()
	defer os.Remove(stagedPath)
	if err := staged.Close(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "stage import: " + err.Error()})
		return
	}
	if err := c.SaveUploadedFile(file, stagedPath); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "save import: " + err.Error()})
		return
	}
	submission, err := h.management.Import(management.ImportRequest{Path: stagedPath, Confirm: true})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "failed", "stage": RestoreStageValidate, "error": err.Error()})
		return
	}
	task, found, waitErr := h.management.Tasks().Wait(context.Background(), submission.TaskID)
	if waitErr != nil || !found || task.State != management.TaskSucceeded {
		errorText := "import failed"
		if waitErr != nil {
			errorText = waitErr.Error()
		} else if found && task.Error != "" {
			errorText = task.Error
		}
		c.JSON(http.StatusBadRequest, gin.H{"status": "failed", "stage": RestoreStageValidate, "error": errorText})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "stage": RestoreStageComplete})
}
