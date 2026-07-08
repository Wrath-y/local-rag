package provider

import "context"

// RerankProvider re-ranks a list of candidate documents against a query.
type RerankProvider interface {
	// Rerank returns up to topN results sorted by descending relevance score.
	Rerank(ctx context.Context, query string, documents []string, topN int) ([]RerankResult, error)
}
