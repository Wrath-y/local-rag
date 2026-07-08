package store

import (
	"path/filepath"
	"testing"
)

func TestNewStore_CreatesTablesAndPragmas(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	s, err := New(dbPath, 512)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer s.Close()

	// Verify WAL mode
	var journalMode string
	if err := s.db.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("querying journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Errorf("journal_mode = %q, want %q", journalMode, "wal")
	}

	// Verify chunks table exists
	var name string
	if err := s.db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='chunks'").Scan(&name); err != nil {
		t.Fatalf("chunks table not found: %v", err)
	}
	if name != "chunks" {
		t.Errorf("table name = %q, want %q", name, "chunks")
	}

	// Verify vec_chunks virtual table exists
	name = ""
	if err := s.db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='vec_chunks'").Scan(&name); err != nil {
		t.Fatalf("vec_chunks table not found: %v", err)
	}
	if name != "vec_chunks" {
		t.Errorf("table name = %q, want %q", name, "vec_chunks")
	}

	// Verify FTS table exists
	name = ""
	if err := s.db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='chunks_fts'").Scan(&name); err != nil {
		t.Fatalf("chunks_fts table not found: %v", err)
	}
	if name != "chunks_fts" {
		t.Errorf("table name = %q, want %q", name, "chunks_fts")
	}
}
