package handler

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/Wrath-y/local-rag/internal/store"
	"github.com/gin-gonic/gin"
)

func TestImport_RequiresExplicitConfirmation(t *testing.T) {
	st := newTestStore(t)
	h := New(testDeps(t, st))
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/import", nil)
	h.Import(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestImport_RestoresConfirmedArchive(t *testing.T) {
	root := t.TempDir()
	activePath := filepath.Join(root, "active.db")
	active, err := store.New(activePath, 4)
	if err != nil {
		t.Fatal(err)
	}
	deps := testDeps(t, active)
	deps.Config.Storage.DBPath = activePath
	deps.Config.Embedding.Provider = "test"
	deps.Config.Embedding.Model = "test"
	deps.Config.Embedding.Dims = 4
	h := New(deps)

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
	archive, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("confirm", "true"); err != nil {
		t.Fatal(err)
	}
	part, err := writer.CreateFormFile("file", "backup.zip")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(archive); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/import", &body)
	c.Request.Header.Set("Content-Type", writer.FormDataContentType())
	h.Import(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	if err := h.deps.Stores.WithStore(func(current *store.Store) error {
		sources, err := current.ListSources()
		if err != nil {
			return err
		}
		if len(sources) != 1 || sources[0].Source != "new-source" {
			t.Fatalf("sources = %#v", sources)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
