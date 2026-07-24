<div align="center">

# 🧠 Local RAG

**Give Claude Code persistent long-term memory — retrieve knowledge precisely from your own documents**

[![Go](https://img.shields.io/badge/Go-1.22+-00ADD8?style=flat-square&logo=go&logoColor=white)](https://go.dev/)
[![SQLite](https://img.shields.io/badge/SQLite-vec0+FTS5-003B57?style=flat-square&logo=sqlite&logoColor=white)](https://github.com/asg017/sqlite-vec)
[![Gin](https://img.shields.io/badge/Gin-HTTP-00ADD8?style=flat-square)](https://gin-gonic.com/)
[![License](https://img.shields.io/badge/License-MIT-green?style=flat-square)](LICENSE)
[![Claude Code](https://img.shields.io/badge/Claude_Code-Plugin-orange?style=flat-square)](https://claude.ai/code)

[Installation](#installation) · [Usage](#usage) · [Configuration](#configuration) · [Architecture](#architecture) · [Command Reference](#command-reference) · [How It Works](#how-it-works) · [FAQ](#faq)

📖 [中文文档](README_zh.md)

🧪 [Retrieval evaluation](docs/retrieval-evaluation.md)

📝 [Local retrieval feedback](docs/retrieval-feedback.md)

🔧 [Agent tool loop boundary](docs/agent-tools.md)

</div>

---

## Why Do You Need This?

Claude Code has no built-in vector database. Its native memory system is file-based (CLAUDE.md) and **lacks semantic retrieval**.

| Native Limitation | Symptom | This Plugin's Solution |
|-------------------|---------|------------------------|
| Forget on session close | New conversation — Claude knows nothing about last session | Documents stored in a local vector DB, persisted forever, always available |
| Large docs burn tokens | Pasting a 100-page manual into the chat costs a fortune just reading the doc | Only retrieves relevant excerpts; the rest is never transmitted |
| No cross-document semantic search | Claude can't "remember" multiple documents and search by meaning | All documents are indexed uniformly, returning the most relevant content by semantics |

> 🔒 **All data stored locally. Nothing is uploaded to any server.**

---

## Installation

### Prerequisites

| Tool | Purpose |
|------|---------|
| [Go 1.22+](https://go.dev/dl/) | Build the server |
| [Python 3.8+](https://www.python.org/downloads/) | Run the embedding sidecar (only if using local models) |
| [jq](https://jqlang.github.io/jq/download/) | Parse JSON in hook script (`brew install jq` on Mac) |

### One-Command Install

```bash
git clone https://github.com/Wrath-y/local-rag
cd local-rag
./start.sh
```

The script automatically:
1. Builds the Go binary
2. Sets up the Python sidecar virtual environment (if using local embedding)
3. Starts the server

When you see:
```
RAG server started (PID: xxxxx) at http://127.0.0.1:8765
```

**Restart Claude Code and you're ready to go.**

> The script is idempotent — safe to run multiple times. If you move the project directory, re-run to update paths.

---

## Usage

All operations are done inside the Claude Code chat — type `/rag` to trigger autocomplete.

### 📥 Ingest Documents

```bash
/rag Your document content here...              # Paste text directly
/rag https://xxx.feishu.cn/docx/xxx             # Feishu document link
/rag /path/to/file.txt                          # Local file path
/rag /path/to/file.txt --source Product_Manual  # Custom source label
```

| Input Type | Auto-inferred Source Label |
|-----------|---------------------------|
| Direct text | `manual` |
| Feishu link | The URL |
| Local file | Filename (e.g., `manual.txt`) |

### Production connectors

`POST /ingest` and MCP `rag_ingest` also accept an explicit `kind` and one
source field: `txt`, `json`, `pdf`, `docx`, `web_url`, or `git`. Use `path`
for local documents/repositories and `url` for a web page or HTTPS/SSH Git
remote. Git accepts optional `ref`, additive `exclusions`, and lower-only
`limits`.

```json
{"kind":"git","path":"/approved/project","ref":"v1.2.0","exclusions":["generated/"]}
```

TXT is normalized as UTF-8 (with BOM/line-ending normalization); JSON is
rendered deterministically; DOCX/PDF must contain extractable text. Web loads
fetch one page only, do not run scripts or crawl, reject credentials and
private destinations, and use bounded redirects/response sizes. Git supports
approved local paths and credential-free HTTPS/SSH remotes only; it disables
submodules and LFS, uses the host Git credential helper/SSH agent, and removes
remote clone workspaces on completion. Source metadata records the redacted
canonical identity, loader, and Git path/ref/revision where applicable.

### 🔍 Retrieve from Knowledge Base

```bash
/rag-retrieve How to handle Redis cache penetration?
```

Retrieved chunks now include request-scoped `[n]` citations. See the
[citation contract](CITATIONS.md) for structured HTTP/MCP fields,
validation, and uncertainty behavior.

### ⚡ Auto-Retrieval Mode

When enabled, every prompt automatically retrieves from the knowledge base and injects results — no manual trigger needed:

```bash
/rag-mode on    # Enable (persisted across restarts)
/rag-mode off   # Disable
```

### 🔄 Update Documents

Re-sync after document changes — one command replaces "delete + re-ingest":

```bash
/rag-update https://xxx.feishu.cn/docx/xxx
/rag-update /path/to/file.txt --source Product_Manual
```

### Incremental Source Sync API

Set `sync.enabled: true` to enable the opt-in asynchronous source-sync API.
Submit a complete snapshot with stable document IDs, then poll the returned
task resource. Unchanged chunks retain their vectors; reports and baseline
responses contain only identifiers and aggregates, never source content.

```bash
curl -X POST http://127.0.0.1:8765/sources/product-manual/syncs \
  -H 'Content-Type: application/json' -H 'Idempotency-Key: refresh-2026-07-23' \
  -d '{"documents":[{"id":"intro","content":"Canonical document text"}]}'
curl http://127.0.0.1:8765/sources/product-manual/syncs/<task-id>
curl http://127.0.0.1:8765/sources/product-manual/syncs/<task-id>/report
```

The equivalent MCP tools are `rag_sync_source`, `rag_get_sync_status`,
`rag_get_sync_report`, `rag_retry_sync`, and `rag_get_sync_baseline`.
Resubmitting an equivalent snapshot with the same key returns the original
task. A failed or cancelled task may be explicitly retried up to
`sync.max_attempts`; a concurrent task for the same source is rejected.

### 📊 Manage Knowledge Base

```bash
/rag-status                        # Service status + chunk count
/rag-sources                       # List all sources with chunk counts
/rag-source-delete <source_name>   # Delete a specific source
/rag-reset                         # Clear entire knowledge base
```

### 💾 Export and Import

Export creates a versioned ZIP package containing a consistent SQLite snapshot (`rag.db`) and `manifest.json`. The manifest records the package/schema versions, creation timestamp, chunk count, embedding summary, file size, and SHA-256 checksum.

```bash
/rag-export ~/rag-backup.zip
/rag-import ~/rag-backup.zip
```

Import requires an explicit confirmation. Before replacement, the server validates ZIP structure, manifest versions, file size/checksum, and SQLite schema; it then snapshots the active database, atomically replaces it, reloads it, and runs an integrity check. Failure during replacement, reload, or integrity validation triggers an automatic rollback.

**Compatibility and limits:** only backups exported by this version are supported. Legacy one-file ZIP exports without `manifest.json` are rejected; re-export the source knowledge base with the current server first. Format v1 accepts exactly `manifest.json` and `rag.db`; archive input is limited to 256 MiB compressed, 512 MiB extracted, and two entries.

HTTP import accepts multipart input with a mandatory confirmation field:

```bash
curl -s -X POST http://127.0.0.1:8765/import \
  -F 'confirm=true' \
  -F 'file=@~/rag-backup.zip'
```

Responses include a `stage` (`validate`, `snapshot`, `replace`, `reload`, `integrity`, or `complete`) and `rolled_back` state. Restore metrics are exposed as `rag_restore_total` and `rag_restore_duration_seconds`; logs never include document text.

### ♻️ Rebuild the Vector Index

Use an index rebuild after changing embedding models or dimensions, or to repair vectors. The rebuild is asynchronous: it snapshots chunk IDs and text, generates vectors in a shadow index, validates the count, IDs, dimensions, and a representative lookup, then transactionally replaces the active vectors. Retrieval continues on the old index until that cutover succeeds.

```bash
curl -s -X POST http://127.0.0.1:8765/index/rebuild
curl -s http://127.0.0.1:8765/index/status
```

`POST /index/rebuild` returns `202 Accepted` with a task ID. A second request while work is running returns `409 Conflict` and the active task. `GET /index/status` reports `normal`, `rebuilding`, `failed`, or `read-only`, plus timestamps, processed/total counts, progress, and a safe error category for failed tasks.

While rebuilding, writes (ingest, source deletion, reset, and import) return `503 Service Unavailable`; retry them after the status is terminal. This deliberate temporary rejection avoids silently losing writes from a snapshot. Failed embedding, validation, cutover, or integrity checks preserve (or restore) the prior active index. The prior index is retained in SQLite for recovery. Prometheus exposes `rag_index_rebuild_total`, `rag_index_rebuild_duration_seconds`, `rag_index_rebuild_progress`, and `rag_index_rebuild_active`; their labels never contain document or query text.

### 🎯 Rerank

Enable cross-encoder reranking for higher relevance precision:

```bash
/rag-rerank on    # Enable
/rag-rerank off   # Disable
```

> First enable downloads `BAAI/bge-reranker-base` model (~400MB), then reuses in-process. Adds ~50-200ms per retrieval, zero extra tokens.

---

## Configuration

Edit `config.yaml` to adjust parameters:

```yaml
server:
  port: 8765

# Embedding provider: "local" (Python sidecar) or "openai" (API)
embedding:
  provider: "local"
  model: "BAAI/bge-small-zh-v1.5"
  dims: 512

# Reranking: "local", "cohere", "jina", or "disabled"
rerank:
  provider: "local"
  model: "BAAI/bge-reranker-base"

# LLM for agentic chunking & query rewrite
llm:
  provider: "anthropic"
  model: "claude-sonnet-4-6"
  api_key_env: "ANTHROPIC_API_KEY"

# Chunking strategy: fixed | structure | semantic | agentic
chunk:
  strategy: "fixed"
  min_tokens: 200
  max_tokens: 400

# Retrieval
retrieve:
  top_k: 3
  candidate_multiplier: 10    # Recall pool = top_k × this
  rerank_candidates: 9        # Sent to reranker = top_k × 3
  score_weights:
    vector: 0.7
    bm25: 0.3

# Storage
storage:
  db_path: "data/rag.db"

# Connector paths are allowlisted; limits are server ceilings.
connectors:
  allowed_local_paths: ["."]
  max_source_bytes: 20971520
  max_documents: 500
  max_duration_seconds: 60
  max_git_files: 2000
```

### Using OpenRouter

OpenRouter uses the OpenAI-compatible Chat Completions API. Set the provider to
`openrouter`; the endpoint is configured automatically:

```yaml
llm:
  provider: "openrouter"
  model: "openai/gpt-4.1-mini"
  api_key_env: "OPENROUTER_API_KEY"
```

Export the API key before starting the server:

```bash
export OPENROUTER_API_KEY="sk-or-v1-..."
```

Use an OpenRouter model slug for `model` (for example,
`anthropic/claude-sonnet-4` or `deepseek/deepseek-chat-v3-0324`).

### Using Third-Party Embedding API

To skip the local Python sidecar entirely:

```yaml
embedding:
  provider: "openai"
  model: "text-embedding-3-small"
  dims: 1536
  api_key_env: "OPENAI_API_KEY"
  base_url: "https://api.openai.com/v1"
```

---

## Architecture

```
┌─────────────────────────────────────┐
│  Go Server (Gin, :8765)            │
│  ┌─────────┐  ┌──────────┐         │
│  │ Chunker │  │ Retriever│         │
│  │ (4 strat)│  │vec+FTS5  │         │
│  └────┬────┘  └────┬─────┘         │
│       │             │               │
│  ┌────▼─────────────▼────┐         │
│  │  Provider Interface    │         │
│  │  Embed / Rerank / LLM │         │
│  └────────────┬──────────┘         │
└───────────────┼─────────────────────┘
                │ HTTP
┌───────────────▼─────────────────────┐
│  Python Sidecar (:8766)             │
│  /embed  /rerank  /health           │
│  (managed by Go, starts on demand)  │
└─────────────────────────────────────┘
```

### Tech Stack

| Layer | Choice |
|-------|--------|
| Language | Go 1.22+ |
| HTTP | Gin |
| Vector Store | SQLite + sqlite-vec (vec0 virtual table) |
| Full-Text Search | SQLite FTS5 (BM25) |
| SQLite Driver | modernc.org/sqlite (pure Go, built-in sqlite-vec) |
| Embedding/Rerank | Unified HTTP interface (local sidecar or third-party API) |
| Observability | Prometheus + slog |

### Hybrid Retrieval Pipeline

```
Query → [Query Rewrite] → Embed
         │
         ├─ Vector KNN (top_k × 10 candidates)
         ├─ BM25 FTS5 (top_k × 10 candidates)
         │
         ├─ Score Fusion: α·vec + (1-α)·bm25
         ├─ Coarse filter → top_k × 3
         ├─ [Rerank] → final top_k
         │
         └─ Return results
```

---

## Chunking Strategies

| Strategy | Description | External Dependency |
|----------|-------------|-------------------|
| `fixed` | Split by sentence boundaries, merge by token count | None |
| `structure` | Markdown-aware: preserve code blocks, tables, lists | None |
| `semantic` | Embed sentences, cut where cosine similarity drops | EmbedProvider |
| `agentic` | LLM determines optimal boundaries | LLMProvider |

Switch at runtime:
```bash
/rag-rewrite on    # Enable query rewriting
```

---

## Command Reference

| Command | Description | Extra Tokens |
|---------|-------------|:------------:|
| `/rag <content> [--source <name>]` | Ingest document | — |
| `/rag-update <link/path> [--source <name>]` | Update source (delete + re-ingest) | — |
| `/rag-retrieve <question>` | Manual retrieve | ✓ small |
| `/rag-mode on/off` | Auto-retrieval mode | ✓ when on |
| `/rag-rerank on/off` | Rerank toggle | — |
| `/rag-rewrite on/off` | Query rewrite toggle | — |
| `/rag-verbose on/off` | Retrieval observability logs | — |
| `/rag-status` | Service status + chunk count | — |
| `/rag-sources` | List all sources | — |
| `/rag-source-delete <name>` | Delete source | — |
| `/rag-reset` | Clear entire knowledge base | — |
| `/rag-export` | Export database as zip backup | — |
| `/rag-import` | Import from zip backup | — |

---

## How It Works

RAG intercepts prompts via **Claude Code Hook mechanism**, injecting retrieval results before the model sees the prompt:

```
User types prompt
    ↓
UserPromptSubmit Hook (hook.sh)
    ├─ rag-mode off → pass through unchanged
    └─ rag-mode on  → POST /hook
                        ↓
                      Retrieve relevant chunks
                        ↓
                      Inject as additionalContext
                        ↓
                      Model sees: [system] + [RAG results] + [user prompt]
```

`additionalContext` is injected at the system prompt level — **visible to the model, invisible to the user** — preserving the conversation structure.

---

## Service Management

```bash
./start.sh    # Build + start (idempotent)
./stop.sh     # Stop server

# Health check
curl http://127.0.0.1:8765/health

# Prometheus metrics
curl http://127.0.0.1:8765/metrics
```

---

## Project Structure

```
local-rag/
├── cmd/server/main.go          # Entry point
├── internal/
│   ├── config/                 # YAML config loader
│   ├── provider/               # Embed/Rerank/LLM interfaces + implementations
│   ├── store/                  # SQLite + vec0 + FTS5
│   ├── chunk/                  # 4 chunking strategies
│   ├── handler/                # All HTTP handlers
│   ├── agent/                  # Agent chat with tool use
│   ├── sidecar/                # Python process manager
│   ├── retrieve/               # Query rewrite
│   └── observe/                # Metrics + logging
├── sidecar/
│   ├── main.py                 # Python embedding/rerank microservice
│   └── requirements.txt
├── config.yaml                 # Configuration
├── start.sh / stop.sh          # Lifecycle scripts
└── .claude/
    ├── hook.sh                 # Claude Code hook
    ├── settings.json           # Hook registration
    └── commands/               # Slash command definitions
```

---

## FAQ

**Q: Do I need Python if I use OpenAI embeddings?**

No. Set `embedding.provider: "openai"` in config.yaml and the Python sidecar won't be started.

**Q: How much disk space does it use?**

The SQLite database is compact. 10,000 chunks with 512-dim vectors ≈ 25MB.

**Q: Can I use a different embedding model?**

Yes. Change `embedding.model` and `embedding.dims` in config.yaml. If using local provider, the model is auto-downloaded on first use.

**Q: What happens if the server crashes?**

SQLite WAL mode ensures data integrity. No data loss on crash.

**Q: How is this different from the Python version?**

The Go rewrite offers:
- ⚡ Faster startup (~100ms vs ~3s)
- 📦 Single binary (no pip install for core)
- 🔒 SQLite replaces pickle+FAISS+WAL — simpler, more reliable
- 🏗️ Cleaner architecture with proper interfaces
