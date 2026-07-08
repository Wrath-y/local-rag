"""
MCP Client Demo — 直连 RAG MCP Server，演示全部 5 个 tool。

用法:
    pip install mcp
    python mcp_client_demo.py

前提: RAG 服务已启动（./start.sh）或 MCP Server 会自动拉起。
"""

import asyncio
import json
import os
import sys

from mcp import ClientSession, StdioServerParameters
from mcp.client.stdio import stdio_client

# MCP Server 路径（自动定位到项目根目录）
_PROJECT_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
_MCP_SERVER = os.path.join(_PROJECT_ROOT, "mcp_server.py")


async def call_tool(session: ClientSession, name: str, arguments: dict | None = None) -> str:
    """调用 MCP tool 并返回文本结果。"""
    result = await session.call_tool(name, arguments or {})
    return result.content[0].text


async def main() -> None:
    server_params = StdioServerParameters(
        command=sys.executable,
        args=[_MCP_SERVER],
        cwd=_PROJECT_ROOT,
    )

    async with stdio_client(server_params) as (read, write):
        async with ClientSession(read, write) as session:
            await session.initialize()

            # ── 1. 查看服务状态 ──
            print("═" * 50)
            print("1. rag_status")
            print("═" * 50)
            status = await call_tool(session, "rag_status")
            print(status)
            print()

            # ── 2. 存入文档 ──
            print("═" * 50)
            print("2. rag_ingest")
            print("═" * 50)
            doc = (
                "Redis 缓存穿透是指查询一个一定不存在的数据，"
                "由于缓存无法命中，每次请求都打到数据库。"
                "解决方案包括：布隆过滤器拦截非法 key、缓存空值并设置短过期时间、"
                "接口层增加参数校验。"
            )
            result = await call_tool(session, "rag_ingest", {
                "text": doc,
                "source": "mcp-demo",
            })
            print(result)
            print()

            # ── 3. 列出来源 ──
            print("═" * 50)
            print("3. rag_list_sources")
            print("═" * 50)
            sources = await call_tool(session, "rag_list_sources")
            print(sources)
            print()

            # ── 4. 语义检索 ──
            print("═" * 50)
            print("4. rag_retrieve")
            print("═" * 50)
            chunks = await call_tool(session, "rag_retrieve", {
                "query": "如何防止缓存穿透？",
                "top_k": 3,
            })
            print(chunks)
            print()

            # ── 5. 清理演示数据 ──
            print("═" * 50)
            print("5. rag_delete_source")
            print("═" * 50)
            deleted = await call_tool(session, "rag_delete_source", {
                "source": "mcp-demo",
            })
            print(deleted)
            print()

            print("✅ 全部 MCP tool 演示完成")


if __name__ == "__main__":
    asyncio.run(main())
