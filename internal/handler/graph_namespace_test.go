package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wrath-y/local-rag/internal/config"
	"github.com/Wrath-y/local-rag/internal/graphsnapshot"
	"github.com/Wrath-y/local-rag/internal/store"
	"github.com/gin-gonic/gin"
)

// Real TCP HTTP, production handlers, durable worker, FTS and SQLite. The
// vector provider is deliberately absent: deletion requires no paid model.
func TestNamespaceLifecycleHTTPRealStoreRestartAndIsolation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx := context.Background()
	path := t.TempDir() + "/rag.db"
	var st *store.Store
	var service *graphsnapshot.Service
	var h *Handler
	var server *httptest.Server
	start := func(runWorker bool) {
		var err error
		st, err = store.New(path, 4)
		if err != nil {
			t.Fatal(err)
		}
		service = graphsnapshot.NewService(st, nil)
		if runWorker {
			if err = service.Start(ctx, func(ctx context.Context, task graphsnapshot.Task) error { return service.Dispatch(ctx, task, nil) }); err != nil {
				t.Fatal(err)
			}
		}
		h = New(Deps{Config: &config.Config{}, Store: st, GraphService: service, GraphSnapshotReader: st, GraphTaskReader: st, GraphLifecycle: st})
		r := newGraphSnapshotRouter(h)
		h.RegisterNamespaceLifecycle(r.Group("/v1"))
		r.POST("/v1/graphs/:namespace/traverse", h.TraverseGraph)
		r.GET("/health", h.Health)
		server = httptest.NewServer(r)
	}
	stop := func() { server.Close(); service.Close(); h.Close(); st.Close() }
	start(true)
	defer func() { stop() }()
	call := func(method, path, key string, payload any, want int, target any) {
		t.Helper()
		var body []byte
		if payload != nil {
			body, _ = json.Marshal(payload)
		}
		request, err := http.NewRequest(method, server.URL+path, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		if key != "" {
			request.Header.Set("Idempotency-Key", key)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var raw json.RawMessage
		if response.StatusCode != http.StatusNoContent {
			if err = json.NewDecoder(response.Body).Decode(&raw); err != nil {
				t.Fatal(err)
			}
		}
		if response.StatusCode != want {
			t.Fatalf("%s %s=%d want=%d body=%s", method, path, response.StatusCode, want, raw)
		}
		if target != nil {
			if err = json.Unmarshal(raw, target); err != nil {
				t.Fatal(err)
			}
		}
	}
	call("POST", "/v1/graphs/A/references/sync", "", map[string]any{"owner": "missing-revision", "references": []any{}}, 400, nil)
	var health map[string]any
	call("GET", "/health", "", nil, 200, &health)
	caps, _ := json.Marshal(health["capabilities"])
	if !bytes.Contains(caps, []byte(store.NamespaceLifecycleCapability)) {
		t.Fatal("missing capability")
	}
	create := func(ns, version string, wait bool) {
		t.Helper()
		nodes := []graphsnapshot.Node{{ID: "person", Type: "person", Label: ns, Text: "namespace " + ns, Properties: []byte(`{}`), Provenance: []byte(`{}`)}}
		_, hash, err := graphsnapshot.CanonicalHash(nodes, nil)
		if err != nil {
			t.Fatal(err)
		}
		var snapshot graphsnapshot.Snapshot
		call("PUT", "/v1/graphs/"+ns+"/snapshots/"+version, "", graphsnapshot.Request{SchemaVersion: "1.0", Mode: graphsnapshot.ModeFull, ContentHash: hash, Nodes: nodes}, 202, &snapshot)
		if wait {
			deadline := time.Now().Add(3 * time.Second)
			for {
				var task graphsnapshot.Task
				call("GET", "/v1/tasks/"+snapshot.TaskID, "", nil, 200, &task)
				if task.State == graphsnapshot.TaskSucceeded {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("build timeout %+v", task)
				}
				time.Sleep(5 * time.Millisecond)
			}
		}
	}
	for i := 0; i < 5; i++ {
		create("A", fmt.Sprintf("v%d", i), true)
	}
	create("B", "v1", true)
	call("POST", "/v1/graphs/A/snapshots/v4/activate", "", map[string]any{}, 200, nil)
	call("POST", "/v1/graphs/B/snapshots/v1/activate", "", map[string]any{}, 200, nil)
	var page store.NamespacePage
	call("POST", "/v1/graphs/A/snapshots/query", "", store.NamespaceQuery{Limit: 2}, 200, &page)
	count := len(page.Items)
	generation := page.Generation
	for page.NextCursor != nil {
		call("POST", "/v1/graphs/A/snapshots/query", "", store.NamespaceQuery{Limit: 2, Cursor: *page.NextCursor}, 200, &page)
		count += len(page.Items)
		if generation != page.Generation {
			t.Fatal("pagination drift")
		}
	}
	if count != 5 {
		t.Fatalf("count=%d", count)
	}
	call("POST", "/v1/graphs/A/references/sync", "", store.NamespaceReferenceSync{Owner: "backups", References: []store.SnapshotReference{{ReferenceID: "backup-a", Version: "v0", Kind: "backup"}}}, 200, nil)
	var errorBody graphsnapshot.Error
	call("POST", "/v1/graphs/A/delete", "cleanup", store.NamespaceDeleteRequest{ExpectedGeneration: page.Generation, ManifestHash: page.ManifestHash, Confirm: true}, 409, &errorBody)
	if errorBody.Code != graphsnapshot.CodeNamespaceGenerationConflict {
		t.Fatal(errorBody)
	}
	call("POST", "/v1/graphs/A/snapshots/query", "", store.NamespaceQuery{}, 200, &page)
	call("POST", "/v1/graphs/A/delete", "cleanup", store.NamespaceDeleteRequest{ExpectedGeneration: page.Generation, ManifestHash: page.ManifestHash, Confirm: true}, 409, &errorBody)
	if errorBody.Code != graphsnapshot.CodeSnapshotReferenced {
		t.Fatal(errorBody)
	}
	call("DELETE", "/v1/graphs/A/snapshots/v0", "", nil, 409, &errorBody)
	call("POST", "/v1/graphs/A/references/sync", "", store.NamespaceReferenceSync{Owner: "backups", ExpectedRevision: 1, References: []store.SnapshotReference{}}, 200, nil)
	// Stop only this isolated server. Persist an accepted writer without running
	// it; deletion must survive restart in draining state and keep its receipt.
	stop()
	start(false)
	create("A", "queued", false)
	call("POST", "/v1/graphs/A/snapshots/query", "", store.NamespaceQuery{}, 200, &page)
	request := store.NamespaceDeleteRequest{ExpectedGeneration: page.Generation, ManifestHash: page.ManifestHash, Confirm: true}
	var deletion store.NamespaceDeletionTask
	call("POST", "/v1/graphs/A/delete", "cleanup", request, 202, &deletion)
	call("PUT", "/v1/graphs/A/snapshots/late", "", map[string]any{}, 409, &errorBody)
	if errorBody.Code != graphsnapshot.CodeNamespaceDeleting {
		t.Fatal(errorBody)
	}
	call("GET", "/v1/graphs/A/snapshots/v0", "", nil, 409, nil)
	call("POST", "/v1/graphs/A/references/sync", "", store.NamespaceReferenceSync{Owner: "runs", References: []store.SnapshotReference{}}, 409, nil)
	call("POST", "/v1/graphs/A/snapshots/v0/activate", "", map[string]any{}, 409, nil)
	stop()
	start(true)
	deadline := time.Now().Add(5 * time.Second)
	for {
		call("GET", deletion.StatusPath, "", nil, 200, &deletion)
		if deletion.Complete {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("cleanup timeout %+v", deletion)
		}
		time.Sleep(10 * time.Millisecond)
	}
	originalID := deletion.TaskID
	call("POST", "/v1/graphs/A/delete", "cleanup", request, 202, &deletion)
	if !deletion.Replayed || deletion.TaskID != originalID {
		t.Fatal("idempotence lost")
	}
	call("POST", deletion.StatusPath+"/retry", "", map[string]any{}, 202, &deletion)
	if !deletion.Complete {
		t.Fatal("retry resurrected job")
	}
	call("POST", "/v1/graphs/A/snapshots/query", "", store.NamespaceQuery{}, 200, &page)
	if len(page.Items) != 0 || page.ActiveVersion != nil || page.State != "deleted" {
		t.Fatalf("A residue=%+v", page)
	}
	call("POST", "/v1/graphs/B/traverse", "", map[string]any{"start_node_ids": []string{"person"}, "max_depth": 0}, 200, nil)
	call("POST", "/v1/graphs/B/snapshots/query", "", store.NamespaceQuery{}, 200, &page)
	if len(page.Items) != 1 || page.ActiveVersion == nil || *page.ActiveVersion != "v1" {
		t.Fatalf("B changed=%+v", page)
	}
}
