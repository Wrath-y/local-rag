# Local RAG — MCP Integration Guide

[中文版](./README_zh.md)

The main `rag-server` binary has built-in MCP (Model Context Protocol) support. Other agents can call RAG tools directly over stdio.

## Quick Start

### 1. Build

```bash
cd /path/to/local-rag
go build -o rag-server ./cmd/server/
```

### 2. Configure Your Agent

**Claude Code** (`.claude/settings.json` or `~/.claude/settings.json`):

```json
{
  "mcpServers": {
    "local-rag": {
      "command": "/absolute/path/to/local-rag/rag-server",
      "args": ["mcp"]
    }
  }
}
```

**Cursor** (`.cursor/mcp.json`):

```json
{
  "mcpServers": {
    "local-rag": {
      "command": "/absolute/path/to/local-rag/rag-server",
      "args": ["mcp"]
    }
  }
}
```

**Other MCP-compatible agents:**

Any agent supporting MCP stdio transport can connect. The command is `rag-server mcp`.

### 3. Use

Restart your agent after configuration. RAG tools will be available in the conversation.

---

## Architecture

```
Agent (Claude Code / Cursor / ...)
  ↓ stdio (JSON-RPC)
rag-server mcp
  ↓ direct internal function calls (no HTTP)
SQLite (vec0 + FTS5)
```

MCP mode calls internal services (store, embedder, chunker) directly — no HTTP middleman, minimal latency.

---

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `RAG_CONFIG` | `config.yaml` | Path to configuration file |

---

## Available Tools

| Tool | Description | Parameters |
|------|-------------|------------|
| `rag_ingest` | Ingest text into the knowledge base | `text` (required), `source` (optional, default "manual") |
| `rag_retrieve` | Hybrid vector + keyword search | `query` (required), `top_k` (optional) |
| `rag_list_sources` | List all sources with chunk counts | none |
| `rag_delete_source` | Delete all chunks from a source | `source` (required) |
| `rag_status` | Service status and total chunk count | none |
| `rag_update_source` | Safely replace a source asynchronously | `source`, `content`, `confirm: true` |
| `rag_reset` | Clear the knowledge base asynchronously | `confirm: true` |
| `rag_export` | Create a local export archive | optional absolute local `.zip` `path` |
| `rag_import` | Import a local export asynchronously | absolute local `path`, `confirm: true` |
| `rag_backup_run` / `rag_backup_list` | Create or list local backups | none |
| `rag_backup_restore` | Restore a local backup asynchronously | absolute local `path`, `confirm: true` |
| `rag_storage_integrity_check` | Run SQLite integrity check | none |
| `rag_index_rebuild` / `rag_index_status` | Rebuild embeddings or inspect index availability | none |
| `rag_retrieval_config_get` / `rag_retrieval_config_set` | Inspect or atomically patch runtime retrieval settings | partial supported settings for `set` |
| `rag_task_status` | Poll an asynchronous management operation | `task_id` |

---

## Usage Examples

### rag_ingest

```json
{
  "text": "Redis cache penetration occurs when queries for non-existent keys bypass the cache and hit the database directly...",
  "source": "redis-guide"
}
```

Response: `Ingested 3 chunks from source "redis-guide".`

### rag_retrieve

```json
{
  "query": "how to handle cache penetration",
  "top_k": 5
}
```

Response: Returns the most relevant document chunks with source labels.

### rag_list_sources

```json
{}
```

Response:
```
- redis-guide: 3 chunks
- api-spec: 12 chunks
```

### rag_delete_source

```json
{
  "source": "redis-guide"
}
```

Response: `Deleted 3 chunks from source "redis-guide".`

### rag_status

```json
{}
```

Response: `RAG Status: OK | Total chunks: 15`

### Destructive management and task polling

Destructive calls require the literal JSON boolean `true`; omission, strings,
and `false` are rejected before work is queued. Import/export paths are local,
absolute filesystem paths. Archive bytes are not sent through MCP.

```json
{"source":"redis-guide","content":"replacement content","confirm":true}
```

The result includes a `task_id` and `status: "queued"`. Poll it until it is
terminal:

```json
{"task_id":"opaque-task-id"}
```

`rag_task_status` reports `queued`, `running`, `succeeded`, or `failed`, plus
safe error/result metadata. Task records are bounded, in-memory, and disappear
when the MCP process restarts.

To export locally:

```json
{"path":"/absolute/path/rag-export.zip"}
```

---

## Relationship to HTTP Mode

| Mode | Command | Use Case |
|------|---------|----------|
| HTTP (default) | `./rag-server` | Hook-based auto-retrieval, browser/script access, multi-client sharing |
| MCP | `./rag-server mcp` | Direct agent integration, zero network overhead |

Both modes use the same config file and database. They can run in parallel (HTTP daemon + on-demand MCP instances).

---

## Lifecycle

MCP mode process lifecycle is managed by the calling agent:
- Agent spawns `rag-server mcp` as a child process on startup
- Process exits automatically when the agent disconnects
- No `start.sh` / `stop.sh` needed

Prerequisite: ensure the binary is compiled and Python sidecar dependencies are installed (run `./start.sh` once to set up everything).
