package handler

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/Wrath-y/local-rag/internal/config"
	"github.com/Wrath-y/local-rag/internal/store"
)

func TestExport_ReturnsManifestBackedArchive(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "active.db")
	st, err := store.New(dbPath, 4)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	deps := testDeps(t, st)
	deps.Config.Storage.DBPath = dbPath
	deps.Config.Embedding = config.EmbeddingConfig{
		Provider: "voyage",
		Model:    "voyage-3",
		Dims:     4,
	}
	h := New(deps)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/export", nil)

	h.Export(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); got != "application/zip" {
		t.Errorf("content type = %q, want application/zip", got)
	}
	if got := w.Header().Get("Content-Disposition"); got != "attachment; filename=rag-export.zip" {
		t.Errorf("content disposition = %q", got)
	}

	reader, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	if err != nil {
		t.Fatalf("open export: %v", err)
	}
	var manifestEntry *zip.File
	for _, file := range reader.File {
		if file.Name == BackupManifestEntryName {
			manifestEntry = file
			break
		}
	}
	if manifestEntry == nil {
		t.Fatal("export missing manifest.json")
	}
	body, err := manifestEntry.Open()
	if err != nil {
		t.Fatalf("open manifest: %v", err)
	}
	defer body.Close()

	var manifest BackupManifest
	if err := json.NewDecoder(body).Decode(&manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if manifest.Embedding != (EmbeddingSummary{Provider: "voyage", Model: "voyage-3", Dimensions: 4}) {
		t.Errorf("embedding = %#v", manifest.Embedding)
	}
	if manifest.ChunkCount != 0 {
		t.Errorf("chunk_count = %d, want 0", manifest.ChunkCount)
	}
}

func TestExport_IncludesCommittedWALData(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "active.db")
	st, err := store.New(dbPath, 4)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.InsertChunk("saved in wal", "source", "wal-md5", "", "", []float32{1, 0, 0, 0}); err != nil {
		t.Fatalf("insert chunk: %v", err)
	}

	deps := testDeps(t, st)
	deps.Config.Storage.DBPath = dbPath
	h := New(deps)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/export", nil)
	h.Export(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}

	reader, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	if err != nil {
		t.Fatalf("open export: %v", err)
	}
	var databaseEntry *zip.File
	for _, file := range reader.File {
		if file.Name == BackupDatabaseEntryName {
			databaseEntry = file
			break
		}
	}
	if databaseEntry == nil {
		t.Fatal("export missing database")
	}
	body, err := databaseEntry.Open()
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	var data bytes.Buffer
	if _, err := data.ReadFrom(body); err != nil {
		_ = body.Close()
		t.Fatalf("read database: %v", err)
	}
	if err := body.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}

	exportedPath := filepath.Join(t.TempDir(), "exported.db")
	if err := os.WriteFile(exportedPath, data.Bytes(), 0o600); err != nil {
		t.Fatalf("write exported database: %v", err)
	}
	exported, err := store.New(exportedPath, 4)
	if err != nil {
		t.Fatalf("open exported database: %v", err)
	}
	defer exported.Close()
	count, err := exported.ChunkCount()
	if err != nil {
		t.Fatalf("count exported chunks: %v", err)
	}
	if count != 1 {
		t.Errorf("exported chunk count = %d, want 1", count)
	}
}
