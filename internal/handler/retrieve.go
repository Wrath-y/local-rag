package handler

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Wrath-y/local-rag/internal/observe"
	"github.com/Wrath-y/local-rag/internal/store"
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

	opts := store.RetrieveOpts{
		TopK:                topK,
		CandidateMultiplier: cfg.Retrieve.CandidateMultiplier,
		VectorWeight:        cfg.Retrieve.ScoreWeights.Vector,
		BM25Weight:          cfg.Retrieve.ScoreWeights.BM25,
	}

	// Embed the query.
	queryPrefix := cfg.Embedding.QueryPrefix
	queryText := text
	if queryPrefix != "" {
		queryText = queryPrefix + text
	}

	vecs, err := h.deps.Embedder.Embed(context.Background(), []string{queryText})
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	if len(vecs) == 0 {
		return nil, fmt.Errorf("embedder returned no vectors")
	}
	queryVec := vecs[0]

	// Hybrid retrieval under the lifecycle read lock.
	var results []store.RetrieveResult
	err = h.deps.Stores.WithStore(func(st *store.Store) error {
		var retrieveErr error
		results, retrieveErr = st.Retrieve(queryVec, text, opts)
		return retrieveErr
	})
	if err != nil {
		return nil, fmt.Errorf("retrieve: %w", err)
	}

	// Optional rerank.
	if h.rerankEnabled && h.deps.Reranker != nil && len(results) > 0 {
		docs := make([]string, len(results))
		for i, r := range results {
			docs[i] = r.Text
		}
		topN := cfg.Retrieve.RerankCandidates
		if topN <= 0 || topN > len(docs) {
			topN = len(docs)
		}
		reranked, err := h.deps.Reranker.Rerank(context.Background(), text, docs, topN)
		if err == nil && len(reranked) > 0 {
			// Reorder results according to rerank order.
			rerankedResults := make([]store.RetrieveResult, 0, len(reranked))
			for _, rr := range reranked {
				if rr.Index >= 0 && rr.Index < len(results) {
					rerankedResults = append(rerankedResults, results[rr.Index])
				}
			}
			if len(rerankedResults) > 0 {
				results = rerankedResults
			}
		}
		// If rerank fails, fall through with original ordering.
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
