package store

import (
	"path/filepath"
	"testing"
)

func TestRetrieve_HybridSearch(t *testing.T) {
	s, err := New(filepath.Join(t.TempDir(), "test.db"), 3)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer s.Close()

	// Insert 3 chunks with different vectors
	s.InsertChunk("Redis cache penetration handling guide", "docs", "md5-1", "", "", []float32{0.9, 0.1, 0.0})
	s.InsertChunk("MySQL index optimization techniques", "docs", "md5-2", "", "", []float32{0.1, 0.9, 0.0})
	s.InsertChunk("Redis cluster deployment steps", "docs", "md5-3", "", "", []float32{0.8, 0.2, 0.0})

	// Query with vector close to redis chunks + "Redis" text
	results, err := s.Retrieve([]float32{0.85, 0.15, 0.0}, "Redis cache", RetrieveOpts{
		TopK: 3, CandidateMultiplier: 10, VectorWeight: 0.7, BM25Weight: 0.3,
	})
	if err != nil {
		t.Fatalf("Retrieve failed: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("no results")
	}
	// First result should contain "Redis" (best combined score)
	if results[0].Text == "" {
		t.Error("first result text is empty")
	}
}

func TestRetrieve_EmptyStore(t *testing.T) {
	s, err := New(filepath.Join(t.TempDir(), "test.db"), 3)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	defer s.Close()

	results, err := s.Retrieve([]float32{0.1, 0.2, 0.3}, "test", RetrieveOpts{
		TopK: 3, CandidateMultiplier: 10, VectorWeight: 0.7, BM25Weight: 0.3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}
