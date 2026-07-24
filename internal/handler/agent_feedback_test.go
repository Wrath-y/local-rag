package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/Wrath-y/local-rag/internal/citation"
	"github.com/Wrath-y/local-rag/internal/config"
	"github.com/Wrath-y/local-rag/internal/provider"
	"github.com/Wrath-y/local-rag/internal/store"
)

type scriptedAgentLLM struct {
	completions []provider.Completion
	index       int
}

func (l *scriptedAgentLLM) Complete(context.Context, []provider.Message) (string, error) {
	return "", nil
}

func (l *scriptedAgentLLM) CompleteWithTools(_ context.Context, _ []provider.Message, _ []provider.ToolDefinition) (provider.Completion, error) {
	result := l.completions[l.index]
	l.index++
	return result, nil
}

func agentFeedbackDeps(t *testing.T, st *store.Store, llm provider.LLMProvider, enabled bool) Deps {
	t.Helper()
	deps := testDeps(t, st)
	deps.LLM = llm
	deps.Config.Feedback = config.FeedbackConfig{
		Enabled: enabled, RetentionDays: 30, QueryExcerptMaxChars: 256,
		NoteMaxChars: 256, ReviewNoteMaxChars: 256, ExportMaxRecords: 100,
		CandidateConversionMax: 100,
	}
	return deps
}

func createAgentSession(t *testing.T, h *Handler) string {
	t.Helper()
	sessions, _, err := h.agentComponents()
	if err != nil {
		t.Fatal(err)
	}
	id, err := sessions.Create("{}")
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func callAgentChat(t *testing.T, h *Handler, sessionID, message string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]string{"session_id": sessionID, "message": message})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/agent/chat", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	h.AgentChat(c)
	return w
}

func TestAgentChatPersistsReturnedEvidenceForFeedback(t *testing.T) {
	st := newTestStore(t)
	if _, err := st.InsertChunk("Redis supports cache penetration protection.", "redis.md", "redis-feedback", "", "", []float32{0, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	llm := &scriptedAgentLLM{completions: []provider.Completion{
		{ToolCalls: []provider.ToolCall{
			{ID: "retrieve-1", Name: "rag_retrieve", Arguments: json.RawMessage(`{"query":"Redis","top_k":1}`)},
			{ID: "retrieve-2", Name: "rag_retrieve", Arguments: json.RawMessage(`{"query":"cache","top_k":1}`)},
		}},
		{Content: "Redis evidence [1] [2]."},
	}}
	h := New(agentFeedbackDeps(t, st, llm, true))
	sessionID := createAgentSession(t, h)
	w := callAgentChat(t, h, sessionID, "How does Redis handle cache penetration?")
	if w.Code != http.StatusOK {
		t.Fatalf("agent chat = %d: %s", w.Code, w.Body.String())
	}
	var response struct {
		RetrievalID string              `json:"retrieval_id"`
		Citations   []citation.Evidence `json:"citations"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.RetrievalID == "" || len(response.Citations) != 2 {
		t.Fatalf("feedback response = %#v", response)
	}
	for _, item := range response.Citations {
		if item.CitationID == "" {
			t.Fatalf("citation lacks durable ID: %#v", item)
		}
	}
	var channel, persistedSession, fingerprint string
	if err := st.DB().QueryRow(`SELECT channel, session_id, query_fingerprint FROM retrieval_events WHERE id=?`, response.RetrievalID).Scan(&channel, &persistedSession, &fingerprint); err != nil {
		t.Fatal(err)
	}
	wantFingerprint, err := st.QueryFingerprint("How does Redis handle cache penetration?")
	if err != nil {
		t.Fatal(err)
	}
	if channel != "agent" || persistedSession != sessionID || fingerprint != wantFingerprint {
		t.Fatalf("persisted event = channel=%q session=%q fingerprint=%q", channel, persistedSession, fingerprint)
	}

	feedbackBody, _ := json.Marshal(store.FeedbackInput{RetrievalID: response.RetrievalID, SessionID: sessionID, Kind: "citation-error", CitationIDs: []string{response.Citations[0].CitationID}})
	feedbackWriter := httptest.NewRecorder()
	feedbackContext, _ := gin.CreateTestContext(feedbackWriter)
	feedbackContext.Request = httptest.NewRequest(http.MethodPost, "/feedback", bytes.NewReader(feedbackBody))
	feedbackContext.Request.Header.Set("Content-Type", "application/json")
	h.CreateFeedback(feedbackContext)
	if feedbackWriter.Code != http.StatusCreated {
		t.Fatalf("feedback = %d: %s", feedbackWriter.Code, feedbackWriter.Body.String())
	}
}

func TestAgentChatOmitsFeedbackIDsWithoutEvidenceOrWhenDisabled(t *testing.T) {
	t.Run("no evidence", func(t *testing.T) {
		st := newTestStore(t)
		h := New(agentFeedbackDeps(t, st, &scriptedAgentLLM{completions: []provider.Completion{{Content: "I need more detail."}}}, true))
		w := callAgentChat(t, h, createAgentSession(t, h), "Clarify this")
		if w.Code != http.StatusOK || bytes.Contains(w.Body.Bytes(), []byte(`"retrieval_id"`)) {
			t.Fatalf("response = %d %s", w.Code, w.Body.String())
		}
		var count int
		if err := st.DB().QueryRow(`SELECT COUNT(*) FROM retrieval_events`).Scan(&count); err != nil || count != 0 {
			t.Fatalf("retrieval event count = %d, %v", count, err)
		}
	})

	t.Run("feedback disabled", func(t *testing.T) {
		st := newTestStore(t)
		if _, err := st.InsertChunk("Redis evidence", "redis.md", "redis-disabled", "", "", []float32{0, 0, 0, 0}); err != nil {
			t.Fatal(err)
		}
		llm := &scriptedAgentLLM{completions: []provider.Completion{
			{ToolCalls: []provider.ToolCall{{ID: "retrieve", Name: "rag_retrieve", Arguments: json.RawMessage(`{"query":"Redis","top_k":1}`)}}},
			{Content: "Redis [1]."},
		}}
		h := New(agentFeedbackDeps(t, st, llm, false))
		w := callAgentChat(t, h, createAgentSession(t, h), "Redis")
		if w.Code != http.StatusOK || bytes.Contains(w.Body.Bytes(), []byte(`"retrieval_id"`)) || bytes.Contains(w.Body.Bytes(), []byte(`"citation_id"`)) {
			t.Fatalf("response = %d %s", w.Code, w.Body.String())
		}
	})
}

func TestPersistRetrievalReportsUnavailableLedger(t *testing.T) {
	st := newTestStore(t)
	h := New(agentFeedbackDeps(t, st, nil, true))
	if err := h.deps.Stores.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := h.persistRetrieval("agent request", "agent", "session", []citation.Evidence{{ChunkID: 1, Source: "source"}})
	if err == nil {
		t.Fatal("persistRetrieval succeeded with no backing store")
	}
}
