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
