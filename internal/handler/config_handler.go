package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// validChunkStrategies is the set of allowed chunking strategy names.
var validChunkStrategies = map[string]bool{
	"fixed":       true,
	"structure":   true,
	"semantic":    true,
	"agentic":     true,
	"hierarchical": true,
}

// RerankToggle toggles reranking on/off and returns the new state.
func (h *Handler) RerankToggle(c *gin.Context) {
	h.rerankEnabled = !h.rerankEnabled
	c.JSON(http.StatusOK, gin.H{"rerank_enabled": h.rerankEnabled})
}

// VerboseToggle toggles verbose logging on/off.
func (h *Handler) VerboseToggle(c *gin.Context) {
	h.verboseEnabled = !h.verboseEnabled
	c.JSON(http.StatusOK, gin.H{"verbose_enabled": h.verboseEnabled})
}

// DynamicTopKToggle toggles dynamic top_k computation on/off.
func (h *Handler) DynamicTopKToggle(c *gin.Context) {
	h.dynamicTopKEnabled = !h.dynamicTopKEnabled
	c.JSON(http.StatusOK, gin.H{"dynamic_top_k_enabled": h.dynamicTopKEnabled})
}

// QueryRewriteToggle toggles query rewriting on/off.
func (h *Handler) QueryRewriteToggle(c *gin.Context) {
	h.queryRewriteEnabled = !h.queryRewriteEnabled
	c.JSON(http.StatusOK, gin.H{"query_rewrite_enabled": h.queryRewriteEnabled})
}

// GetChunkStrategy returns the current chunk strategy.
func (h *Handler) GetChunkStrategy(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"strategy": h.chunkStrategy})
}

type setChunkStrategyRequest struct {
	Strategy string `json:"strategy" binding:"required"`
}

// SetChunkStrategy updates the active chunk strategy after validation.
func (h *Handler) SetChunkStrategy(c *gin.Context) {
	var req setChunkStrategyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if !validChunkStrategies[req.Strategy] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unknown strategy: " + req.Strategy})
		return
	}
	h.chunkStrategy = req.Strategy
	c.JSON(http.StatusOK, gin.H{"strategy": h.chunkStrategy})
}
