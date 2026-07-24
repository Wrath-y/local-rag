package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wrath-y/local-rag/internal/agent"
	"github.com/Wrath-y/local-rag/internal/citation"
	"github.com/Wrath-y/local-rag/internal/provider"
)

type scriptedToolLLM struct {
	mu          sync.Mutex
	completions []provider.Completion
	block       bool
	requests    []toolRequest
}

type toolRequest struct {
	messages []provider.Message
	tools    []provider.ToolDefinition
}

func (l *scriptedToolLLM) Complete(context.Context, []provider.Message) (string, error) {
	return "", errors.New("native tool path expected")
}

func (l *scriptedToolLLM) CompleteWithTools(ctx context.Context, messages []provider.Message, tools []provider.ToolDefinition) (provider.Completion, error) {
	if l.block {
		<-ctx.Done()
		return provider.Completion{}, ctx.Err()
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.requests = append(l.requests, toolRequest{
		messages: append([]provider.Message(nil), messages...),
		tools:    append([]provider.ToolDefinition(nil), tools...),
	})
	if len(l.completions) == 0 {
		return provider.Completion{Content: "done"}, nil
	}
	next := l.completions[0]
	l.completions = l.completions[1:]
	return next, nil
}

func newLoop(t *testing.T, llm provider.LLMProvider, retrieve agent.RetrievalFunc, budget agent.ToolBudget) (*agent.AgentLoop, *agent.SessionManager, string) {
	t.Helper()
	sm, err := agent.NewSessionManager(openTestDB(t))
	if err != nil {
		t.Fatal(err)
	}
	sessionID, err := sm.Create("")
	if err != nil {
		t.Fatal(err)
	}
	registry := agent.NewToolRegistry(agent.NewRAGRetrieveTool(retrieve))
	return agent.NewAgentLoop(llm, sm, registry, budget), sm, sessionID
}

func retrieveEvidence(_ context.Context, query string, topK int) ([]citation.Evidence, error) {
	if query != "redis" || topK != 1 {
		return nil, errors.New("unexpected retrieval input")
	}
	return []citation.Evidence{{Source: "kb", Excerpt: "Redis uses a cache."}}, nil
}

func toolCall(name string) provider.ToolCall {
	return provider.ToolCall{ID: "call-1", Name: name, Arguments: json.RawMessage(`{"query":"redis","top_k":1}`)}
}

func TestToolLoopRetrievesEvidenceAndPersistsSafeTrace(t *testing.T) {
	llm := &scriptedToolLLM{completions: []provider.Completion{{ToolCalls: []provider.ToolCall{toolCall(agent.RAGRetrieveToolName)}}, {Content: "Redis is cached [1]."}}}
	loop, sm, sessionID := newLoop(t, llm, retrieveEvidence, agent.DefaultToolBudget())
	result, err := loop.ChatWithResult(context.Background(), sessionID, "How does Redis work?", "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != agent.OutcomeCompleted || result.Response != "Redis is cached [1]." || len(result.Evidence) != 1 || result.Evidence[0].Label != "[1]" {
		t.Fatalf("unexpected result: %#v", result)
	}
	traces, err := sm.ListToolTraces(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(traces) != 1 || traces[0].Tool != agent.RAGRetrieveToolName || traces[0].ResultCount != 1 || len(traces[0].EvidenceIDs) != 1 || traces[0].EvidenceIDs[0] != 1 {
		t.Fatalf("unexpected traces: %#v", traces)
	}
}

func TestToolLoopRejectsUnregisteredToolWithoutRetrieval(t *testing.T) {
	calls := 0
	llm := &scriptedToolLLM{completions: []provider.Completion{{ToolCalls: []provider.ToolCall{toolCall("shell")}}, {Content: "I cannot run that tool."}}}
	loop, sm, sessionID := newLoop(t, llm, func(context.Context, string, int) ([]citation.Evidence, error) { calls++; return nil, nil }, agent.DefaultToolBudget())
	result, err := loop.ChatWithResult(context.Background(), sessionID, "run shell", "")
	if err != nil || result.Outcome != agent.OutcomeCompleted || calls != 0 {
		t.Fatalf("result=%#v calls=%d err=%v", result, calls, err)
	}
	traces, err := sm.ListToolTraces(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(traces) != 1 || traces[0].ErrorCategory != "unregistered_tool" || traces[0].Outcome != "rejected" {
		t.Fatalf("unexpected trace: %#v", traces)
	}
}

func TestToolLoopTerminatesAtToolBudget(t *testing.T) {
	llm := &scriptedToolLLM{completions: []provider.Completion{{ToolCalls: []provider.ToolCall{toolCall(agent.RAGRetrieveToolName)}}, {ToolCalls: []provider.ToolCall{toolCall(agent.RAGRetrieveToolName)}}}}
	budget := agent.DefaultToolBudget()
	budget.MaxToolCalls = 1
	loop, _, sessionID := newLoop(t, llm, retrieveEvidence, budget)
	result, err := loop.ChatWithResult(context.Background(), sessionID, "redis", "")
	if err != nil || result.Outcome != agent.OutcomeBudgetExhausted || result.ToolCalls != 1 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestToolLoopExposesConfiguredTopKAndCorrectsInvalidRetrieveArguments(t *testing.T) {
	llm := &scriptedToolLLM{completions: []provider.Completion{
		{ToolCalls: []provider.ToolCall{{ID: "invalid", Name: agent.RAGRetrieveToolName, Arguments: json.RawMessage(`{"query":"redis","top_k":10}`)}}},
		{ToolCalls: []provider.ToolCall{toolCall(agent.RAGRetrieveToolName)}},
		{Content: "Redis is cached [1]."},
	}}
	budget := agent.DefaultToolBudget()
	budget.MaxTopK = 3
	loop, _, sessionID := newLoop(t, llm, retrieveEvidence, budget)
	result, err := loop.ChatWithResult(context.Background(), sessionID, "redis", "")
	if err != nil || result.Outcome != agent.OutcomeCompleted || result.ToolCalls != 2 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if len(llm.requests) != 3 {
		t.Fatalf("request count=%d, want 3", len(llm.requests))
	}
	var schema struct {
		Properties struct {
			TopK struct {
				Maximum int `json:"maximum"`
			} `json:"top_k"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(llm.requests[0].tools[0].InputSchema, &schema); err != nil || schema.Properties.TopK.Maximum != 3 {
		t.Fatalf("schema=%s maximum=%d err=%v", llm.requests[0].tools[0].InputSchema, schema.Properties.TopK.Maximum, err)
	}
	lastMessage := llm.requests[1].messages[len(llm.requests[1].messages)-1]
	if lastMessage.Role != "user" || !strings.Contains(lastMessage.Content, "top_k between 1 and 3") {
		t.Fatalf("correction=%#v", lastMessage)
	}
}

func TestToolLoopReservesFinalAnswerRoundAfterToolBudget(t *testing.T) {
	llm := &scriptedToolLLM{completions: []provider.Completion{
		{ToolCalls: []provider.ToolCall{toolCall(agent.RAGRetrieveToolName)}},
		{Content: "Redis is cached [1]."},
	}}
	budget := agent.DefaultToolBudget()
	budget.MaxRounds = 1
	budget.MaxToolCalls = 1
	loop, _, sessionID := newLoop(t, llm, retrieveEvidence, budget)
	result, err := loop.ChatWithResult(context.Background(), sessionID, "redis", "")
	if err != nil || result.Outcome != agent.OutcomeCompleted || result.Response != "Redis is cached [1]." || result.Rounds != 2 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestToolLoopHonorsTimeoutAndCancellation(t *testing.T) {
	budget := agent.DefaultToolBudget()
	budget.Deadline = time.Millisecond
	loop, _, sessionID := newLoop(t, &scriptedToolLLM{block: true}, retrieveEvidence, budget)
	result, err := loop.ChatWithResult(context.Background(), sessionID, "redis", "")
	if err != nil || result.Outcome != agent.OutcomeTimedOut {
		t.Fatalf("timeout result=%#v err=%v", result, err)
	}
	loop, _, sessionID = newLoop(t, &scriptedToolLLM{}, retrieveEvidence, agent.DefaultToolBudget())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err = loop.ChatWithResult(ctx, sessionID, "redis", "")
	if err != nil || result.Outcome != agent.OutcomeCancelled {
		t.Fatalf("cancel result=%#v err=%v", result, err)
	}
}

func TestFallbackToolJSONRequiresValidatedArguments(t *testing.T) {
	valid, err := agent.ParseFallbackToolJSON(`{"tool_calls":[{"id":"a","name":"rag_retrieve","arguments":{"query":"redis"}}]}`)
	if err != nil || len(valid.ToolCalls) != 1 {
		t.Fatalf("valid fallback = %#v, %v", valid, err)
	}
	invalid, err := agent.ParseFallbackToolJSON(`{"tool_calls":[{"name":"rag_retrieve"}]}`)
	if err != nil || len(invalid.ToolCalls) != 0 {
		t.Fatalf("invalid fallback = %#v, %v", invalid, err)
	}
}

func TestMutationToolRequiresAndUsesOneTimeApproval(t *testing.T) {
	db := openTestDB(t)
	sm, err := agent.NewSessionManager(db)
	if err != nil {
		t.Fatal(err)
	}
	sessionID, err := sm.Create("")
	if err != nil {
		t.Fatal(err)
	}
	executions := 0
	mutation := agent.ApprovedTool{
		Def:   provider.ToolDefinition{Name: "rag_ingest", InputSchema: json.RawMessage(`{"type":"object"}`)},
		Check: func(json.RawMessage) error { return nil },
		Run: func(context.Context, json.RawMessage) (agent.ToolResult, error) {
			executions++
			return agent.ToolResult{Content: "ingested"}, nil
		},
	}
	llm := &scriptedToolLLM{completions: []provider.Completion{{ToolCalls: []provider.ToolCall{{ID: "write-1", Name: "rag_ingest", Arguments: json.RawMessage(`{"source":"manual","text":"hello"}`)}}}}}
	loop := agent.NewAgentLoop(llm, sm, agent.NewToolRegistry(mutation))
	result, err := loop.ChatWithResult(context.Background(), sessionID, "add this", "")
	if err != nil || result.Outcome != agent.OutcomePermissionRequired || result.Permission == nil || executions != 0 {
		t.Fatalf("result=%#v executions=%d err=%v", result, executions, err)
	}
	approved, err := loop.Approve(context.Background(), sessionID, result.Permission.Token, true)
	if err != nil || !approved.Executed || executions != 1 {
		t.Fatalf("approved=%#v executions=%d err=%v", approved, executions, err)
	}
	if _, err := loop.Approve(context.Background(), sessionID, result.Permission.Token, true); err == nil {
		t.Fatal("approval token was reusable")
	}
}
