package handler

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wrath-y/local-rag/internal/config"
	"github.com/Wrath-y/local-rag/internal/sourcesync"
	"github.com/Wrath-y/local-rag/internal/store"
	"github.com/gin-gonic/gin"
)

func TestSyncHTTPSubmissionAndSourceScopedStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)
	st, err := store.New(t.TempDir()+"/sync.db", 2)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := &config.Config{Sync: config.SyncConfig{Enabled: true, Workers: 1, MaxSnapshotBytes: 1024, MaxAttempts: 2}}
	svc := sourcesync.New(st, nil, nil, cfg)
	// Do not start the worker: this test is about the asynchronous acceptance
	// contract, so the task should remain observably queued.
	h := New(Deps{Config: &config.Config{}, Store: st, Sync: svc})
	r := gin.New()
	r.POST("/sources/:source/syncs", h.SyncSubmit)
	r.GET("/sources/:source/syncs/:task", h.SyncStatus)

	body := []byte(`{"documents":[{"id":"doc-1","content":"text"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/sources/source-a/syncs", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "same")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("submit status = %d: %s", w.Code, w.Body.String())
	}
	task, _, err := svc.Submit(store.SyncSnapshot{Source: "source-a", Documents: []store.SyncDocument{{ID: "doc-1", Content: "text"}}}, "same")
	if err != nil {
		t.Fatal(err)
	}
	wrong := httptest.NewRequest(http.MethodGet, "/sources/other/syncs/"+task.ID, nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, wrong)
	if w.Code != http.StatusNotFound {
		t.Fatalf("cross-source status = %d: %s", w.Code, w.Body.String())
	}
}

func TestSyncHTTPRejectsDuplicateDocumentIDs(t *testing.T) {
	gin.SetMode(gin.TestMode)
	st, err := store.New(t.TempDir()+"/sync.db", 2)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc := sourcesync.New(st, nil, nil, &config.Config{Sync: config.SyncConfig{Enabled: true, Workers: 1, MaxSnapshotBytes: 1024, MaxAttempts: 2}})
	h := New(Deps{Config: &config.Config{}, Store: st, Sync: svc})
	r := gin.New()
	r.POST("/sources/:source/syncs", h.SyncSubmit)
	req := httptest.NewRequest(http.MethodPost, "/sources/source-a/syncs", bytes.NewBufferString(`{"documents":[{"id":"same","content":"one"},{"id":"same","content":"two"}]}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("duplicate submit = %d: %s", w.Code, w.Body.String())
	}
}
