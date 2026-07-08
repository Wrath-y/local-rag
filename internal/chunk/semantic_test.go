package chunk

import (
	"context"
	"testing"

	"github.com/Wrath-y/local-rag/internal/config"
)

// mockEmbedder is a simple mock that returns pre-set embeddings.
type mockEmbedder struct {
	vectors [][]float32
	err     error
}

func (m *mockEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	if m.err != nil {
		return nil, m.err
	}
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = m.vectors[i%len(m.vectors)]
	}
	return out, nil
}

func (m *mockEmbedder) Dims() int {
	if len(m.vectors) > 0 {
		return len(m.vectors[0])
	}
	return 0
}

// sequentialEmbedder returns embeddings in the exact order they are provided.
type sequentialEmbedder struct {
	vecs [][]float32
}

func (s *sequentialEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		if i < len(s.vecs) {
			out[i] = s.vecs[i]
		} else {
			out[i] = s.vecs[len(s.vecs)-1]
		}
	}
	return out, nil
}

func (s *sequentialEmbedder) Dims() int {
	if len(s.vecs) > 0 {
		return len(s.vecs[0])
	}
	return 0
}

func semanticCfg() config.ChunkConfig {
	return config.ChunkConfig{
		MinTokens: 1,
		MaxTokens: 500,
		Semantic: config.SemanticChunkConfig{
			ThresholdPercentile: 80,
			MinChunkSize:        1,
			MaxChunkSize:        20,
		},
	}
}

// TestSemanticChunker_NilEmbedderFallback verifies that a nil embedder falls
// back to FixedChunker without error.
func TestSemanticChunker_NilEmbedderFallback(t *testing.T) {
	text := "Hello world. This is a test. Another sentence here."
	c := NewSemanticChunker(semanticCfg(), nil)
	chunks, err := c.Chunk(text, "src")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) == 0 {
		t.Fatal("expected at least one chunk")
	}
}

// TestSemanticChunker_EmbedErrorFallback verifies that an embedding error
// triggers the FixedChunker fallback.
func TestSemanticChunker_EmbedErrorFallback(t *testing.T) {
	embedder := &mockEmbedder{err: context.DeadlineExceeded}
	c := NewSemanticChunker(semanticCfg(), embedder)
	text := "First sentence. Second sentence. Third sentence here."
	chunks, err := c.Chunk(text, "src")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chunks) == 0 {
		t.Fatal("expected fallback chunks")
	}
}

// TestSemanticChunker_SplitOnLowSimilarity verifies that sentences with very
// different embeddings are split into separate chunks.
// Two clusters: sentences 0-2 share vector A, sentences 3-5 share vector B.
// A and B are orthogonal so cosine(A,B)=0, triggering a breakpoint.
func TestSemanticChunker_SplitOnLowSimilarity(t *testing.T) {
	vecA := []float32{1, 0, 0}
	vecB := []float32{0, 1, 0}

	seqEmbedder := &sequentialEmbedder{
		vecs: [][]float32{vecA, vecA, vecA, vecB, vecB, vecB},
	}

	cfg := semanticCfg()
	cfg.Semantic.ThresholdPercentile = 50 // cut at median similarity
	c := NewSemanticChunker(cfg, seqEmbedder)

	// 6 sentences — 3 similar to each other, then 3 similar to each other.
	text := "Alpha one. Alpha two. Alpha three. Beta one. Beta two. Beta three."
	chunks, err := c.Chunk(text, "src")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// With orthogonal vectors between clusters we expect at least 2 chunks.
	if len(chunks) < 2 {
		t.Errorf("expected >= 2 chunks from dissimilar clusters, got %d", len(chunks))
	}
}

// TestSemanticChunker_EmptyText returns nil for empty input.
func TestSemanticChunker_EmptyText(t *testing.T) {
	c := NewSemanticChunker(semanticCfg(), nil)
	chunks, err := c.Chunk("", "src")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if chunks != nil {
		t.Errorf("expected nil for empty text, got %v", chunks)
	}
}
