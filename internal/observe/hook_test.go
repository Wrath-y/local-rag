package observe

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestHookObservations_BoundedMetricsAndSafeLogs(t *testing.T) {
	InitMetrics()
	observations := NewHookObservations()
	if observations.Record(HookOutcome("unknown"), time.Second, HookReasonNone, false) {
		t.Fatal("unknown outcome must be rejected")
	}
	if observations.Record(HookOutcomeTimeout, time.Second, HookReasonCode("SENTINEL_PROMPT"), false) {
		t.Fatal("unbounded reason must be rejected")
	}

	previous := slog.Default()
	var logs bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	if !observations.Record(HookOutcomeInjected, 10*time.Millisecond, HookReasonNone, false) {
		t.Fatal("injected outcome should be accepted")
	}
	if logs.Len() != 0 {
		t.Fatalf("injected outcome must be quiet unless verbose: %s", logs.String())
	}
	if !observations.Record(HookOutcomeServiceUnavailable, 20*time.Millisecond, HookReasonHTTPNonSuccess, false) {
		t.Fatal("service outcome should be accepted")
	}
	if !strings.Contains(logs.String(), `"event":"rag_hook_outcome"`) || !strings.Contains(logs.String(), `"reason_code":"http_non_success"`) {
		t.Fatalf("missing safe structured fields: %s", logs.String())
	}
	if strings.Contains(logs.String(), "SENTINEL_PROMPT") || strings.Contains(logs.String(), "SENTINEL_CONTEXT") {
		t.Fatalf("log leaked hook content: %s", logs.String())
	}

	snapshot := observations.Snapshot()
	if snapshot.TotalEnabledAttempts != 2 || len(snapshot.Outcomes) != 5 {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}
	metrics := string(Render())
	if !strings.Contains(metrics, "rag_hook_outcomes_total") || !strings.Contains(metrics, "rag_hook_latency_seconds") {
		t.Fatalf("hook metrics missing: %s", metrics)
	}
	if strings.Contains(metrics, "unknown") || strings.Contains(metrics, "SENTINEL_PROMPT") {
		t.Fatalf("metrics contain unbounded hook labels: %s", metrics)
	}
}
