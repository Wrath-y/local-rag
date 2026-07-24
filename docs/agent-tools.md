# Agent tool loop (v1)

`POST /agent/chat` runs a bounded, evidence-grounded tool loop. Version 1
registers four fixed tools: `rag_retrieve`, `rag_ingest`, `rag_delete_source`,
and `rag_index_rebuild`.

```json
{"query":"How is Redis cache penetration handled?","top_k":3}
```

`rag_retrieve` is read-only. `query` is required and is limited to 1,024
characters; `top_k` is optional, must be positive, and is capped by
`agent.max_top_k`. The tool schema sent to the model exposes that configured
upper bound. If its retrieval arguments are invalid, the model receives a
safe correction that includes the allowed `top_k` range.

The three knowledge-base mutations require explicit approval. When a model
requests one, `/agent/chat` returns `outcome: "permission_required"` and a
session-bound `permission_request` token. The client presents the operation to
the user and then calls `POST /agent/permission/:token` with `session_id` and
`approved`. The opaque token executes exactly one validated request, cannot be
reused, and expires after five minutes. Denial, expiry, or a different session
executes nothing. Shell, network, arbitrary filesystem, and unregistered tools
remain unavailable.

The Agent uses native tool calls for OpenAI-compatible and Anthropic providers.
Providers that implement only the basic completion interface can request a tool
by returning the documented structured JSON envelope; the envelope and its
arguments are strictly decoded before the same fixed registry is consulted.
Ordinary non-JSON output is treated as the final answer.

Each request is bounded by `max_rounds`, `max_tool_calls`, `deadline_seconds`,
`max_context_bytes`, `max_result_bytes`, and `max_top_k`. On exhaustion,
timeout, or cancellation, `/agent/chat` returns a transparent `outcome` rather
than allowing further tool execution. The runtime reserves one model round
after the allowed tool calls for the final answer, automatically normalizing
`max_rounds` to at least `max_tool_calls + 1`.

Retrieved evidence is assigned request-scoped labels (`[1]`, `[2]`, …). The
final response is validated against that request's evidence manifest and the
response contains `citations`, `evidence_token`, and `citation_validation`.
Tool traces stored in SQLite retain only timing, tool name, safe outcome/error
category, result count, and evidence IDs; they never retain prompts or
retrieved excerpts. Prometheus exports `rag_agent_tool_calls_total`,
`rag_agent_tool_latency_seconds`, and `rag_agent_terminal_total`.

## Feedback-eligible Agent responses

When local retrieval-feedback capture is enabled and an Agent chat response
contains evidence, `POST /agent/chat` additionally returns a non-empty
`retrieval_id`. Each item in `citations` then includes a durable,
retrieval-scoped `citation_id`. The complete evidence returned for that answer
(including evidence from multiple read-only tool calls) is represented by one
retrieval event with channel `agent`, the initiating user message under the
configured privacy policy, and the Agent `session_id`.

Use those additive fields with `POST /feedback`, for example:

```json
{"retrieval_id":"...","session_id":"...","kind":"helpful"}
```

or a citation-specific report:

```json
{"retrieval_id":"...","session_id":"...","kind":"citation-error","citation_ids":["..."]}
```

Existing Agent response fields and outcomes are unchanged. When no evidence is
returned or feedback capture is disabled, no `retrieval_id` or durable
`citation_id` is supplied; clients must not submit feedback with invented IDs.
If the required feedback ledger cannot be recorded, the chat request fails
safely instead of returning unusable feedback controls. Feedback is an
offline artifact and does not automatically alter Agent retrieval behavior.
