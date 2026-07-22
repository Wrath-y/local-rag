package handler

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/gin-gonic/gin"

	"github.com/Wrath-y/local-rag/internal/config"
	"github.com/Wrath-y/local-rag/internal/store"
)

// ListSources returns all indexed sources with chunk counts.
func (h *Handler) ListSources(c *gin.Context) {
	var sources []store.SourceInfo
	err := h.deps.Stores.WithStore(func(st *store.Store) error {
		var listErr error
		sources, listErr = st.ListSources()
		return listErr
	})
	if err != nil {
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
	var n int
	err := h.deps.Stores.WithStore(func(st *store.Store) error {
		var deleteErr error
		n, deleteErr = st.DeleteSource(source)
		return deleteErr
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": n})
}

// Reset removes all chunks from the store.
func (h *Handler) Reset(c *gin.Context) {
	if err := h.deps.Stores.WithStore(func(st *store.Store) error { return st.Reset() }); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
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
	c.JSON(http.StatusOK, stats)
}

// Export downloads the SQLite database file as a versioned zip archive.
func (h *Handler) Export(c *gin.Context) {
	snapshotPath, cleanup, err := h.createDatabaseSnapshot()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "export failed: " + err.Error()})
		return
	}
	defer cleanup()

	zipData, err := createDBZip(snapshotPath, h.backupPackageMetadata())
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
	result, err := h.deps.Restore.Restore(stagedPath)
	if err != nil {
		status := http.StatusInternalServerError
		if result.Stage == RestoreStageValidate {
			status = http.StatusBadRequest
		}
		c.JSON(status, gin.H{"status": "failed", "stage": result.Stage, "rolled_back": result.RolledBack, "error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "stage": result.Stage, "snapshot_path": result.SnapshotPath})
}
