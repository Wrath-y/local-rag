package handler

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Wrath-y/local-rag/internal/citation"
	"github.com/Wrath-y/local-rag/internal/observe"
	"github.com/Wrath-y/local-rag/internal/retrieval"
	"github.com/Wrath-y/local-rag/internal/store"
)

type retrieveRequest struct {
	Text              string `json:"text" binding:"required"`
	ContextTokensUsed int    `json:"context_tokens_used"`
	SessionID         string `json:"session_id"`
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

	evidence, err := h.doRetrieveEvidence(req.Text, req.ContextTokensUsed)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	retrievalID, err := h.persistRetrieval(req.Text, "http", req.SessionID, evidence)
	if err != nil {
		status := http.StatusInternalServerError
		if store.IsValidationError(err) {
			status = http.StatusBadRequest
		}
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}
	manifest := h.citations.Create(evidence)
	chunks := citation.RenderChunks(evidence)
	observe.RetrieveTotal.WithLabelValues(boolLabel(len(chunks) > 0)).Inc()
	c.JSON(http.StatusOK, gin.H{
		"chunks":         chunks,
		"citations":      manifest.Citations,
		"evidence_token": manifest.Token,
		"retrieval_id":   retrievalID,
	})
}

// persistRetrieval creates the durable, privacy-minimized feedback ledger
// entry and maps its opaque citation IDs back onto the existing response
// evidence. It intentionally does not alter ranking or chunk formatting.
func (h *Handler) persistRetrieval(query, channel, sessionID string, evidence []citation.Evidence) (string, error) {
	if h.deps.Config == nil || !h.deps.Config.Feedback.Enabled || h.deps.Stores == nil {
		return "", nil
	}
	var event store.RetrievalEvent
	err := h.deps.Stores.WithStore(func(st *store.Store) error {
		citations := make([]store.RetrievalCitation, len(evidence))
		for i, item := range evidence {
			citations[i] = store.RetrievalCitation{ChunkID: item.ChunkID, Source: item.Source}
		}
		var err error
		event, err = st.RecordRetrieval(store.RecordRetrievalInput{Query: query, Channel: channel, SessionID: sessionID, StoreExcerpt: h.deps.Config.Feedback.StoreQueryExcerpt, ExcerptLimit: h.deps.Config.Feedback.QueryExcerptMaxChars, Citations: citations})
		return err
	})
	if err != nil {
		return "", err
	}
	for i := range evidence {
		if i < len(event.Citations) {
			evidence[i].CitationID = event.Citations[i].CitationID
		}
	}
	return event.RetrievalID, nil
}

// doRetrieveEvidence encapsulates ranked retrieval and deterministic evidence
// assignment. Endpoint-specific callers own their request-scoped manifests.
func (h *Handler) doRetrieveEvidence(text string, contextTokensUsed int) ([]citation.Evidence, error) {
	return h.retrieveEvidence(context.Background(), text, contextTokensUsed, 0)
}

func (h *Handler) retrieveEvidence(ctx context.Context, text string, contextTokensUsed, forcedTopK int) ([]citation.Evidence, error) {
	start := time.Now()
	defer func() {
		observe.RetrieveLatency.Observe(time.Since(start).Seconds())
	}()

	cfg := h.deps.Config

	// Dynamic top_k: reduce k when context window is nearly full.
	topK := cfg.Retrieve.TopK
	if forcedTopK > 0 {
		topK = forcedTopK
	}
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
	}).Retrieve(ctx, text, topK)
	if err != nil {
		return nil, err
	}
	return citation.EvidenceFromResults(results), nil
}

type citationValidationRequest struct {
	EvidenceToken string `json:"evidence_token" binding:"required"`
	Answer        string `json:"answer"`
}

// ValidateCitations validates answer labels against exactly the manifest that
// was returned for its retrieval request. Tokens are short-lived and opaque.
func (h *Handler) ValidateCitations(c *gin.Context) {
	var req citationValidationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	validation, ok := h.citations.Validate(req.EvidenceToken, req.Answer)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "evidence manifest not found or expired"})
		return
	}
	c.JSON(http.StatusOK, validation)
}

func boolLabel(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
