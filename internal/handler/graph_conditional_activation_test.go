package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Wrath-y/local-rag/internal/config"
	"github.com/Wrath-y/local-rag/internal/graphsnapshot"
	"github.com/Wrath-y/local-rag/internal/operability"
	"github.com/Wrath-y/local-rag/internal/store"
	"github.com/gin-gonic/gin"
)

func activationHTTPFixture(t *testing.T) (*store.Store, *gin.Engine, string) {
	t.Helper()
	st, err := store.New(filepath.Join(t.TempDir(), "rag.db"), 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	_, hash, err := graphsnapshot.CanonicalHash(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"base", "left", "right"} {
		task := "build-" + version
		svc := graphsnapshot.NewService(st, func() (string, error) { return task, nil })
		if _, err := svc.Put(context.Background(), "project", version, graphsnapshot.Request{SchemaVersion: "1.0", Mode: graphsnapshot.ModeFull, ContentHash: hash}); err != nil {
			t.Fatal(err)
		}
		if err := st.PromoteGraphComponent(context.Background(), task); err != nil {
			t.Fatal(err)
		}
		if err := st.PopulateGraphSearchDocuments(context.Background(), task); err != nil {
			t.Fatal(err)
		}
		if err := st.MarkGraphVectorUnavailable(context.Background(), task, "not configured"); err != nil {
			t.Fatal(err)
		}
	}
	h := New(Deps{Config: &config.Config{}, Store: st, GraphSnapshotReader: st, GraphLifecycle: st})
	t.Cleanup(h.Close)
	router := newGraphSnapshotRouter(h)
	router.GET("/health", h.Health)
	return st, router, hash
}

func TestConditionalActivationHTTPContract(t *testing.T) {
	_, router, hash := activationHTTPFixture(t)
	for _, test := range []struct {
		path, body string
		status     int
		contains   []string
	}{
		{"/active/query", `{}`, 200, []string{`"active_version":null`, `"content_hash":null`}},
		{"/snapshots/base/activate", `{"expected_active_version":null}`, 200, []string{`"active_version":"base"`, `"changed":true`}},
		{"/active/query", ``, 200, []string{`"active_version":"base"`, `"content_hash":"` + hash + `"`}},
		{"/snapshots/base/activate", `{"expected_active_version":null}`, 409, []string{`"code":"ACTIVE_VERSION_CONFLICT"`, `"retryable":false`, `"expected_active_version":null`, `"active_version":"base"`}},
		{"/snapshots/base/activate", `{"expected_active_version":"base"}`, 200, []string{`"changed":false`}},
		{"/snapshots/left/activate", `{"expected_active_version":"base"}`, 200, []string{`"changed":true`}},
		{"/snapshots/right/activate", `{"expected_active_version":"base"}`, 409, []string{`"code":"ACTIVE_VERSION_CONFLICT"`, `"active_version":"left"`}},
		{"/snapshots/right/activate", ``, 200, []string{`"changed":true`}},
		{"/snapshots/right/activate", `{}`, 200, []string{`"changed":false`}},
	} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/graphs/project"+test.path, strings.NewReader(test.body)))
		if w.Code != test.status {
			t.Fatalf("%s %s => %d %s", test.path, test.body, w.Code, w.Body)
		}
		for _, want := range test.contains {
			if !strings.Contains(w.Body.String(), want) {
				t.Fatalf("missing %s: %s", want, w.Body)
			}
		}
	}
	for _, body := range []string{`null`, `[]`, `{"unexpected":1}`, `{"expected_active_version":1}`, `{"expected_active_version":""}`, `{"expected_active_version":null,"expected_active_version":"base"}`, `{} {}`} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/graphs/project/snapshots/base/activate", strings.NewReader(body)))
		if w.Code != 400 {
			t.Fatalf("accepted malformed activation %s: %d %s", body, w.Code, w.Body)
		}
	}
	for _, body := range []string{`null`, `[]`, `{"expected_active_version":"base"}`} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/graphs/project/active/query", strings.NewReader(body)))
		if w.Code != 400 {
			t.Fatalf("accepted malformed query %s: %d %s", body, w.Code, w.Body)
		}
	}
}

