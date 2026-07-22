package mcpserver

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/Wrath-y/local-rag/internal/management"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestManagementToolsAreDiscoverableWithConfirmationSchemas(t *testing.T) {
	ctx := context.Background()
	mcpServer := newMCPServer(Deps{Management: management.New(management.Deps{})})
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := mcpServer.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()
	tools, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(tools.Tools))
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	for _, expected := range []string{"rag_update_source", "rag_reset", "rag_export", "rag_import", "rag_backup_run", "rag_backup_list", "rag_backup_restore", "rag_storage_integrity_check", "rag_index_rebuild", "rag_index_status", "rag_retrieval_config_get", "rag_retrieval_config_set", "rag_task_status"} {
		if !slices.Contains(names, expected) {
			t.Errorf("tool list does not include %q: %v", expected, names)
		}
	}
	for _, tool := range tools.Tools {
		if tool.Name != "rag_reset" && tool.Name != "rag_import" && tool.Name != "rag_backup_restore" && tool.Name != "rag_update_source" {
			continue
		}
		data, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "confirm") {
			t.Errorf("%s schema lacks confirm: %s", tool.Name, data)
		}
	}
}

func TestManagementToolsRejectUnconfirmedRequests(t *testing.T) {
	server := &server{deps: Deps{Management: management.New(management.Deps{})}}
	result, _, err := server.handleReset(context.Background(), nil, ResetInput{})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || !result.IsError {
		t.Fatalf("unconfirmed reset result = %#v, want MCP error", result)
	}
	result, _, err = server.handleUpdateSource(context.Background(), nil, UpdateSourceInput{Source: "source", Content: "content"})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || !result.IsError {
		t.Fatalf("unconfirmed update result = %#v, want MCP error", result)
	}
}

func TestTaskStatusReportsUnknownTask(t *testing.T) {
	server := &server{deps: Deps{Management: management.New(management.Deps{})}}
	result, _, err := server.handleTaskStatus(context.Background(), nil, TaskStatusInput{TaskID: "unknown"})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || !result.IsError {
		t.Fatalf("unknown task result = %#v, want MCP error", result)
	}
}

func TestRetrievalConfigurationToolUsesSharedService(t *testing.T) {
	server := &server{deps: Deps{Management: management.New(management.Deps{})}}
	enabled := true
	result, output, err := server.handleRetrievalConfigSet(context.Background(), nil, RetrievalConfigSetInput{RerankEnabled: &enabled})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || result.IsError || !output.RerankEnabled {
		t.Fatalf("set output = %#v, result = %#v", output, result)
	}
}
