package retrieve

import (
	"context"
	"strings"

	"github.com/Wrath-y/local-rag/internal/provider"
)

// Rewrite applies query rewriting using the specified strategy.
// Returns a list of rewritten queries:
//   - "expansion": 1 query with synonyms/related terms expanded
//   - "hyde": 1 hypothetical document that answers the query
//   - "multi_query": 3 variant queries
//   - unknown/empty: [query] passthrough
//
// If llm is nil or returns an error, returns [query] as a graceful fallback.
func Rewrite(ctx context.Context, llm provider.LLMProvider, query, strategy string) ([]string, error) {
	if llm == nil || strategy == "" {
		return []string{query}, nil
	}

	switch strategy {
	case "expansion":
		return rewriteExpansion(ctx, llm, query)
	case "hyde":
		return rewriteHyDE(ctx, llm, query)
	case "multi_query":
		return rewriteMultiQuery(ctx, llm, query)
	default:
		return []string{query}, nil
	}
}

// rewriteExpansion asks the LLM to expand the query with synonyms and related terms.
func rewriteExpansion(ctx context.Context, llm provider.LLMProvider, query string) ([]string, error) {
	prompt := "Rewrite the following search query by adding relevant synonyms and related terms to improve retrieval. " +
		"Return only the rewritten query, no explanations.\n\nQuery: " + query

	messages := []provider.Message{
		{Role: "user", Content: prompt},
	}

	result, err := llm.Complete(ctx, messages)
	if err != nil {
		return []string{query}, nil //nolint:nilerr // graceful fallback
	}

	result = strings.TrimSpace(result)
	if result == "" {
		return []string{query}, nil
	}
	return []string{result}, nil
}

// rewriteHyDE generates a hypothetical document that answers the query (HyDE strategy).
func rewriteHyDE(ctx context.Context, llm provider.LLMProvider, query string) ([]string, error) {
	prompt := "Write a short hypothetical document that would be the ideal answer to the following query. " +
		"Return only the document text, no explanations.\n\nQuery: " + query

	messages := []provider.Message{
		{Role: "user", Content: prompt},
	}

	result, err := llm.Complete(ctx, messages)
	if err != nil {
		return []string{query}, nil //nolint:nilerr // graceful fallback
	}

	result = strings.TrimSpace(result)
	if result == "" {
		return []string{query}, nil
	}
	return []string{result}, nil
}

// rewriteMultiQuery asks the LLM to generate 3 variant queries.
// Each variant should be on its own line.
func rewriteMultiQuery(ctx context.Context, llm provider.LLMProvider, query string) ([]string, error) {
	prompt := "Generate 3 different variants of the following search query to improve retrieval coverage. " +
		"Return exactly 3 queries, one per line, with no numbering or extra text.\n\nQuery: " + query

	messages := []provider.Message{
		{Role: "user", Content: prompt},
	}

	result, err := llm.Complete(ctx, messages)
	if err != nil {
		return []string{query}, nil //nolint:nilerr // graceful fallback
	}

	result = strings.TrimSpace(result)
	if result == "" {
		return []string{query}, nil
	}

	// Split by newlines and filter empty lines.
	lines := strings.Split(result, "\n")
	var queries []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			queries = append(queries, line)
		}
	}

	if len(queries) == 0 {
		return []string{query}, nil
	}
	return queries, nil
}
