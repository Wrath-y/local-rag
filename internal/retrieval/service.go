// Package retrieval contains the production ranked-retrieval composition.
// It deliberately returns structured evidence; HTTP callers retain ownership of
// their response formatting and offline callers can evaluate the same path.
package retrieval

import (
	"context"
	"fmt"

	"github.com/Wrath-y/local-rag/internal/config"
	"github.com/Wrath-y/local-rag/internal/provider"
	"github.com/Wrath-y/local-rag/internal/store"
)

// StoreAccess executes a read operation against the currently active store.
// Handler store lifecycle management satisfies this interface without exposing
// its locking implementation to other packages.
type StoreAccess func(func(*store.Store) error) error

// Service is the shared, production retrieval composition. It only performs
// embedding, hybrid retrieval, and optional reranking; it never formats an HTTP
// response or invokes an LLM.
type Service struct {
	Config        *config.Config
	Embedder      provider.EmbedProvider
	Reranker      provider.RerankProvider
	RerankEnabled bool
	Stores        StoreAccess
}

// Retrieve returns ranked retrieval evidence for query. topKOverride is used
// by the HTTP dynamic-top-k policy; zero uses the configured value.
func (s Service) Retrieve(ctx context.Context, query string, topKOverride int) ([]store.RetrieveResult, error) {
	if s.Config == nil || s.Embedder == nil || s.Stores == nil {
		return nil, fmt.Errorf("retrieval service is not fully configured")
	}
	topK := s.Config.Retrieve.TopK
	if topKOverride > 0 {
		topK = topKOverride
	}
	opts := store.RetrieveOpts{
		TopK:                topK,
		CandidateMultiplier: s.Config.Retrieve.CandidateMultiplier,
		VectorWeight:        s.Config.Retrieve.ScoreWeights.Vector,
		BM25Weight:          s.Config.Retrieve.ScoreWeights.BM25,
	}

	queryForEmbedding := query
	if prefix := s.Config.Embedding.QueryPrefix; prefix != "" {
		queryForEmbedding = prefix + query
	}
	vecs, err := s.Embedder.Embed(ctx, []string{queryForEmbedding})
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	if len(vecs) == 0 {
		return nil, fmt.Errorf("embedder returned no vectors")
	}

	var results []store.RetrieveResult
	if err := s.Stores(func(st *store.Store) error {
		var retrieveErr error
		results, retrieveErr = st.Retrieve(vecs[0], query, opts)
		return retrieveErr
	}); err != nil {
		return nil, fmt.Errorf("retrieve: %w", err)
	}

	if s.RerankEnabled && s.Reranker != nil && len(results) > 0 {
		docs := make([]string, len(results))
		for i, result := range results {
			docs[i] = result.Text
		}
		topN := s.Config.Retrieve.RerankCandidates
		if topN <= 0 || topN > len(docs) {
			topN = len(docs)
		}
		reranked, rerankErr := s.Reranker.Rerank(ctx, query, docs, topN)
		if rerankErr == nil && len(reranked) > 0 {
			rerankedResults := make([]store.RetrieveResult, 0, len(reranked))
			for _, result := range reranked {
				if result.Index >= 0 && result.Index < len(results) {
					rerankedResults = append(rerankedResults, results[result.Index])
				}
			}
			if len(rerankedResults) > 0 {
				results = rerankedResults
			}
		}
	}
	return results, nil
}
