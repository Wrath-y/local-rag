package store

import (
	"path/filepath"
	"strings"
	"testing"
)

// helper: open a fresh in-memory-ish test store with 3-dim vectors
func newManageTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(filepath.Join(t.TempDir(), "test.db"), 3)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// insertTestChunk is a convenience wrapper that fatals on error.
func insertTestChunk(t *testing.T, s *Store, text, source, md5 string) {
	t.Helper()
	vec := []float32{0.1, 0.2, 0.3}
	if _, err := s.InsertChunk(text, source, md5, "", "", vec); err != nil {
		t.Fatalf("InsertChunk(%q): %v", text, err)
	}
}

func TestListSources(t *testing.T) {
	s := newManageTestStore(t)

	insertTestChunk(t, s, "alpha one", "src-a", "md5-a1")
	insertTestChunk(t, s, "alpha two", "src-a", "md5-a2")
	insertTestChunk(t, s, "beta one", "src-b", "md5-b1")

	infos, err := s.ListSources()
	if err != nil {
		t.Fatalf("ListSources: %v", err)
	}
	if len(infos) != 2 {
		t.Fatalf("expected 2 sources, got %d: %v", len(infos), infos)
	}

	// Results are ordered by source name (a < b)
	if infos[0].Source != "src-a" || infos[0].Count != 2 {
		t.Errorf("infos[0] = %+v, want {src-a 2}", infos[0])
	}
	if infos[1].Source != "src-b" || infos[1].Count != 1 {
		t.Errorf("infos[1] = %+v, want {src-b 1}", infos[1])
	}
}

func TestDeleteSource(t *testing.T) {
	s := newManageTestStore(t)

	insertTestChunk(t, s, "delete me 1", "to-delete", "del-md5-1")
	insertTestChunk(t, s, "delete me 2", "to-delete", "del-md5-2")
	insertTestChunk(t, s, "keep me", "keep", "keep-md5-1")

	deleted, err := s.DeleteSource("to-delete")
	if err != nil {
		t.Fatalf("DeleteSource: %v", err)
	}
	if deleted != 2 {
		t.Errorf("expected 2 deleted, got %d", deleted)
	}

	count, err := s.ChunkCount()
	if err != nil {
		t.Fatalf("ChunkCount: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 remaining chunk, got %d", count)
	}

	// Verify vec_chunks are also cleaned up
	var vecCount int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM vec_chunks").Scan(&vecCount); err != nil {
		t.Fatalf("count vec_chunks: %v", err)
	}
	if vecCount != 1 {
		t.Errorf("expected 1 vec_chunks entry, got %d", vecCount)
	}
}

func TestDeleteSourceDeletesCanonicalGitDescendantsOnly(t *testing.T) {
	s := newManageTestStore(t)
	insertTestChunk(t, s, "one", "https://example.test/repo.git#guide.md", "git-1")
	insertTestChunk(t, s, "two", "https://example.test/repo.git#api.md", "git-2")
	insertTestChunk(t, s, "keep", "https://example.test/other.git#guide.md", "git-3")
	deleted, err := s.DeleteSource("https://example.test/repo.git")
	if err != nil || deleted != 2 {
		t.Fatalf("deleted=%d err=%v", deleted, err)
	}
	if count, _ := s.ChunkCount(); count != 1 {
		t.Fatalf("remaining=%d", count)
	}
}

func TestDeleteSource_NonExistent(t *testing.T) {
	s := newManageTestStore(t)

	deleted, err := s.DeleteSource("does-not-exist")
	if err != nil {
		t.Fatalf("DeleteSource on missing source should not error: %v", err)
	}
	if deleted != 0 {
		t.Errorf("expected 0 deleted, got %d", deleted)
	}
}

func TestReset(t *testing.T) {
	s := newManageTestStore(t)

	insertTestChunk(t, s, "chunk one", "src-x", "x-md5-1")
	insertTestChunk(t, s, "chunk two", "src-y", "y-md5-1")

	if err := s.Reset(); err != nil {
		t.Fatalf("Reset: %v", err)
	}

	count, err := s.ChunkCount()
	if err != nil {
		t.Fatalf("ChunkCount after Reset: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 chunks after Reset, got %d", count)
	}

	// vec_chunks must also be empty
	var vecCount int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM vec_chunks").Scan(&vecCount); err != nil {
		t.Fatalf("count vec_chunks after Reset: %v", err)
	}
	if vecCount != 0 {
		t.Errorf("expected 0 vec_chunks after Reset, got %d", vecCount)
	}
}

func TestStats(t *testing.T) {
	s := newManageTestStore(t)

	insertTestChunk(t, s, "one", "src", "stats-md5-1")
	insertTestChunk(t, s, "two", "src", "stats-md5-2")

	stats, err := s.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	total, ok := stats["total_chunks"]
	if !ok {
		t.Fatal("Stats missing 'total_chunks' key")
	}
	if total.(int) != 2 {
		t.Errorf("expected total_chunks=2, got %v", total)
	}
}

func TestIntegrityCheck(t *testing.T) {
	s := newManageTestStore(t)

	result, err := s.IntegrityCheck()
	if err != nil {
		t.Fatalf("IntegrityCheck: %v", err)
	}
	if !strings.HasPrefix(result, "ok") {
		t.Errorf("expected 'ok', got %q", result)
	}
}
