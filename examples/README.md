# Examples: Agent 通过 MCP 接入 Local RAG

本目录展示如何让任意 Agent 应用通过 **MCP（Model Context Protocol）** 接入本项目的向量知识库。

## 前提

```bash
# 1. 安装并启动 RAG 服务
cd /path/to/local-rag
./start.sh

# 2. 确认服务运行中
curl http://127.0.0.1:8765/health
```

## MCP Server 注册

将以下配置添加到 Agent 的 MCP 配置中（以 Claude Code 为例，编辑 `~/.claude/settings.json`）：

```json
{
  "mcpServers": {
    "rag": {
      "command": "python3",
      "args": ["/absolute/path/to/local-rag/mcp_server.py"]
    }
  }
}
```

> MCP Server 启动时会自动检测并拉起 FastAPI 后台服务，无需手动管理。

## 可用 MCP Tools

| Tool | 参数 | 说明 |
|------|------|------|
| `rag_retrieve` | `query` (string, required), `top_k` (int, optional) | 语义检索知识库 |
| `rag_ingest` | `text` (string, required), `source` (string, optional) | 存入文档 |
| `rag_delete_source` | `source` (string, required) | 按来源删除 |
| `rag_status` | 无 | 服务状态 + chunk 总数 |
| `rag_list_sources` | 无 | 列出所有来源 |

## 示例列表

| 文件 | 说明 |
|------|------|
| [`mcp_client_demo.py`](mcp_client_demo.py) | 用 MCP Python SDK 直连 MCP Server，演示全部 5 个 tool |
| [`claude_agent_with_rag.py`](claude_agent_with_rag.py) | 基于 Anthropic SDK 构建 Agent，通过 MCP tool_use 实现 RAG 增强对话 |

## 运行

```bash
cd examples

# 示例 1：MCP Client 直连
pip install mcp
python mcp_client_demo.py

# 示例 2：Claude Agent + RAG
pip install anthropic mcp
export ANTHROPIC_API_KEY=your-key
python claude_agent_with_rag.py "Redis 缓存穿透怎么处理？"
```
