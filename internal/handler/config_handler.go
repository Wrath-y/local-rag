package handler

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/Wrath-y/local-rag/internal/management"
)

// validChunkStrategies is the set of allowed chunking strategy names.
var validChunkStrategies = map[string]bool{
	"fixed":        true,
	"structure":    true,
	"semantic":     true,
	"agentic":      true,
	"hierarchical": true,
}

// RerankToggle toggles reranking on/off and returns the new state.
func (h *Handler) RerankToggle(c *gin.Context) {
	value := !h.management.RetrievalConfig().RerankEnabled
	config, err := h.management.SetRetrievalConfig(management.RetrievalPatch{RerankEnabled: &value})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	h.rerankEnabled = config.RerankEnabled
	c.JSON(http.StatusOK, gin.H{"rerank_enabled": h.rerankEnabled})
}

// VerboseToggle toggles verbose logging, or sets it explicitly with ?enabled=true|false.
func (h *Handler) VerboseToggle(c *gin.Context) {
	if raw, present := c.GetQuery("enabled"); present {
		enabled, err := strconv.ParseBool(raw)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "enabled must be true or false"})
			return
		}
		config, err := h.management.SetRetrievalConfig(management.RetrievalPatch{VerboseEnabled: &enabled})
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		h.verboseEnabled = config.VerboseEnabled
		c.JSON(http.StatusOK, gin.H{"verbose_enabled": h.verboseEnabled})
		return
	}
	value := !h.management.RetrievalConfig().VerboseEnabled
	config, err := h.management.SetRetrievalConfig(management.RetrievalPatch{VerboseEnabled: &value})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	h.verboseEnabled = config.VerboseEnabled
	c.JSON(http.StatusOK, gin.H{"verbose_enabled": h.verboseEnabled})
}

// DynamicTopKToggle toggles dynamic top_k computation on/off.
func (h *Handler) DynamicTopKToggle(c *gin.Context) {
	value := !h.management.RetrievalConfig().DynamicTopKEnabled
	config, err := h.management.SetRetrievalConfig(management.RetrievalPatch{DynamicTopKEnabled: &value})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	h.dynamicTopKEnabled = config.DynamicTopKEnabled
	c.JSON(http.StatusOK, gin.H{"dynamic_top_k_enabled": h.dynamicTopKEnabled})
}

// QueryRewriteToggle toggles query rewriting on/off.
func (h *Handler) QueryRewriteToggle(c *gin.Context) {
	value := !h.management.RetrievalConfig().QueryRewriteEnabled
	config, err := h.management.SetRetrievalConfig(management.RetrievalPatch{QueryRewriteEnabled: &value})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	h.queryRewriteEnabled = config.QueryRewriteEnabled
	c.JSON(http.StatusOK, gin.H{"query_rewrite_enabled": h.queryRewriteEnabled})
}

// GetChunkStrategy returns the current chunk strategy.
func (h *Handler) GetChunkStrategy(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"strategy": h.management.RetrievalConfig().ChunkStrategy})
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
	config, err := h.management.SetRetrievalConfig(management.RetrievalPatch{ChunkStrategy: &req.Strategy})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	h.chunkStrategy = config.ChunkStrategy
	c.JSON(http.StatusOK, gin.H{"strategy": h.chunkStrategy})
}
