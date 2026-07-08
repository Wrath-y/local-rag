"""
Claude Agent + RAG — 通过 MCP tool_use 实现 RAG 增强对话。

Agent 在每轮对话中可自主决定是否调用 rag_retrieve 获取知识，
实现"需要时检索、检索后回答"的 Agent Loop。

用法:
    pip install anthropic mcp
    export ANTHROPIC_API_KEY=your-key
    python claude_agent_with_rag.py "Redis 缓存穿透怎么处理？"

前提: RAG 服务已启动且知识库中已有文档。
"""

import asyncio
import json
import os
import sys

import anthropic
from mcp import ClientSession, StdioServerParameters
from mcp.client.stdio import stdio_client

_PROJECT_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
_MCP_SERVER = os.path.join(_PROJECT_ROOT, "mcp_server.py")

# Claude 可用的 tool 定义 — 与 MCP Server 注册的 tool 对齐
TOOLS = [
    {
        "name": "rag_retrieve",
        "description": (
            "Semantically retrieve relevant document chunks from the RAG knowledge base. "
            "Use when the user asks a question that might be answered by ingested documents."
        ),
        "input_schema": {
            "type": "object",
            "properties": {
                "query": {"type": "string", "description": "The question or search query"},
                "top_k": {
                    "type": "integer",
                    "description": "Max number of chunks to return (default: 3)",
                },
            },
            "required": ["query"],
        },
    },
    {
        "name": "rag_ingest",
        "description": "Ingest text content into the RAG knowledge base for future retrieval.",
        "input_schema": {
            "type": "object",
            "properties": {
                "text": {"type": "string", "description": "Text content to ingest"},
                "source": {
                    "type": "string",
                    "description": "Source identifier label (default: manual)",
                },
            },
            "required": ["text"],
        },
    },
]

MAX_TURNS = 10  # Agent loop 最大轮次，防止无限循环


async def run_agent(question: str) -> None:
    """运行一个带 RAG 能力的 Agent loop。"""
    client = anthropic.Anthropic()

    server_params = StdioServerParameters(
        command=sys.executable,
        args=[_MCP_SERVER],
        cwd=_PROJECT_ROOT,
    )

    async with stdio_client(server_params) as (read, write):
        async with ClientSession(read, write) as session:
            await session.initialize()

            messages = [{"role": "user", "content": question}]
            print(f"\n🧑 User: {question}\n")

            for turn in range(MAX_TURNS):
                # ── 调用 Claude ──
                response = client.messages.create(
                    model="claude-sonnet-4-6",
                    max_tokens=4096,
                    system=(
                        "你是一个知识助手。当用户提问时，先调用 rag_retrieve 检索知识库，"
                        "基于检索结果回答。如果检索结果不相关，用自身知识回答并说明。"
                    ),
                    tools=TOOLS,
                    messages=messages,
                )

                # ── 处理响应 ──
                assistant_content = response.content
                messages.append({"role": "assistant", "content": assistant_content})

                # 无 tool_use → 最终回答，退出循环
                if response.stop_reason == "end_turn":
                    for block in assistant_content:
                        if hasattr(block, "text"):
                            print(f"🤖 Agent:\n{block.text}")
                    break

                # 有 tool_use → 执行 MCP tool call
                tool_results = []
                for block in assistant_content:
                    if block.type != "tool_use":
                        continue

                    tool_name = block.name
                    tool_input = block.input
                    print(f"  🔧 [{turn + 1}] tool_use: {tool_name}({json.dumps(tool_input, ensure_ascii=False)})")

                    # 通过 MCP session 执行
                    result = await session.call_tool(tool_name, tool_input)
                    result_text = result.content[0].text

                    print(f"  📄 result: {result_text[:200]}{'...' if len(result_text) > 200 else ''}\n")

                    tool_results.append({
                        "type": "tool_result",
                        "tool_use_id": block.id,
                        "content": result_text,
                    })

                messages.append({"role": "user", "content": tool_results})
            else:
                print(f"⚠️  达到最大轮次 ({MAX_TURNS})，Agent 停止。")


def main() -> None:
    if len(sys.argv) < 2:
        print("用法: python claude_agent_with_rag.py \"你的问题\"")
        sys.exit(1)

    question = sys.argv[1]
    asyncio.run(run_agent(question))


if __name__ == "__main__":
    main()
