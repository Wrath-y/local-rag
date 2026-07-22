package handler

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/Wrath-y/local-rag/internal/store"
)

func TestRestoreService_RestoresValidatedArchive(t *testing.T) {
	root := t.TempDir()
	activePath := filepath.Join(root, "active.db")
	active, err := store.New(activePath, 4)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := active.InsertChunk("old", "old-source", "old", "", "", []float32{1, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}

	importPath := filepath.Join(root, "import.db")
	imported, err := store.New(importPath, 4)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := imported.InsertChunk("new", "new-source", "new", "", "", []float32{1, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	if err := imported.Close(); err != nil {
		t.Fatal(err)
	}
	archivePath := createRestoreArchive(t, importPath)

	lifecycle := NewStoreLifecycle(active)
	service := NewRestoreService(lifecycle, activePath, 4)
	result, err := service.Restore(archivePath)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if result.Stage != RestoreStageComplete || result.RolledBack {
		t.Errorf("result = %#v", result)
	}
	if err := lifecycle.WithStore(func(current *store.Store) error {
		sources, err := current.ListSources()
		if err != nil {
			return err
		}
		if len(sources) != 1 || sources[0].Source != "new-source" {
			t.Errorf("sources after restore = %#v", sources)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreService_ReloadFailureRollsBack(t *testing.T) {
	root := t.TempDir()
	activePath := filepath.Join(root, "active.db")
	active, err := store.New(activePath, 4)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := active.InsertChunk("old", "old-source", "old", "", "", []float32{1, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	importPath := filepath.Join(root, "import.db")
	imported, err := store.New(importPath, 4)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := imported.InsertChunk("new", "new-source", "new", "", "", []float32{1, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	if err := imported.Close(); err != nil {
		t.Fatal(err)
	}

	lifecycle := NewStoreLifecycle(active)
	service := NewRestoreService(lifecycle, activePath, 4)
	primary := errors.New("candidate reload failed")
	openCalls := 0
	service.open = func(path string, dims int) (*store.Store, error) {
		openCalls++
		if openCalls == 1 {
			return nil, primary
		}
		return store.New(path, dims)
	}
	result, err := service.Restore(createRestoreArchive(t, importPath))
	if result.Stage != RestoreStageReload || !result.RolledBack {
		t.Errorf("result = %#v", result)
	}
	if !errors.Is(err, primary) {
		t.Errorf("restore error does not preserve primary failure: %v", err)
	}
	if err := lifecycle.WithStore(func(current *store.Store) error {
		sources, err := current.ListSources()
		if err != nil {
			return err
		}
		if len(sources) != 1 || sources[0].Source != "old-source" {
			return fmt.Errorf("sources after rollback = %#v", sources)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func createRestoreArchive(t *testing.T, databasePath string) string {
	t.Helper()
	data, err := createDBZip(databasePath, BackupPackageMetadata{Embedding: EmbeddingSummary{Provider: "test", Model: "test", Dimensions: 4}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "import.zip")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
