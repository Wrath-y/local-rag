<div align="center">

# 🧠 Claude Local RAG

**Give Claude Code persistent long-term memory — retrieve knowledge precisely from your own documents**

[![Python](https://img.shields.io/badge/Python-3.8+-3776AB?style=flat-square&logo=python&logoColor=white)](https://www.python.org/)
[![FastAPI](https://img.shields.io/badge/FastAPI-0.100+-009688?style=flat-square&logo=fastapi&logoColor=white)](https://fastapi.tiangolo.com/)
[![FAISS](https://img.shields.io/badge/FAISS-Vector_Search-blue?style=flat-square)](https://github.com/facebookresearch/faiss)
[![License](https://img.shields.io/badge/License-MIT-green?style=flat-square)](LICENSE)
[![Claude Code](https://img.shields.io/badge/Claude_Code-Plugin-orange?style=flat-square)](https://claude.ai/code)
[![MCP](https://img.shields.io/badge/MCP-Supported-blueviolet?style=flat-square)](https://modelcontextprotocol.io/)
[![Agent](https://img.shields.io/badge/Agent-ReAct-orange?style=flat-square)]()

[Installation](#installation) · [Usage](#usage) · [LLM Configuration](#llm-configuration) · [Agent Mode](#agent-mode) · [MCP Integration](#mcp-integration) · [Configuration](#configuration) · [Chunking Strategies](#chunking-strategies) · [Command Reference](#command-reference) · [How It Works](#how-it-works-how-the-prompt-is-modified) · [FAQ](#faq)

📖 [中文文档](README_zh.md)

</div>

---

## Why Do You Need This?

Claude Code has no built-in vector database. Its native memory system is file-based (CLAUDE.md) and **lacks semantic retrieval**.

| Native Limitation | Symptom | This Plugin's Solution |
|-------------------|---------|------------------------|
| Forget on session close | New conversation — Claude knows nothing about last session | Documents stored in a local vector DB, persisted forever, always available |
| Large docs burn tokens | Pasting a 100-page manual into the chat costs a fortune just reading the doc | Only retrieves relevant excerpts; the rest is never transmitted |
| No cross-document semantic search | Claude can't "remember" multiple documents and search across them semantically | All ingested documents are indexed together, returning the most relevant content by meaning |

> 🔒 **All data is stored locally. Nothing is uploaded to any server.**

---

## Installation

### Prerequisites

| Tool | macOS / Linux | Windows |
|------|--------------|---------|
| [Python 3.8+](https://www.python.org/downloads/) | Required | Required |
| [Node.js 16+](https://nodejs.org) (optional) | Feishu doc ingestion | Feishu doc ingestion |
| [curl](https://curl.se) | Built-in | Built-in on Windows 10 1803+ |

### One-Command Install

**macOS / Linux**

```bash
git clone https://github.com/Wrath-y/claude-local-rag
cd claude-local-rag
./start.sh
```

**Windows** (Command Prompt / PowerShell, run as Administrator)

```bat
git clone https://github.com/Wrath-y/claude-local-rag
cd claude-local-rag
start.bat
```

The script automatically installs dependencies, registers Hooks, and starts the service. When you see the following message, the setup is complete:

```
Setup complete! Restart Claude Code to start using it.
```

**Restart Claude Code to start using it.**

> The script is idempotent — safe to re-run. If you move the project directory, re-run the script to update paths.

---

## Usage

All operations are done inside the Claude Code chat box. Type `/rag` to trigger command autocompletion.

### 📥 Ingest Documents

```bash
/rag your document text...                       # Paste text directly
/rag https://xxx.feishu.cn/docx/xxx              # Feishu doc link
/rag https://example.com/docs/api                # Any web URL
/rag /path/to/file.txt                           # Local file (.txt .md .pdf, etc.)
/rag /path/to/file.txt --source product-manual   # Custom source label
```

| Input Type | Auto-inferred Source Label |
|------------|---------------------------|
| Plain text | `manual` |
| Feishu doc link | Link URL |
| Any web URL | Link URL |
| Local file path | Filename (e.g. `manual.txt`) |

> 📌 Retrieval results show `[source: xxx]`. You can also delete by source.

---

### 🔍 Retrieve from Knowledge Base

```bash
/rag-retrieve How do I handle Redis cache penetration?
```

---

### ⚡ Auto-Retrieve Mode

When enabled, every prompt submission automatically queries the knowledge base and injects the results — no manual trigger needed:

```bash
/rag-mode on    # Enable (persistent, survives restarts)
/rag-mode off   # Disable
```

> Driven by a Hook — independent of conversation context. Unaffected by `/clear` or compaction.

---

### 🔄 Update a Document

Re-sync after content changes — one command replaces the two-step "delete + re-ingest" flow:

```bash
/rag-update https://xxx.feishu.cn/docx/xxx
/rag-update /path/to/file.txt --source product-manual
```

---

### 📊 Manage Knowledge Base

```bash
/rag-status                        # Service status, chunk count, retrieval hit rate
/rag-sources                       # List all sources with chunk counts
/rag-source-delete <source-name>   # Delete a source (confirmation prompt)
/rag-reset                         # Clear entire knowledge base (confirmation prompt)
/rag-export ~/backup.zip           # Export knowledge base as zip (for migration)
/rag-import ~/backup.zip           # Import from backup (confirmation prompt, replaces current data)
```

---

### 🎯 Rerank

When enabled, retrieval results are re-ranked by a cross-encoder for higher relevance precision:

```bash
/rag-rerank on    # Enable
/rag-rerank off   # Disable
```

> First enable downloads `BAAI/bge-reranker-base` (~400 MB). Subsequent runs reuse the loaded model in-process. Adds ~50–200 ms per retrieval; consumes no tokens.

---

### 📐 Dynamic top_k

When enabled, the hook estimates how many tokens are already in the conversation transcript before each retrieval. If the remaining context window cannot fit the full `top_k` chunks, the count is automatically reduced (minimum 1):

```bash
/rag-dynamic-top-k on    # Enable
/rag-dynamic-top-k off   # Disable
```

> Disabled by default. Useful in long sessions or when many tool calls have consumed significant context — prevents RAG results from being truncated. In normal short conversations the behavior is identical to the disabled state.

---

### 🔍 Query Rewriting (Advanced)

When enabled, the query is rewritten by an LLM before vectorisation, improving recall for short, vague, or misspelled queries:

```bash
/rag-rewrite on                         # Enable (uses default strategy: expansion)
/rag-rewrite off                        # Disable
/rag-rewrite on --strategy hyde         # Enable with HyDE strategy
/rag-rewrite on --strategy multi_query  # Enable with multi-query strategy
```

| Strategy | How It Works | Best For |
|----------|-------------|----------|
| `expansion` (default) | Expands query with synonyms and related terms | Short or domain-specific queries |
| `hyde` | Generates a hypothetical document, uses its vector | Open-ended questions where query and docs differ in style |
| `multi_query` | Generates 3 query variants, retrieves each, merges results | Ambiguous queries with multiple valid interpretations |

> Disabled by default. Each retrieval adds ~1 LLM call (~100–500 ms). If the LLM call fails, the original query is used transparently — no 500 errors.

---

## LLM Configuration

The project now includes an LLM provider abstraction layer for use by Agent mode and Query Rewriting. Configure in `config.yaml`:

```yaml
llm:
  provider: "anthropic"            # openai | anthropic
  model: "claude-sonnet-4-6"
  api_key_env: "ANTHROPIC_API_KEY" # key is read from this environment variable
  timeout: 30
  max_retries: 2
```

**Switching providers** — change `provider` and `api_key_env`, restart the service:

```yaml
# Switch to OpenAI
llm:
  provider: "openai"
  model: "gpt-4o"
  api_key_env: "OPENAI_API_KEY"
```

**Adding a new provider** — create `llm/your_provider.py` inheriting `LLMProvider`, then register it in `llm/factory.py`:

```python
from .your_provider import YourProvider
register("your-name")(YourProvider)
```

---

## Agent Mode

The project now ships a ReAct Agent that can autonomously decide which RAG tools to invoke based on your message.

### Interactive CLI

```bash
python -m agent.cli                        # Start a new session
python -m agent.cli --session <uuid>       # Resume an existing session
```

Example session:

```
New session: 3f2a...
You: What do our API docs say about authentication?
Agent: [calls rag_retrieve("authentication")]
       According to the API spec, authentication uses Bearer tokens with...

You: Ingest this new auth guide: /docs/auth-v2.md
Agent: [calls rag_ingest(...)]
       Ingestion complete. 8 chunks added from source 'auth-v2.md'.
```

### HTTP API

```bash
# Create a session
curl -X POST http://127.0.0.1:8765/agent/session

# Chat (auto-creates session if omitted)
curl -X POST http://127.0.0.1:8765/agent/chat \
  -H "Content-Type: application/json" \
  -d '{"message": "What is Redis?", "session_id": "<uuid>"}'

# List sessions
curl http://127.0.0.1:8765/agent/sessions

# Delete a session
curl -X DELETE http://127.0.0.1:8765/agent/session/<uuid>
```

Agent conversation history is persisted in SQLite (`agent/agent.db`) — survives service restarts.

---

## MCP Integration

MCP (Model Context Protocol) lets Claude Code and Codex call RAG tools natively, without any slash commands.

### Setup

**1. Start the RAG service** (if not already running):

```bash
./start.sh
```

**2. Register the MCP server** — add to `~/.claude/settings.json`:

```json
{
  "mcpServers": {
    "rag": {
      "command": "python",
      "args": ["/absolute/path/to/rag-plugin/mcp_server.py"]
    }
  }
}
```

> The `start.sh` script prints the exact config snippet with the correct path at the end of installation.

**3. Restart Claude Code** to load the MCP server.

### Available MCP Tools

| Tool | Description |
|------|-------------|
| `rag_retrieve` | Semantic search across the knowledge base |
| `rag_ingest` | Add text to the knowledge base |
| `rag_delete_source` | Remove all chunks from a source |
| `rag_status` | Service health + chunk count |
| `rag_list_sources` | List all sources with chunk counts |

Once registered, Claude Code will automatically call these tools when relevant — no `/rag-retrieve` command needed.

---

## Command Reference

| Command | Description | Extra Tokens |
|---------|-------------|:------------:|
| `/rag <content or link> [--source <name>]` | Ingest document; `--source` overrides auto-inferred label | — |
| `/rag-update <link or path> [--source <name>]` | Update source (delete old + re-ingest); `--source` must match original label | — |
| `/rag-retrieve <question>` | Manual retrieval | ✓ Small |
| `/rag-mode on/off` | Auto-retrieve mode | ✓ When on |
| `/rag-rerank on/off` | Cross-encoder rerank | — |
| `/rag-rewrite on/off [--strategy <name>]` | Query rewriting; strategy: `expansion`\|`hyde`\|`multi_query` | — |
| `/rag-dynamic-top-k on/off` | Dynamic top_k (auto-reduce chunk count when context is nearly full) | — |
| `/rag-verbose on/off` | Retrieval observability logs | — |
| `/rag-status` | Service status + chunk count + hit rate | — |
| `/rag-sources` | All sources with chunk counts | — |
| `/rag-source-delete <name>` | Delete by source (exact name match required) | — |
| `/rag-reset` | Clear entire knowledge base | — |
| `/rag-export [path]` | Export as zip backup (default: `~/rag_backup.zip`) | — |
| `/rag-import <zip-path>` | Import from zip backup, replacing current data (confirmation step) | — |

---

## How It Works: How the Prompt Is Modified

RAG intercepts the prompt via the **Claude Code Hook mechanism**, injecting retrieval results before it is sent to the model:

```
User submits prompt
    ↓
UserPromptSubmit Hook (hook_script.py)
    ├─ rag-mode off → pass through unchanged
    └─ rag-mode on  → POST /retrieve
                        ↓
                      outputs additionalContext
                        ↓
                      injected into system prompt area
                        ↓
                      model sees: [system prompt] + [RAG results] + [user prompt]
```

`additionalContext` is injected at the system prompt layer — **visible to the model, not shown to the user** — without altering the conversation structure.

### Retrieval Pipeline

```
User question
    ↓
① FAISS vector search (top_k × 3 candidates, cosine similarity)
    ↓
② Threshold filter (discard similarity < 0.45)
    ↓
③ BM25 hybrid scoring (final = vec × 0.7 + bm25 × 0.3) → take top_k
    ↓
④ Cross-Encoder Rerank (optional, re-orders top_k results)
    ↓
Final chunks injected into system prompt
```

### Ingestion Pipeline

```
Raw text (pasted / file / URL / Feishu doc)
    ↓
Chunk splitting (strategy-based: fixed / structure / semantic / agentic)
    ↓
Embedding cache hit? → Yes: reuse vector; No: encode with BGE model
    ↓
FAISS IndexFlatIP write + BM25 index rebuild
    ↓
Persist (index.bin + chunks.pkl)
```

> **Key optimization**: The embedding cache is automatically restored from FAISS vectors at startup — no re-encoding needed after restart. When a source is deleted and the index is rebuilt, all remaining chunks hit the cache. The cache is pruned on deletion to prevent unbounded growth; `/rag-update` never causes cache bloat.

### Retrieval Observability

```bash
/rag-verbose on    # Enable detailed logging
tail -f /tmp/claude-local-rag.log
```

```
[retrieve] query: 'GET /api/v2/orders returns 403, help me debug'
[retrieve] FAISS returned 9 candidates (total: 137)
[retrieve] threshold filter (< 0.45) discarded 6, remaining 3
  vec=0.774 bm25=0.600 final=0.722 [api-spec.md] '/api/v2/orders requires scope: orders:read...'
  vec=0.691 bm25=0.400 final=0.604 [auth-guide.md] 'Bearer Token missing permission returns 403...'
  vec=0.652 bm25=0.200 final=0.516 [changelog.md] 'v2.3.0 added IP allowlist check'
[retrieve] after rerank:
  rerank=0.912 'Bearer Token missing permission returns 403...'
  rerank=0.743 '/api/v2/orders requires scope: orders:read...'
[retrieve] returning 3 chunks
```

Each candidate shows vector similarity (`vec`), BM25 keyword score (`bm25`), hybrid score (`final`), source, and a text preview.

### Real-World Example

A team stores internal API docs, interface specs, and release checklists in the vector DB, then enables `rag-mode on`.

User types:
```
GET /api/v2/orders returns 403, help me debug
```

What Claude actually receives (invisible to the user):
```
[RAG auto-retrieval results]
[source: api-spec.md]
/api/v2/orders requires scope: orders:read. The caller must declare this scope when requesting a token...
---
[source: auth-guide.md]
When a Bearer Token lacks permission, a 403 is returned. Check the token's scope list...
---
[source: changelog.md]
v2.3.0 added an IP allowlist check to /orders. Non-allowlisted IPs also return 403...
```

**Result:** Claude pinpoints scope misconfiguration and IP allowlist as the two suspects immediately, without guessing from scratch.

---

## Service Management

**macOS / Linux**

```bash
./start.sh                          # Install dependencies + start service
./stop.sh                           # Stop service
tail -f /tmp/claude-local-rag.log   # View logs
```

**Windows**

```bat
start.bat                                    # Install dependencies + start service
stop.bat                                     # Stop service
type %TEMP%\claude-local-rag.log             # View logs
```

---

## Configuration

Edit `config.yaml` to tune parameters:

| Parameter | Default | Description |
|-----------|---------|-------------|
| `chunk.strategy` | `fixed` | Chunking strategy: `fixed` / `structure` / `semantic` / `agentic` — see [Chunking Strategies](#chunking-strategies) |
| `chunk.min_tokens` | `200` | Minimum tokens per chunk |
| `chunk.max_tokens` | `400` | Maximum tokens per chunk |
| `chunk.structure_aware` | `true` | Markdown structure-aware splitting (only effective when `strategy=fixed`, kept for back-compat) |
| `chunk.hierarchical.enabled` | `false` | Enable Parent-Child hierarchical chunks (orthogonal to `strategy`) |
| `chunk.context_prefix.enabled` | `false` | Prepend heading breadcrumb to each chunk (orthogonal to `strategy`) |
| `retrieve.top_k` | `3` | Number of chunks returned per retrieval |
| `log.lang` | `en` | Log language: `zh` (Chinese) or `en` (English) |
| `retrieve.verbose` | `true` | Enable retrieval logs |
| `retrieve.dynamic_top_k` | `false` | Enable dynamic top_k |
| `retrieve.context_window` | `180000` | Model context window size (tokens) |
| `retrieve.response_reserve` | `8000` | Tokens reserved for model response |
| `rerank.enabled` | `false` | Enable rerank by default |
| `rerank.model` | `BAAI/bge-reranker-base` | Rerank model |
| `model.name` | `BAAI/bge-small-zh-v1.5` | Embedding Model |
| `embedding.doc_prefix` | `段落：` | Prefix prepended to text at ingestion time (BGE-specific) |
| `embedding.query_prefix` | `查询：` | Prefix prepended to query at retrieval time (BGE-specific) |

### Switching the Embedding Model

`model.name` accepts any `sentence-transformers`-compatible model. Edit `config.yaml` and restart the service to apply. **You must run `/rag-reset` before switching models** — different models produce incompatible vector spaces, so searching a new model's index with old vectors will return garbage results.

`doc_prefix` / `query_prefix` are BGE-specific prefixes. When switching to a non-BGE model, clear both:

```yaml
embedding:
  doc_prefix: ""
  query_prefix: ""
```

Common alternatives:

| Model | Dim | Language | Notes |
|-------|-----|----------|-------|
| `BAAI/bge-small-zh-v1.5` (default) | 512 | Chinese | Small, fast |
| `BAAI/bge-base-zh-v1.5` | 768 | Chinese | Higher accuracy, larger |
| `BAAI/bge-small-en-v1.5` | 512 | English | Best for English docs |
| `BAAI/bge-m3` | 1024 | Multilingual | Best for mixed Chinese/English, slower |
| `sentence-transformers/all-MiniLM-L6-v2` | 384 | English | Generic English; clear both prefixes |

### Chunking Strategies

The project ships four chunking strategies. Switch via `chunk.strategy` in `config.yaml`, or at runtime through the HTTP API / Agent tools — only newly ingested documents are affected, already-stored chunks are not re-processed.

| Strategy | How It Works | Best For |
|----------|--------------|----------|
| `fixed` (default) | Sentence-boundary splitting, 200–400 tokens per chunk, 2-sentence overlap | General purpose, fastest path, no LLM cost |
| `structure` | Markdown structure-aware: tables / fenced code blocks / lists stay atomic, splits prefer heading boundaries | Technical docs, API references, structured Markdown |
| `semantic` | Embeds every sentence and splits at points where adjacent similarity drops below a configurable percentile | Long-form prose with topic shifts |
| `agentic` | An LLM analyses the document and decides chunk boundaries; can prepend a one-line summary per chunk | High-value documents where retrieval precision outweighs ingestion cost |

Two orthogonal enhancements can be layered on top of any strategy:

- **Hierarchical (Parent-Child)** — child chunks are used for retrieval; on hit, the larger parent chunk is returned to the LLM, keeping recall precision while expanding context.
- **Context Prefix (breadcrumb)** — propagates the heading path (e.g. `[Guide > Auth > Bearer]`) as a prefix on every chunk, so an isolated chunk still carries its hierarchical position.

Full configuration with defaults:

```yaml
chunk:
  strategy: "fixed"             # fixed | structure | semantic | agentic (default: fixed)
  min_tokens: 200               # default: 200
  max_tokens: 400               # default: 400
  structure_aware: true         # default: true; only applies when strategy=fixed (back-compat)
  hierarchical:
    enabled: false              # default: false
    parent_max_tokens: 800      # default: 800
  context_prefix:
    enabled: false              # default: false
    format: "breadcrumb"        # default: "breadcrumb" (only value supported today)
    max_depth: 3                # default: 3
  semantic:
    threshold_percentile: 90    # default: 90; lower → more, smaller chunks
    min_chunk_size: 2           # default: 2 sentences
    max_chunk_size: 20          # default: 20 sentences
  agentic:
    enabled: false              # hint flag; actual switch is `strategy: agentic`
    generate_summary: true      # default: true; prepend an LLM-generated one-line summary
    max_llm_input_tokens: 4000  # default: 4000; long docs are processed in segments
```

**Runtime switching (no restart required)**

```bash
# Query the current strategy
curl http://127.0.0.1:8765/config/chunk-strategy

# Switch strategy at runtime
curl -X PUT http://127.0.0.1:8765/config/chunk-strategy \
  -H "Content-Type: application/json" \
  -d '{"strategy": "structure"}'
```

| Endpoint | Description |
|----------|-------------|
| `GET /config/chunk-strategy` | Return the current strategy and its parameters |
| `PUT /config/chunk-strategy` | Switch strategy at runtime (`fixed` / `structure` / `semantic` / `agentic`) |

**Agent tools**

In Agent mode the same operations are exposed as natural-language tools:

| Tool | Description |
|------|-------------|
| `set_chunk_strategy` | Switch the active chunking strategy (`fixed` / `structure` / `semantic` / `agentic`) |
| `get_chunk_strategy` | Report the current strategy and its parameters |

Example: saying *"switch to semantic chunking"* lets the agent pick `set_chunk_strategy` and apply the change.

> Switching strategies only affects subsequent ingestions. To re-chunk existing documents under a new strategy, run `/rag-update` per source (or `/rag-reset` followed by re-ingestion).

### Why Is `top_k` 3 by Default?

`top_k = 3` balances **recall** against **token cost**:

- **Token budget**: Each chunk is ~200–400 tokens; 3 chunks total ~600–1200 tokens — retrieval results don't dominate the context
- **Quality floor**: Three-layer filtering (vector → hybrid scoring → rerank) means 3 high-quality results outperform 10 mixed-quality candidates
- **"Lost in the Middle"**: LLMs are known to pay less attention to content in the middle of long contexts — more chunks can actually hurt accuracy

| Scenario | Suggested Value |
|----------|----------------|
| Default / general | `3` |
| Broad topic, multi-document synthesis | `5` |
| Strict token budget | `1–2` |
| Not recommended above | `8` |

---

## Project Structure

```
claude-local-rag/
├── server.py                   # FastAPI backend service
├── mcp_server.py               # MCP stdio server (Claude Code / Codex integration)
├── mcp_tools.py                # MCP tool implementations
├── query_rewrite.py            # Query rewriting strategies (expansion / HyDE / multi_query)
├── markdown_chunker.py         # Structure-aware Markdown chunker
├── semantic_chunker.py         # Embedding-similarity based semantic chunker
├── agentic_chunker.py          # LLM-driven intelligent chunker
├── config.yaml                 # Configuration file
├── requirements.txt            # Python dependencies
├── setup_hook.py               # Cross-platform Hook registration (called by start.sh / start.bat)
├── start.sh                    # One-command install script (macOS / Linux)
├── stop.sh                     # Stop service script (macOS / Linux)
├── start.bat                   # One-command install script (Windows)
├── stop.bat                    # Stop service script (Windows)
├── index.bin                   # Vector index (auto-generated)
├── chunks.pkl                  # Document store (auto-generated)
├── llm/                        # LLM provider abstraction
│   ├── base.py                 # LLMProvider abstract base class
│   ├── openai_provider.py      # OpenAI implementation
│   ├── anthropic_provider.py   # Anthropic implementation
│   └── factory.py              # get_provider() factory + registry
├── agent/                      # ReAct Agent
│   ├── db.py                   # SQLite connection + schema
│   ├── memory.py               # Session + message persistence
│   ├── tools.py                # RAG tool definitions
│   ├── loop.py                 # ReAct reasoning loop
│   └── cli.py                  # Interactive CLI entry point
└── .claude/
    ├── settings.json           # Hook configuration
    ├── hook_script.py          # UserPromptSubmit Hook
    └── commands/               # Slash command definitions
        ├── rag.md
        ├── rag-retrieve.md
        ├── rag-mode.md
        ├── rag-rewrite.md
        └── ...
```

---

## Roadmap

> Checked items are implemented. Open items are planned improvements. Contributions via Issue or PR are welcome.

**Retrieval Quality**

- [x] Vector semantic search (FAISS + BGE Embedding)
- [x] BM25 hybrid scoring (vec × 0.7 + bm25 × 0.3) — improves long-tail keyword recall
- [x] Cross-Encoder Rerank
- [x] Query Rewriting (expansion / HyDE / multi-query strategies)
- [ ] Chunk head/tail overlap — prevent semantic truncation at boundaries
- [x] Pluggable chunking strategies (`fixed` / `structure` / `semantic` / `agentic`) with runtime switching
- [x] Hierarchical (Parent-Child) chunks and breadcrumb context prefix as orthogonal enhancements
- [x] Dynamic top_k (auto-adjust based on remaining context window)

**Knowledge Base Management**

- [x] Source-based management (ingest / update / delete)
- [x] Feishu docs, local files, plain text ingestion
- [x] Embedding cache (skip re-encoding identical content, speeds up repeated ingestion)
- [ ] Scheduled re-indexing (watch file changes, auto-trigger `/rag-update`)
- [x] Export / Import (backup `index.bin` + `chunks.pkl` and migrate)

**Document Format Support**

- [x] Plain text / Markdown
- [x] Feishu cloud docs
- [x] PDF parsing
- [ ] Word / Excel parsing
- [x] Web URL scraping (non-Feishu)

**Observability & Tuning**

- [x] Retrieval observability logs (vec / bm25 / final scores per candidate)
- [x] `/rag-verbose on/off` toggle
- [ ] Web management UI (visualize chunks, test retrieval)
- [x] Retrieval hit rate statistics

---

## FAQ

<details>
<summary><b>Q: The /rag command doesn't show autocomplete?</b></summary>

Restart Claude Code and make sure you have run `./start.sh` (or `start.bat` on Windows).
</details>

<details>
<summary><b>Q: "Service not running" error?</b></summary>

Run `./start.sh` to restart the service, or check the logs:

```bash
tail -f /tmp/claude-local-rag.log
```
</details>

<details>
<summary><b>Q: Feishu documents can't be read?</b></summary>

You need to install and configure lark-cli first. See the [official docs](https://www.feishu.cn/content/article/7623291503305083853).
</details>

<details>
<summary><b>Q: Retrieval results are inaccurate?</b></summary>

- Store paragraphs with clear structure and complete semantics
- Avoid ingesting table screenshots or scanned text
- For larger knowledge bases, enable `/rag-rerank on` for better precision
- Use `/rag-verbose on` and inspect the logs to analyze specific hit behavior
</details>

<details>
<summary><b>Q: Does RAG still work after /clear?</b></summary>

Yes. `/clear` only clears the conversation context — it does not affect the vector store, persistent flag files (`rag_mode`, etc.), or the background service.
</details>

<details>
<summary><b>Q: Will /rag re-embed a file I've already ingested?</b></summary>

No. Each source is fingerprinted with an MD5 hash on ingest. If you run `/rag` on the same file again without changing its content, the service detects the match and skips immediately — zero embedding calls.

Only `/rag-update` forces a full rebuild, because it is an explicit "replace" operation regardless of content.

**Why not skip at the chunk level** (reuse vectors for unchanged paragraphs, re-embed only modified ones)? The added complexity isn't worth it here: this project uses a local BGE model where re-embedding is free and takes milliseconds. Chunk-level diffing only pays off when embedding is billed per token (e.g. a paid API). If you switch to one, that would be the right time to add it.
</details>

<details>
<summary><b>Q: How does Query Rewriting work?</b></summary>

Enable it with `/rag-rewrite on`. Before vectorising your query, an LLM rewrites it using one of three strategies:

- **expansion** — adds synonyms and related terms to the query
- **hyde** — generates a hypothetical document and uses its embedding (great for open-ended questions)
- **multi_query** — generates 3 query variants, retrieves results for each, and merges/deduplicates

If the LLM call fails for any reason, the original query is used — no errors, no interruption.
</details>
