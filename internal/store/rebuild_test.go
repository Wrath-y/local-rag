package store

import (
	"bytes"
	"path/filepath"
	"testing"
)

func TestShadowIndexValidationCutoverAndRollback(t *testing.T) {
	s, err := New(filepath.Join(t.TempDir(), "rebuild.db"), 3)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	id, err := s.InsertChunk("stable chunk", "test", "stable", "", "", []float32{1, 0, 0})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.SnapshotChunks()
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot) != 1 || snapshot[0].ID != id {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}
	if err := s.CreateShadowIndex("vec_chunks_rebuild_test"); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertShadowVectors("vec_chunks_rebuild_test", snapshot, [][]float32{{0, 1, 0}}); err != nil {
		t.Fatal(err)
	}
	if err := s.ValidateShadowIndex("vec_chunks_rebuild_test", snapshot); err != nil {
		t.Fatal(err)
	}
	if err := s.ActivateShadowIndex("vec_chunks_rebuild_test", "vec_chunks_retired_test"); err != nil {
		t.Fatal(err)
	}

	var active, retired []byte
	if err := s.DB().QueryRow(`SELECT embedding FROM vec_chunks WHERE chunk_id = ?`, id).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().QueryRow(`SELECT embedding FROM vec_chunks_retired_test WHERE chunk_id = ?`, id).Scan(&retired); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(active, retired) {
		t.Fatal("cutover did not activate the shadow vector")
	}
	if err := s.RollbackActiveIndex("vec_chunks_retired_test", "vec_chunks_failed_test"); err != nil {
		t.Fatal(err)
	}
	var restored []byte
	if err := s.DB().QueryRow(`SELECT embedding FROM vec_chunks WHERE chunk_id = ?`, id).Scan(&restored); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(restored, retired) {
		t.Fatal("rollback did not restore the retained active index")
	}
}

func TestValidateShadowIndexRejectsMissingVectors(t *testing.T) {
	s, err := New(filepath.Join(t.TempDir(), "rebuild.db"), 2)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.InsertChunk("first", "test", "one", "", "", []float32{1, 0}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertChunk("second", "test", "two", "", "", []float32{0, 1}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.SnapshotChunks()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateShadowIndex("vec_chunks_rebuild_missing"); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertShadowVectors("vec_chunks_rebuild_missing", snapshot[:1], [][]float32{{1, 0}}); err != nil {
		t.Fatal(err)
	}
	if err := s.ValidateShadowIndex("vec_chunks_rebuild_missing", snapshot); err == nil {
		t.Fatal("expected validation to reject missing vector")
	}
}
