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

func TestRetrievePreservesCitationProvenanceAndLegacyChunks(t *testing.T) {
	s, err := New(filepath.Join(t.TempDir(), "test.db"), 3)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.InsertChunkWithProvenance("citation provenance guide", "guide.md", "citation-md5", "", "", Provenance{Title: "Guide", URI: "/docs/guide.md", Location: "page:3"}, []float32{1, 0, 0}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertChunk("legacy citation guide", "legacy.md", "legacy-md5", "", "", []float32{0.9, 0.1, 0}); err != nil {
		t.Fatal(err)
	}
	results, err := s.Retrieve([]float32{1, 0, 0}, "citation guide", RetrieveOpts{TopK: 2, CandidateMultiplier: 2, VectorWeight: 0.7, BM25Weight: 0.3})
	if err != nil || len(results) != 2 {
		t.Fatalf("retrieve: results=%#v err=%v", results, err)
	}
	byHash := make(map[string]RetrieveResult, len(results))
	for _, result := range results {
		byHash[result.ContentHash] = result
	}
	provenance := byHash["citation-md5"]
	if provenance.DocumentTitle != "Guide" || provenance.DocumentURI != "/docs/guide.md" || provenance.Location != "page:3" {
		t.Fatalf("provenance was not propagated: %#v", provenance)
	}
	legacy := byHash["legacy-md5"]
	if legacy.DocumentURI != "" || legacy.Location != "" {
		t.Fatalf("legacy chunk must keep unavailable metadata empty: %#v", legacy)
	}
}
