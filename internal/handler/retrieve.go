package handler

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Wrath-y/local-rag/internal/observe"
	"github.com/Wrath-y/local-rag/internal/retrieval"
)

type retrieveRequest struct {
	Text              string `json:"text" binding:"required"`
	ContextTokensUsed int    `json:"context_tokens_used"`
}

// Retrieve performs hybrid vector+BM25 search and returns formatted chunks.
func (h *Handler) Retrieve(c *gin.Context) {
	var req retrieveRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Text == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "text is required"})
		return
	}

	chunks, err := h.doRetrieve(req.Text, req.ContextTokensUsed)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	observe.RetrieveTotal.WithLabelValues(boolLabel(len(chunks) > 0)).Inc()
	c.JSON(http.StatusOK, gin.H{"chunks": chunks})
}

// doRetrieve encapsulates the shared retrieve logic used by Retrieve and Hook.
func (h *Handler) doRetrieve(text string, contextTokensUsed int) ([]string, error) {
	start := time.Now()
	defer func() {
		observe.RetrieveLatency.Observe(time.Since(start).Seconds())
	}()

	cfg := h.deps.Config

	// Dynamic top_k: reduce k when context window is nearly full.
	topK := cfg.Retrieve.TopK
	if h.dynamicTopKEnabled && contextTokensUsed > 0 {
		available := cfg.Retrieve.ContextWindow - cfg.Retrieve.ResponseReserve - contextTokensUsed
		if available < 0 {
			available = 0
		}
		// Rough estimate: each chunk ~400 tokens; cap topK accordingly.
		estimated := available / 400
		if estimated < topK {
			topK = estimated
		}
		if topK < 1 {
			topK = 1
		}
	}

	results, err := (retrieval.Service{
		Config:        cfg,
		Embedder:      h.deps.Embedder,
		Reranker:      h.deps.Reranker,
		RerankEnabled: h.rerankEnabled,
		Stores:        h.deps.Stores.WithStore,
	}).Retrieve(context.Background(), text, topK)
	if err != nil {
		return nil, err
	}

	// Format results.
	chunks := make([]string, 0, len(results))
	for _, r := range results {
		displayText := r.Text
		if r.ParentText != "" {
			displayText = r.ParentText
		}
		chunks = append(chunks, fmt.Sprintf("[来源: %s]\n%s", r.Source, strings.TrimSpace(displayText)))
	}

	return chunks, nil
}

func boolLabel(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
