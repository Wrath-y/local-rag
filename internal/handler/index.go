package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// IndexRebuild triggers an asynchronous index rebuild (stub).
func (h *Handler) IndexRebuild(c *gin.Context) {
	// In a full implementation this would kick off a goroutine that
	// re-embeds all chunks and rebuilds the vec_chunks table.
	c.JSON(http.StatusOK, gin.H{"status": "started"})
}

// IndexStatus returns the current index state.
func (h *Handler) IndexStatus(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"state": "normal"})
}
