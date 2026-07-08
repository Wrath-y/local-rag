# Local RAG — MCP Server Example

This example shows how other AI agents can use the Local RAG knowledge base via [MCP (Model Context Protocol)](https://modelcontextprotocol.io/).

> **Note:** The main `rag-server` binary has **built-in MCP support** via `./rag-server mcp`.
> This example (`examples/mcp/`) is a standalone alternative that connects to the HTTP API
> instead — useful when the RAG service is already running as a daemon.

## Two ways to use MCP

### Option 1: Built-in (recommended)

The main binary directly serves MCP over stdio — no HTTP in the middle:

```json
{
  "mcpServers": {
    "local-rag": {
      "command": "/path/to/local-rag/rag-server",
      "args": ["mcp"]
    }
  }
}
```

### Option 2: Standalone proxy (this example)

A separate lightweight binary that proxies MCP calls to the running HTTP service:

```json
{
  "mcpServers": {
    "local-rag": {
      "command": "/path/to/local-rag/examples/mcp/rag-mcp-server"
    }
  }
}
```

## Architecture Comparison

```
Option 1 (built-in):
Agent → stdio → rag-server mcp → [internal function calls] → SQLite

Option 2 (proxy):
Agent → stdio → rag-mcp-server → HTTP → rag-server → SQLite
```

Option 1 is faster (no network hop) and simpler (single binary).
Option 2 is useful when the RAG service is shared across multiple clients.

## Build (this example)

## Build

```bash
cd /path/to/local-rag
go build -o examples/mcp/rag-mcp-server ./examples/mcp/
```

## Prerequisites

The RAG service must be running:

```bash
./start.sh
```

## Configure in Claude Code

Add to your project's `.claude/settings.json` (or `~/.claude/settings.json` for global):

```json
{
  "mcpServers": {
    "local-rag": {
      "command": "/absolute/path/to/local-rag/examples/mcp/rag-mcp-server"
    }
  }
}
```

## Configure in Cursor

Add to `.cursor/mcp.json`:

```json
{
  "mcpServers": {
    "local-rag": {
      "command": "/absolute/path/to/local-rag/examples/mcp/rag-mcp-server"
    }
  }
}
```

## Configure in Other Agents

Any MCP-compatible agent can connect. The server uses stdio transport (standard JSON-RPC over stdin/stdout).

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `RAG_BASE_URL` | `http://127.0.0.1:8765` | RAG service base URL |

## Available Tools

| Tool | Description |
|------|-------------|
| `rag_ingest` | Ingest text into the knowledge base |
| `rag_retrieve` | Semantic + keyword hybrid search |
| `rag_list_sources` | List all sources and chunk counts |
| `rag_delete_source` | Delete chunks by source |
| `rag_status` | Service health and stats |

## Tool Usage Examples

### rag_ingest

```json
{
  "text": "Redis cache penetration occurs when...",
  "source": "redis-guide"
}
```

### rag_retrieve

```json
{
  "query": "How to handle cache penetration?",
  "top_k": 5
}
```

### rag_list_sources

```json
{}
```

### rag_delete_source

```json
{
  "source": "redis-guide"
}
```

### rag_status

```json
{}
```

## How It Works

1. Agent sends an MCP `tools/call` request via stdio
2. This server parses the request, calls the RAG HTTP API
3. Response is formatted as MCP `TextContent` and returned to the agent
4. If the RAG service is unavailable, the tool returns an error message (doesn't crash)

## Development

Run directly (for testing):

```bash
go run ./examples/mcp/
```

The server will wait for MCP JSON-RPC messages on stdin. You can test with the MCP inspector or any MCP client.
