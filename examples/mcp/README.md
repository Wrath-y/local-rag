# Local RAG — MCP 接入指南

本项目的主服务 `rag-server` 内置 MCP (Model Context Protocol) 支持，其他 Agent 可通过 stdio 直接调用 RAG 能力。

## 快速开始

### 1. 编译

```bash
cd /path/to/local-rag
go build -o rag-server ./cmd/server/
```

### 2. 配置 Agent

在 Agent 的 MCP 配置中添加：

**Claude Code** (`.claude/settings.json` 或 `~/.claude/settings.json`)：

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

**Cursor** (`.cursor/mcp.json`)：

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

**其他 MCP 兼容 Agent**：

任何支持 MCP stdio transport 的 Agent 均可接入，命令为 `rag-server mcp`。

### 3. 使用

配置完成后重启 Agent，即可在对话中调用 RAG 工具。

---

## 架构

```
Agent (Claude Code / Cursor / ...)
  ↓ stdio (JSON-RPC)
rag-server mcp
  ↓ 直接调用内部函数（无 HTTP）
SQLite (vec0 + FTS5)
```

MCP 模式直接调用内部服务（store、embedder、chunker），无 HTTP 中间层，延迟最低。

---

## 环境变量

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `RAG_CONFIG` | `config.yaml` | 配置文件路径 |

---

## 可用 Tools

| Tool | 说明 | 参数 |
|------|------|------|
| `rag_ingest` | 存入知识库 | `text` (必填), `source` (可选, 默认 "manual") |
| `rag_retrieve` | 语义+关键词混合检索 | `query` (必填), `top_k` (可选) |
| `rag_list_sources` | 列出所有来源 | 无 |
| `rag_delete_source` | 按来源删除 | `source` (必填) |
| `rag_status` | 服务状态 + chunk 总数 | 无 |

---

## 调用示例

### rag_ingest

```json
{
  "text": "Redis 缓存穿透是指查询一个不存在的 key，请求直接打到数据库...",
  "source": "redis-guide"
}
```

响应：`Ingested 3 chunks from source "redis-guide".`

### rag_retrieve

```json
{
  "query": "缓存穿透怎么处理",
  "top_k": 5
}
```

响应：返回最相关的文档片段，附带来源标识。

### rag_list_sources

```json
{}
```

响应：
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

响应：`Deleted 3 chunks from source "redis-guide".`

### rag_status

```json
{}
```

响应：`RAG Status: OK | Total chunks: 15`

---

## 与 HTTP 模式的关系

| 模式 | 命令 | 用途 |
|------|------|------|
| HTTP（默认） | `./rag-server` | Hook 自动检索、浏览器/脚本访问、多客户端共享 |
| MCP | `./rag-server mcp` | Agent 直接调用，无网络开销 |

两种模式使用相同的配置文件和数据库，可以并行运行（HTTP 守护进程 + 按需启动的 MCP 实例）。

---

## 生命周期

MCP 模式的进程生命周期由 Agent 管理：
- Agent 启动时 spawn `rag-server mcp` 子进程
- Agent 关闭/断开时进程自动退出
- 无需 `start.sh` / `stop.sh`

首次使用前需确保已编译二进制并安装好 Python sidecar（运行一次 `./start.sh` 即可完成）。
