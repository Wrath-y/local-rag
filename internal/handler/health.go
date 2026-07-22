package handler

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Wrath-y/local-rag/internal/observe"
)

// Health returns the service health status.
// It probes the embedder with a ping; degraded if unreachable.
func (h *Handler) Health(c *gin.Context) {
	// Probe embedder with a tiny request.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := h.deps.Embedder.Embed(ctx, []string{"ping"})
	if err != nil {
		// Degraded: embedder unreachable.
		c.JSON(http.StatusOK, gin.H{"status": "degraded", "reason": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// Metrics serves Prometheus metrics in text exposition format.
func (h *Handler) Metrics(c *gin.Context) {
	data := observe.Render()
	c.Data(http.StatusOK, "text/plain; version=0.0.4; charset=utf-8", data)
}

// IntegrityCheck runs SQLite integrity_check and returns the result.
func (h *Handler) IntegrityCheck(c *gin.Context) {
	result, err := h.management.IntegrityCheck()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	status := "ok"
	code := http.StatusOK
	if result != "ok" {
		status = "error"
		code = http.StatusConflict
	}
	c.JSON(code, gin.H{"status": status, "detail": result})
}
