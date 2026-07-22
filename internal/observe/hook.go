package observe

import (
	"log/slog"
	"sync"
	"time"
)

// HookOutcome is the terminal result of an enabled, non-empty RAG hook attempt.
type HookOutcome string

const (
	HookOutcomeInjected           HookOutcome = "injected"
	HookOutcomeNoResults          HookOutcome = "no_results"
	HookOutcomeTimeout            HookOutcome = "timeout"
	HookOutcomeServiceUnavailable HookOutcome = "service_unavailable"
	HookOutcomeInvalidResponse    HookOutcome = "invalid_response"
)

// HookReasonCode is deliberately finite so diagnostics cannot include raw errors.
type HookReasonCode string

const (
	HookReasonNone                HookReasonCode = ""
	HookReasonRetrievalError      HookReasonCode = "retrieval_error"
	HookReasonCurlTimeout         HookReasonCode = "curl_timeout"
	HookReasonConnectionRefused   HookReasonCode = "connection_refused"
	HookReasonHTTPNonSuccess      HookReasonCode = "http_non_success"
	HookReasonTransportFailure    HookReasonCode = "transport_failure"
	HookReasonMalformedJSON       HookReasonCode = "malformed_json"
	HookReasonMissingContextField HookReasonCode = "missing_context_field"
	HookReasonNonStringContext    HookReasonCode = "non_string_context"
)

var hookOutcomes = map[HookOutcome]struct{}{
	HookOutcomeInjected: {}, HookOutcomeNoResults: {}, HookOutcomeTimeout: {},
	HookOutcomeServiceUnavailable: {}, HookOutcomeInvalidResponse: {},
}

var hookReasonCodes = map[HookReasonCode]struct{}{
	HookReasonNone: {}, HookReasonRetrievalError: {}, HookReasonCurlTimeout: {},
	HookReasonConnectionRefused: {}, HookReasonHTTPNonSuccess: {},
	HookReasonTransportFailure: {}, HookReasonMalformedJSON: {},
	HookReasonMissingContextField: {}, HookReasonNonStringContext: {},
}

func ValidHookOutcome(outcome HookOutcome) bool {
	_, ok := hookOutcomes[outcome]
	return ok
}

func ValidHookReasonCode(reason HookReasonCode) bool {
	_, ok := hookReasonCodes[reason]
	return ok
}

// ClientReportedHookOutcome reports terminal states which cannot be inferred by
// the successful /hook handler response.
func ClientReportedHookOutcome(outcome HookOutcome) bool {
	return outcome == HookOutcomeTimeout || outcome == HookOutcomeServiceUnavailable || outcome == HookOutcomeInvalidResponse
}

// HookLatest is deliberately limited to non-sensitive terminal metadata.
type HookLatest struct {
	Outcome    HookOutcome    `json:"outcome"`
	ElapsedMS  int64          `json:"elapsed_ms"`
	ReasonCode HookReasonCode `json:"reason_code,omitempty"`
}

// HookSnapshot contains process-lifetime, safe hook observability data.
type HookSnapshot struct {
	TotalEnabledAttempts uint64                 `json:"total_enabled_attempts"`
	Outcomes             map[HookOutcome]uint64 `json:"outcomes"`
	Latest               *HookLatest            `json:"latest,omitempty"`
}

// HookObservations stores hook results in memory. A Handler owns one instance,
// so its counters reset when the service process creates a new handler.
type HookObservations struct {
	mu       sync.Mutex
	total    uint64
	outcomes map[HookOutcome]uint64
	latest   *HookLatest
}

func NewHookObservations() *HookObservations {
	outcomes := make(map[HookOutcome]uint64, len(hookOutcomes))
	for outcome := range hookOutcomes {
		outcomes[outcome] = 0
	}
	return &HookObservations{outcomes: outcomes}
}

// Record stores one valid terminal outcome and emits only allow-listed fields.
func (o *HookObservations) Record(outcome HookOutcome, elapsed time.Duration, reason HookReasonCode, verbose bool) bool {
	if !ValidHookOutcome(outcome) || !ValidHookReasonCode(reason) {
		return false
	}
	elapsedMS := elapsed.Milliseconds()
	if elapsedMS < 0 {
		elapsedMS = 0
	}

	o.mu.Lock()
	o.total++
	o.outcomes[outcome]++
	o.latest = &HookLatest{Outcome: outcome, ElapsedMS: elapsedMS, ReasonCode: reason}
	o.mu.Unlock()

	HookOutcomesTotal.WithLabelValues(string(outcome)).Inc()
	HookLatency.Observe(elapsed.Seconds())
	logHookEvent(outcome, elapsedMS, reason, verbose)
	return true
}

func (o *HookObservations) Snapshot() HookSnapshot {
	o.mu.Lock()
	defer o.mu.Unlock()

	outcomes := make(map[HookOutcome]uint64, len(o.outcomes))
	for outcome, count := range o.outcomes {
		outcomes[outcome] = count
	}
	var latest *HookLatest
	if o.latest != nil {
		copy := *o.latest
		latest = &copy
	}
	return HookSnapshot{TotalEnabledAttempts: o.total, Outcomes: outcomes, Latest: latest}
}

func logHookEvent(outcome HookOutcome, elapsedMS int64, reason HookReasonCode, verbose bool) {
	if outcome == HookOutcomeInjected && !verbose {
		return
	}
	args := []any{"event", "rag_hook_outcome", "outcome", string(outcome), "elapsed_ms", elapsedMS}
	if reason != HookReasonNone {
		args = append(args, "reason_code", string(reason))
	}
	if outcome == HookOutcomeInjected || outcome == HookOutcomeNoResults {
		slog.Info("rag hook outcome", args...)
		return
	}
	slog.Warn("rag hook outcome", args...)
}
