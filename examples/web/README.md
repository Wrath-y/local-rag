# Local RAG HTTP Test Client

A single static web page for manually calling the Local RAG HTTP API.

## Start

Run the RAG server from the repository root:

```bash
./rag-server
```

Build it first if needed:

```bash
go build -o rag-server ./cmd/server/
./rag-server
```

Serve this directory in another terminal:

```bash
cd examples/web
python3 -m http.server 8080
```

Open <http://127.0.0.1:8080>. The default API address is `http://127.0.0.1:8765`.

Open <http://127.0.0.1:8080/agent-tool-test.html> to manually test Agent
retrieval and the approval flow for knowledge-base ingest, source deletion, and
index rebuild.

Open <http://127.0.0.1:8080/retrieval-feedback-test.html> to exercise every
HTTP endpoint in the retrieval-feedback loop. Its API address defaults to
`http://127.0.0.1:8765` and can be changed in the page.

Open <http://127.0.0.1:8080/agent-feedback-console.html> for the Chinese
dark-mode Agent conversation and feedback console. It has no build step or
third-party assets; its API address defaults to `http://127.0.0.1:8765` and is
editable in the left rail.

Create a conversation (or select a listed server session), then send one
message at a time. Selecting an existing session continues the Agent's
server-side context but intentionally does not restore a visual transcript;
the visible transcript exists only in the current page memory. A clarification
question is an ordinary Agent turn: answer it in the same composer to continue.

If a response asks for a mutation approval, inspect the server-provided
operation, tool, and expiry and explicitly approve or reject it in the card.
The page sends exactly one decision to `POST /agent/permission/:token`, never
calls a mutation endpoint itself, and discards a token after its decision.

Evidence-backed responses can expose Helpful, Unhelpful, and selected
citation-error feedback. These calls use `POST /feedback` with the current
session ID. Feedback requires the server's durable `retrieval_id` and citation
IDs; if feedback capture is disabled, evidence is absent, or the server
rejects the request, the controls explain why they are unavailable. Feedback
is a local offline review artifact and never changes retrieval automatically.

## Available actions

| Page action | API endpoint |
| --- | --- |
| Check health | `GET /health` |
| Ingest text | `POST /ingest` |
| Retrieve chunks | `POST /retrieve` |
| Refresh statistics | `GET /stats` |
| Refresh sources | `GET /sources` |

The page does not include destructive operations such as source deletion or database reset.

## CORS

The static page normally runs on port `8080`, while the RAG API runs on port `8765`; browsers treat them as different origins. If a request fails without an HTTP status, first confirm the RAG server address and process are correct. If the browser console reports a CORS error, allow `http://127.0.0.1:8080` in the server's local CORS configuration or serve the page from an allowed origin.
