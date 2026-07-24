# Local retrieval feedback

Every successful HTTP, hook, and MCP retrieval is recorded locally as an
opaque `retrieval_id`. HTTP `POST /retrieve` and MCP `rag_retrieve` retain the
existing `chunks` response and add `retrieval_id`; each structured citation has
a durable retrieval-scoped `citation_id`. Preserve these values when submitting
feedback later.

## Capture

Use `POST /feedback` (or MCP `rag_feedback_capture`) with one of
`helpful`, `unhelpful`, or `citation-error`:

```json
{"retrieval_id":"...","kind":"citation-error","citation_ids":["..."]}
```

`citation-error` requires at least one citation. A citation must belong to the
specified retrieval. Session IDs are optional, but when provided must name a
known local session and match a session-bound retrieval. Feedback is immutable;
send `supersedes_feedback_id` for a correction from the same retrieval.

## Privacy and retention

The ledger stores IDs, timestamps, channel/configuration metadata, result
rank/source/score snapshots and an HMAC-SHA-256 query fingerprint. The key is
created alongside the SQLite database with restrictive permissions and is never
exported. Plaintext queries, prompts, messages, answers, provider payloads,
network identifiers and account identity are not accepted.

`feedback.store_query_excerpt` is disabled by default. When enabled, excerpts
are redacted for common credential-like values and URL query/fragment portions,
then bounded by `query_excerpt_max_chars`. Notes and excerpts are omitted from
list/export output unless explicitly requested. All feedback data is removed
after `feedback.retention_days` (default 30); cleanup runs before feedback
reads and writes.

## Analysis and review

`GET /feedback`, `/feedback/aggregate`, and `/feedback/export` (and their MCP
counterparts) provide deterministic local listing, safe count aggregation, and
bounded JSON Lines or CSV export. Exports have schema version `v1` and never
include the fingerprint key.

`POST /feedback/candidates/convert` explicitly produces deduplicated pending
candidates. List them at `GET /feedback/candidates`, then review at
`POST /feedback/candidates/{id}/review` with `approved` or `rejected`.

Feedback and candidate approval are offline review artifacts only. They never
change retrieval ranking, reranking, rewriting, embedding, chunking, indexing,
or any online learning behavior.

## How approved candidates affect future retrieval

They do not affect it automatically. Reviewing a candidate as `approved` only
marks it as suitable for an offline evaluation workflow; the next retrieval
continues to use the same configured retrieval, reranking, embedding, and
indexing behavior.

To use approved candidates to improve retrieval, run an explicit, human-owned
workflow:

1. Export or list approved candidates with their provenance.
2. Curate them into an evaluation set and review relevance labels.
3. Run the offline retrieval evaluation and compare the results with the
   approved baseline.
4. If the evidence supports a change, deliberately update retrieval
   configuration, a reranker, or indexed source content, then validate and
   deploy that separate change.

This separation prevents a single feedback record or candidate approval from
silently changing production retrieval behavior.
