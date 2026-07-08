package store

import (
	"path/filepath"
	"testing"
)

func TestInsertChunk_Success(t *testing.T) {
	s, _ := New(filepath.Join(t.TempDir(), "test.db"), 3)
	defer s.Close()

	vec := []float32{0.1, 0.2, 0.3}
	id, err := s.InsertChunk("hello world", "test-source", "abc123", "", "", vec)
	if err != nil {
		t.Fatalf("InsertChunk failed: %v", err)
	}
	if id <= 0 {
		t.Errorf("expected positive id, got %d", id)
	}

	// Verify in metadata table
	var text, source string
	s.db.QueryRow("SELECT text, source FROM chunks WHERE id = ?", id).Scan(&text, &source)
	if text != "hello world" || source != "test-source" {
		t.Errorf("got text=%q source=%q", text, source)
	}
}

func TestInsertChunk_DuplicateMD5Skips(t *testing.T) {
	s, _ := New(filepath.Join(t.TempDir(), "test.db"), 3)
	defer s.Close()

	vec := []float32{0.1, 0.2, 0.3}
	s.InsertChunk("hello", "src", "same-md5", "", "", vec)
	id2, err := s.InsertChunk("hello", "src", "same-md5", "", "", vec)
	if err != nil {
		t.Fatal(err)
	}
	if id2 != 0 {
		t.Errorf("expected 0 (skipped), got %d", id2)
	}
}

func TestChunkCount(t *testing.T) {
	s, _ := New(filepath.Join(t.TempDir(), "test.db"), 3)
	defer s.Close()

	vec := []float32{0.1, 0.2, 0.3}
	s.InsertChunk("one", "src", "md5-1", "", "", vec)
	s.InsertChunk("two", "src", "md5-2", "", "", vec)

	count, err := s.ChunkCount()
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Errorf("expected 2, got %d", count)
	}
}
