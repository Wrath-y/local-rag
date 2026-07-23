package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/Wrath-y/local-rag/internal/citation"
	"github.com/Wrath-y/local-rag/internal/provider"
)

const RAGRetrieveToolName = "rag_retrieve"

var ragRetrieveSchema = json.RawMessage(`{"type":"object","additionalProperties":false,"required":["query"],"properties":{"query":{"type":"string","minLength":1,"maxLength":1024},"top_k":{"type":"integer","minimum":1}}}`)

// ToolResult is deliberately structured so only bounded evidence, rather than
// raw tool output, reaches the model.
type ToolResult struct {
	Content  string              `json:"content,omitempty"`
	Evidence []citation.Evidence `json:"evidence,omitempty"`
}

// ToolExecutor is the only execution boundary for Agent tools.
type ToolExecutor interface {
	Definition() provider.ToolDefinition
	Validate(json.RawMessage, ToolBudget) error
	RequiresApproval() bool
	Execute(context.Context, json.RawMessage, ToolBudget) (ToolResult, error)
}

// ToolRegistry is intentionally fixed. Its three knowledge-base mutations
// remain approval-gated; it provides no API for arbitrary tools.
type ToolRegistry struct {
	tools map[string]ToolExecutor
}

func NewToolRegistry(executors ...ToolExecutor) *ToolRegistry {
	r := &ToolRegistry{tools: make(map[string]ToolExecutor)}
	for _, executor := range executors {
		if executor != nil {
			switch executor.Definition().Name {
			case RAGRetrieveToolName, "rag_ingest", "rag_delete_source", "rag_index_rebuild":
				r.tools[executor.Definition().Name] = executor
			}
		}
	}
	return r
}

func (r *ToolRegistry) Definitions() []provider.ToolDefinition {
	if r == nil || len(r.tools) == 0 {
		return nil
	}
	names := []string{RAGRetrieveToolName, "rag_ingest", "rag_delete_source", "rag_index_rebuild"}
	definitions := make([]provider.ToolDefinition, 0, len(r.tools))
	for _, name := range names {
		if tool := r.tools[name]; tool != nil {
			definitions = append(definitions, tool.Definition())
		}
	}
	return definitions
}

func (r *ToolRegistry) Execute(ctx context.Context, call provider.ToolCall, budget ToolBudget) (ToolResult, string, error) {
	if r == nil || r.tools[call.Name] == nil {
		return ToolResult{}, "unregistered_tool", fmt.Errorf("tool %q is not registered", call.Name)
	}
	tool := r.tools[call.Name]
	result, err := tool.Execute(ctx, call.Arguments, budget)
	if err != nil {
		return ToolResult{}, "invalid_request", err
	}
	return result, "", nil
}

func (r *ToolRegistry) Validate(call provider.ToolCall, budget ToolBudget) (bool, string, error) {
	if r == nil || r.tools[call.Name] == nil {
		return false, "unregistered_tool", fmt.Errorf("tool %q is not registered", call.Name)
	}
	tool := r.tools[call.Name]
	if err := tool.Validate(call.Arguments, budget); err != nil {
		return false, "invalid_request", err
	}
	return tool.RequiresApproval(), "", nil
}

// RetrievalFunc bridges Handler retrieval to the Agent without importing the
// handler package (and therefore without a package cycle).
type RetrievalFunc func(context.Context, string, int) ([]citation.Evidence, error)

type ragRetrieveTool struct{ retrieve RetrievalFunc }

func NewRAGRetrieveTool(retrieve RetrievalFunc) ToolExecutor {
	return ragRetrieveTool{retrieve: retrieve}
}

func (ragRetrieveTool) Definition() provider.ToolDefinition {
	return provider.ToolDefinition{
		Name: RAGRetrieveToolName, Description: "Read-only retrieval from the configured RAG knowledge base.", InputSchema: ragRetrieveSchema,
	}
}

func (ragRetrieveTool) RequiresApproval() bool { return false }

func (t ragRetrieveTool) Validate(raw json.RawMessage, budget ToolBudget) error {
	_, _, err := parseRetrieveInput(raw, budget)
	return err
}

func (t ragRetrieveTool) Execute(ctx context.Context, raw json.RawMessage, budget ToolBudget) (ToolResult, error) {
	if t.retrieve == nil {
		return ToolResult{}, fmt.Errorf("rag_retrieve is unavailable")
	}
	query, topK, err := parseRetrieveInput(raw, budget)
	if err != nil {
		return ToolResult{}, err
	}
	evidence, err := t.retrieve(ctx, query, topK)
	if err != nil {
		return ToolResult{}, err
	}
	if len(evidence) > topK {
		evidence = evidence[:topK]
	}
	return ToolResult{Evidence: evidence}, nil
}

func parseRetrieveInput(raw json.RawMessage, budget ToolBudget) (string, int, error) {
	var input struct {
		Query string `json:"query"`
		TopK  *int   `json:"top_k"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return "", 0, fmt.Errorf("rag_retrieve arguments: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return "", 0, fmt.Errorf("rag_retrieve arguments must be one JSON object")
	}
	if strings.TrimSpace(input.Query) == "" || len(input.Query) > 1024 {
		return "", 0, fmt.Errorf("rag_retrieve query must contain 1-1024 characters")
	}
	topK := budget.MaxTopK
	if input.TopK != nil {
		topK = *input.TopK
	}
	if topK < 1 || topK > budget.MaxTopK {
		return "", 0, fmt.Errorf("rag_retrieve top_k must be between 1 and %d", budget.MaxTopK)
	}
	return strings.TrimSpace(input.Query), topK, nil
}

// ApprovedTool is a fixed, named knowledge-base mutation. Its callback is
// supplied by the Handler and only runs after the Agent approval gate.
type ApprovedTool struct {
	Def   provider.ToolDefinition
	Check func(json.RawMessage) error
	Run   func(context.Context, json.RawMessage) (ToolResult, error)
}

func (t ApprovedTool) Definition() provider.ToolDefinition { return t.Def }
func (ApprovedTool) RequiresApproval() bool                { return true }
func (t ApprovedTool) Validate(raw json.RawMessage, _ ToolBudget) error {
	if t.Check == nil {
		return fmt.Errorf("%s is unavailable", t.Def.Name)
	}
	return t.Check(raw)
}
func (t ApprovedTool) Execute(ctx context.Context, raw json.RawMessage, _ ToolBudget) (ToolResult, error) {
	if err := t.Validate(raw, ToolBudget{}); err != nil {
		return ToolResult{}, err
	}
	return t.Run(ctx, raw)
}