func TestConditionalActivationLostHTTPResponseReconcilesByHeadAndHash(t *testing.T) {
	_, router, hash := activationHTTPFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Drop-Response") == "true" {
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, r)
			if recorder.Code != 200 {
				t.Errorf("activation before dropped reply: %d %s", recorder.Code, recorder.Body)
			}
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			conn.Close()
			return
		}
		router.ServeHTTP(w, r)
	}))
	defer server.Close()
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/graphs/project/snapshots/base/activate", strings.NewReader(`{"expected_active_version":null}`))
	request.Header.Set("Drop-Response", "true")
	response, err := server.Client().Do(request)
	if err == nil {
		response.Body.Close()
		t.Fatal("expected lost activation response")
	}
	response, err = server.Client().Post(server.URL+"/v1/graphs/project/active/query", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var active graphsnapshot.ActiveSnapshot
	if err := json.NewDecoder(response.Body).Decode(&active); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 || active.ActiveVersion == nil || *active.ActiveVersion != "base" || active.ContentHash == nil || *active.ContentHash != hash {
		t.Fatalf("reconciliation=%+v HTTP=%d", active, response.StatusCode)
	}
}

func TestConditionalActivationPostCommitFailureStillExposesCommittedHead(t *testing.T) {
	st, router, hash := activationHTTPFixture(t)
	// Damage only this disposable derived-index table. Activation commits the
	// head before retention reads it, producing a real post-commit server error.
	if _, err := st.DB().Exec(`DROP TABLE graph_retrieval_generations`); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/graphs/project/snapshots/base/activate", strings.NewReader(`{"expected_active_version":null}`)))
	if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), `"code":"INTERNAL_ERROR"`) {
		t.Fatalf("post-commit error = %d %s", w.Code, w.Body)
	}
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/graphs/project/active/query", nil))
	var active graphsnapshot.ActiveSnapshot
	if err := json.Unmarshal(w.Body.Bytes(), &active); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusOK || active.ActiveVersion == nil || *active.ActiveVersion != "base" || active.ContentHash == nil || *active.ContentHash != hash {
		t.Fatalf("committed head lost after 500: %d %+v", w.Code, active)
	}
	// The original expectation stays stale after an uncertain result. It must
	// not be silently reinterpreted as an unconditional idempotent replay.
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/graphs/project/snapshots/base/activate", strings.NewReader(`{"expected_active_version":null}`)))
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"code":"ACTIVE_VERSION_CONFLICT"`) {
		t.Fatalf("post-commit stale replay = %d %s", w.Code, w.Body)
	}
}

func TestConditionalActivationHealthAndUnsupportedBackend(t *testing.T) {
	st, router, _ := activationHTTPFixture(t)
	for _, test := range []struct {
		router *gin.Engine
		want   string
	}{
		{router, "available"},
		{func() *gin.Engine {
			h := New(Deps{Config: &config.Config{}, Store: st, GraphLifecycle: graphLifecycleFake{}})
			t.Cleanup(h.Close)
			r := newGraphSnapshotRouter(h)
			r.GET("/health", h.Health)
			return r
		}(), "unavailable"},
	} {
		w := httptest.NewRecorder()
		test.router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/health", nil))
		var health operability.Health
		if err := json.Unmarshal(w.Body.Bytes(), &health); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, capability := range health.Capabilities {
			if capability.Name == graphsnapshot.ConditionalActivationCapability {
				found = true
				if string(capability.State) != test.want {
					t.Fatalf("capability=%+v want %s", capability, test.want)
				}
			}
		}
		if !found {
			t.Fatal("conditional capability missing")
		}
		if test.want == "unavailable" {
			w = httptest.NewRecorder()
			test.router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/graphs/project/snapshots/base/activate", strings.NewReader(`{"expected_active_version":null}`)))
			if w.Code != 503 {
				t.Fatalf("unsupported condition silently ignored: %d %s", w.Code, w.Body)
			}
			w = httptest.NewRecorder()
			test.router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/graphs/project/snapshots/base/activate", nil))
			if w.Code != 200 {
				body, _ := io.ReadAll(w.Body)
				t.Fatalf("legacy path failed: %s", body)
			}
		}
	}
}
