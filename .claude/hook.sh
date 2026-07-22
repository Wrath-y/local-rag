#!/bin/bash
# Claude Code UserPromptSubmit hook — deliberately fail-open and silent.

PAYLOAD=$(cat)

# Disabled mode and empty prompts are bypasses, not hook outcomes. The server
# makes the same check; this local guard prevents client-failure telemetry from
# accidentally counting a bypass when the service is down.
CWD=$(printf '%s' "$PAYLOAD" | jq -r 'if (.cwd | type) == "string" then .cwd else empty end' 2>/dev/null)
HAS_PROMPT=$(printf '%s' "$PAYLOAD" | jq -e '(.prompt | type) == "string" and (.prompt | length) > 0' >/dev/null 2>&1; printf '%s' "$?")
if [ "$HAS_PROMPT" != "0" ] || [ -z "$CWD" ] || [ ! -f "$CWD/.rag-mode" ]; then
  exit 0
fi

# This telemetry is best effort: it runs in the background, has no retry path,
# and redirects all output so it cannot delay or alter hook completion.
report_outcome() {
  local outcome="$1"
  local reason_code="$2"
  (
    curl -s --max-time 1 --output /dev/null -X POST http://127.0.0.1:8765/hook/outcome \
      -H "Content-Type: application/json" \
      -d "{\"outcome\":\"${outcome}\",\"reason_code\":\"${reason_code}\"}" >/dev/null 2>&1
  ) &
}

RESP=$(printf '%s' "$PAYLOAD" | curl -s --fail --max-time 3 -X POST http://127.0.0.1:8765/hook \
  -H "Content-Type: application/json" -d @- 2>/dev/null)
CURL_STATUS=$?
if [ "$CURL_STATUS" -ne 0 ]; then
  case "$CURL_STATUS" in
    28) report_outcome "timeout" "curl_timeout" ;;
    7)  report_outcome "service_unavailable" "connection_refused" ;;
    22) report_outcome "service_unavailable" "http_non_success" ;;
    *)  report_outcome "service_unavailable" "transport_failure" ;;
  esac
  exit 0
fi

if ! printf '%s' "$RESP" | jq -e 'type == "object"' >/dev/null 2>&1; then
  report_outcome "invalid_response" "malformed_json"
  exit 0
fi
if ! printf '%s' "$RESP" | jq -e 'has("additional_context")' >/dev/null 2>&1; then
  report_outcome "invalid_response" "missing_context_field"
  exit 0
fi
if ! printf '%s' "$RESP" | jq -e '.additional_context | type == "string"' >/dev/null 2>&1; then
  report_outcome "invalid_response" "non_string_context"
  exit 0
fi

CTX=$(printf '%s' "$RESP" | jq -r '.additional_context')
if [ -n "$CTX" ]; then
  printf '%s' "$CTX"
fi
