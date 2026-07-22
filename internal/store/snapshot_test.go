package store

import (
	"path/filepath"
	"testing"
)

func TestSnapshot_CapturesCommittedWALData(t *testing.T) {
	active, err := New(filepath.Join(t.TempDir(), "active.db"), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer active.Close()
	if _, err := active.InsertChunk("snapshot text", "snapshot-source", "snapshot-md5", "", "", []float32{1, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}

	snapshotPath := filepath.Join(t.TempDir(), "snapshot.db")
	if err := active.Snapshot(snapshotPath); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	snapshot, err := New(snapshotPath, 4)
	if err != nil {
		t.Fatalf("open snapshot: %v", err)
	}
	defer snapshot.Close()
	count, err := snapshot.ChunkCount()
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("snapshot chunks = %d, want 1", count)
	}
}
