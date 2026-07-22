package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/Wrath-y/local-rag/internal/observe"
)

func hookRequestForTest(t *testing.T, h *Handler, prompt, cwd string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(hookRequest{Prompt: prompt, CWD: cwd})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/hook", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	h.Hook(c)
	return w
}

func TestHookObservability_RecordsNoResultsAndBypasses(t *testing.T) {
	st := newTestStore(t)
	h := New(testDeps(t, st))
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, ".rag-mode"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	w := hookRequestForTest(t, h, "SENTINEL_PROMPT", cwd)
	if w.Code != http.StatusOK || w.Body.String() != "{\"additional_context\":\"\"}" {
		t.Fatalf("unexpected no-results hook response: %d %q", w.Code, w.Body.String())
	}
	snapshot := h.hookObservations.Snapshot()
	if snapshot.TotalEnabledAttempts != 1 || snapshot.Outcomes[observe.HookOutcomeNoResults] != 1 {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}

	_ = hookRequestForTest(t, h, "", cwd)
	_ = hookRequestForTest(t, h, "SENTINEL_PROMPT", t.TempDir())
	snapshot = h.hookObservations.Snapshot()
	if snapshot.TotalEnabledAttempts != 1 {
		t.Fatalf("bypasses must not be observed: %#v", snapshot)
	}
}

func TestHookObservability_RecordsInjectedAndStatsRemainSafe(t *testing.T) {
	st := newTestStore(t)
	if _, err := st.InsertChunk("SENTINEL_CONTEXT", "source", "hook-observability", "", "", []float32{0, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	h := New(testDeps(t, st))
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, ".rag-mode"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	w := hookRequestForTest(t, h, "SENTINEL_PROMPT", cwd)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "SENTINEL_CONTEXT") {
		t.Fatalf("expected injected context, got %d %q", w.Code, w.Body.String())
	}
	snapshot := h.hookObservations.Snapshot()
	if snapshot.Outcomes[observe.HookOutcomeInjected] != 1 {
		t.Fatalf("expected injected outcome, got %#v", snapshot)
	}

	w = httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/stats", nil)
	h.Stats(c)
	if w.Code != http.StatusOK {
		t.Fatalf("stats: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "SENTINEL_PROMPT") || strings.Contains(w.Body.String(), "SENTINEL_CONTEXT") {
		t.Fatalf("stats leaked hook content: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "hook_observability") || !strings.Contains(w.Body.String(), "verbose_enabled") {
		t.Fatalf("stats missing hook observability: %s", w.Body.String())
	}
}

func TestHookOutcomeReport_OnlyAcceptsClientOwnedAllowListedOutcomes(t *testing.T) {
	h := New(testDeps(t, newTestStore(t)))
	for _, body := range []string{
		`{"outcome":"timeout","reason_code":"curl_timeout"}`,
		`{"outcome":"service_unavailable","reason_code":"http_non_success"}`,
		`{"outcome":"invalid_response","reason_code":"malformed_json"}`,
	} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/hook/outcome", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		h.HookOutcomeReport(c)
		if w.Code != http.StatusNoContent {
			t.Fatalf("report %s: got %d", body, w.Code)
		}
	}
	for _, body := range []string{
		`{"outcome":"injected","reason_code":""}`,
		`{"outcome":"timeout","reason_code":"unbounded error"}`,
		`{"outcome":"unknown","reason_code":"curl_timeout"}`,
	} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/hook/outcome", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		h.HookOutcomeReport(c)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("invalid report %s: got %d", body, w.Code)
		}
	}
	snapshot := h.hookObservations.Snapshot()
	if snapshot.TotalEnabledAttempts != 3 {
		t.Fatalf("unexpected client report total: %#v", snapshot)
	}
}

func TestVerboseToggle_SetsExplicitStateForHookDiagnostics(t *testing.T) {
	h := New(testDeps(t, newTestStore(t)))
	for _, tc := range []struct {
		path string
		code int
		want string
	}{
		{"/retrieve/verbose?enabled=true", http.StatusOK, `"verbose_enabled":true`},
		{"/retrieve/verbose?enabled=false", http.StatusOK, `"verbose_enabled":false`},
		{"/retrieve/verbose?enabled=maybe", http.StatusBadRequest, ""},
	} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, tc.path, nil)
		h.VerboseToggle(c)
		if w.Code != tc.code || (tc.want != "" && !strings.Contains(w.Body.String(), tc.want)) {
			t.Fatalf("%s: got %d %s", tc.path, w.Code, w.Body.String())
		}
	}
}
