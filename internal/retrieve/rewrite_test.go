package retrieve_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Wrath-y/local-rag/internal/provider"
	"github.com/Wrath-y/local-rag/internal/retrieve"
)

// mockLLM is a test double for provider.LLMProvider.
type mockLLM struct {
	response string
	err      error
}

func (m *mockLLM) Complete(_ context.Context, _ []provider.Message) (string, error) {
	return m.response, m.err
}

func TestRewrite_Expansion(t *testing.T) {
	llm := &mockLLM{response: "Redis cache penetration solution strategies"}

	queries, err := retrieve.Rewrite(context.Background(), llm, "Redis cache miss", "expansion")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(queries) != 1 {
		t.Fatalf("expected 1 query, got %d", len(queries))
	}
	if queries[0] != "Redis cache penetration solution strategies" {
		t.Errorf("unexpected query: %q", queries[0])
	}
}

func TestRewrite_MultiQuery(t *testing.T) {
	llm := &mockLLM{response: "Redis cache miss problem\nHow to handle cache penetration in Redis\nRedis missing cache entries solution"}

	queries, err := retrieve.Rewrite(context.Background(), llm, "Redis cache miss", "multi_query")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(queries) != 3 {
		t.Fatalf("expected 3 queries, got %d: %v", len(queries), queries)
	}
}

func TestRewrite_NilLLM(t *testing.T) {
	queries, err := retrieve.Rewrite(context.Background(), nil, "Redis cache miss", "expansion")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(queries) != 1 {
		t.Fatalf("expected 1 query (passthrough), got %d", len(queries))
	}
	if queries[0] != "Redis cache miss" {
		t.Errorf("expected original query, got %q", queries[0])
	}
}

func TestRewrite_UnknownStrategy(t *testing.T) {
	llm := &mockLLM{response: "some response"}

	queries, err := retrieve.Rewrite(context.Background(), llm, "my query", "unknown_strategy")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(queries) != 1 || queries[0] != "my query" {
		t.Errorf("expected passthrough for unknown strategy, got %v", queries)
	}
}

func TestRewrite_EmptyStrategy(t *testing.T) {
	llm := &mockLLM{response: "some response"}

	queries, err := retrieve.Rewrite(context.Background(), llm, "my query", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(queries) != 1 || queries[0] != "my query" {
		t.Errorf("expected passthrough for empty strategy, got %v", queries)
	}
}

func TestRewrite_LLMError_Fallback(t *testing.T) {
	llm := &mockLLM{err: errors.New("LLM unavailable")}

	queries, err := retrieve.Rewrite(context.Background(), llm, "my query", "expansion")
	if err != nil {
		t.Fatalf("expected graceful fallback, got error: %v", err)
	}
	if len(queries) != 1 || queries[0] != "my query" {
		t.Errorf("expected original query on LLM error, got %v", queries)
	}
}

func TestRewrite_HyDE(t *testing.T) {
	llm := &mockLLM{response: "Redis stores data in memory and uses eviction policies to handle cache misses."}

	queries, err := retrieve.Rewrite(context.Background(), llm, "Redis cache miss", "hyde")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(queries) != 1 {
		t.Fatalf("expected 1 query (hypothetical doc), got %d", len(queries))
	}
	if queries[0] == "Redis cache miss" {
		t.Error("expected HyDE to return a different (expanded) query")
	}
}
