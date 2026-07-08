package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// ListSources returns all indexed sources with chunk counts.
func (h *Handler) ListSources(c *gin.Context) {
	sources, err := h.deps.Store.ListSources()
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
	n, err := h.deps.Store.DeleteSource(source)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": n})
}

// Reset removes all chunks from the store.
func (h *Handler) Reset(c *gin.Context) {
	if err := h.deps.Store.Reset(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// Stats returns basic store statistics.
func (h *Handler) Stats(c *gin.Context) {
	stats, err := h.deps.Store.Stats()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, stats)
}

// Export downloads the SQLite database file as a zip archive.
func (h *Handler) Export(c *gin.Context) {
	dbPath := h.deps.Config.Storage.DBPath

	zipData, err := createDBZip(dbPath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "export failed: " + err.Error()})
		return
	}

	c.Header("Content-Disposition", "attachment; filename=rag-export.zip")
	c.Data(http.StatusOK, "application/zip", zipData)
}

// Import replaces the database from an uploaded zip (not yet implemented).
func (h *Handler) Import(c *gin.Context) {
	c.JSON(http.StatusNotImplemented, gin.H{"error": "import not implemented"})
}
