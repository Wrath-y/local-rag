#!/bin/bash
# Claude Code UserPromptSubmit hook — forwards to Go RAG service.
# Fails silently to never block normal conversation.
RESP=$(cat | curl -s --max-time 3 -X POST http://127.0.0.1:8765/hook \
  -H "Content-Type: application/json" -d @- 2>/dev/null) || exit 0
CTX=$(echo "$RESP" | jq -r '.additional_context // empty' 2>/dev/null)
[ -n "$CTX" ] && printf '%s' "$CTX"
