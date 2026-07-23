package mcpserver

import (
	"context"
	"testing"

	"github.com/Wrath-y/local-rag/internal/config"
	"github.com/Wrath-y/local-rag/internal/sourcesync"
	"github.com/Wrath-y/local-rag/internal/store"
)

func TestMCPSyncLifecycleUsesSharedTaskService(t *testing.T) {
	st, err := store.New(t.TempDir()+"/sync.db", 4)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := &config.Config{Sync: config.SyncConfig{Enabled: true, Workers: 1, MaxSnapshotBytes: 1024, MaxAttempts: 2}, Embedding: config.EmbeddingConfig{Provider: "test", Model: "model"}}
	svc := sourcesync.New(st, nil, parityEmbedder{}, cfg)
	adapter := &server{sync: svc}
	_, accepted, err := adapter.handleSyncSource(context.Background(), nil, SyncSourceInput{Source: "mcp-source", Documents: []store.SyncDocument{{ID: "doc", Content: "document", Chunks: []store.SyncChunk{{Key: "one", Content: "chunk"}}}}})
	if err != nil || accepted.Task.State != store.SyncQueued {
		t.Fatalf("submit = %#v, %v", accepted, err)
	}
	if !svc.ProcessNext(context.Background()) {
		t.Fatal("queued MCP task was not processed")
	}
	_, status, err := adapter.handleGetSyncStatus(context.Background(), nil, SyncTaskInput{Source: "mcp-source", TaskID: accepted.Task.ID})
	if err != nil || status.State != store.SyncSucceeded {
		t.Fatalf("status = %#v, %v", status, err)
	}
	_, report, err := adapter.handleGetSyncReport(context.Background(), nil, SyncTaskInput{Source: "mcp-source", TaskID: accepted.Task.ID})
	if err != nil || report.EmbeddedChunks != 1 || report.ResultRevision != 1 {
		t.Fatalf("report = %#v, %v", report, err)
	}
}
