"""MCP server exposing RAG tools over stdio JSON-RPC.

Register in ~/.claude/settings.json:
{
  "mcpServers": {
    "rag": {
      "command": "python",
      "args": ["/absolute/path/to/rag-plugin/mcp_server.py"]
    }
  }
}
"""

import asyncio
import os
import subprocess
import sys

import httpx
from mcp.server import Server
from mcp.server.stdio import stdio_server
from mcp import types

from mcp_tools import (
    rag_retrieve,
    rag_ingest,
    rag_delete_source,
    rag_status,
    rag_list_sources,
)

_BASE = "http://127.0.0.1:8765"
_SERVER_STARTUP_TIMEOUT = 10  # seconds


async def _ensure_fastapi_running() -> None:
    """Check if FastAPI is up; if not, launch it and wait."""
    try:
        async with httpx.AsyncClient(timeout=2.0) as c:
            await c.get(f"{_BASE}/health")
        return
    except Exception:
        pass

    _dir = os.path.dirname(os.path.abspath(__file__))
    subprocess.Popen(
        [sys.executable, "server.py"],
        cwd=_dir,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )

    for _ in range(_SERVER_STARTUP_TIMEOUT * 2):
        await asyncio.sleep(0.5)
        try:
            async with httpx.AsyncClient(timeout=1.0) as c:
                await c.get(f"{_BASE}/health")
            return
        except Exception:
            continue

    raise RuntimeError(
        f"RAG server failed to start within {_SERVER_STARTUP_TIMEOUT}s. "
        "Check /tmp/local-rag.log for details."
    )


server = Server("rag")


@server.list_tools()
async def list_tools() -> list[types.Tool]:
    return [
        types.Tool(
            name="rag_retrieve",
            description=(
                "Semantically retrieve relevant document chunks from the RAG knowledge base. "
                "Use when the user asks a question that might be answered by ingested documents."
            ),
            inputSchema={
                "type": "object",
                "properties": {
                    "query": {"type": "string", "description": "The question or search query"},
                    "top_k": {
                        "type": "integer",
                        "description": "Max number of chunks to return (optional)",
                    },
                },
                "required": ["query"],
            },
        ),
        types.Tool(
            name="rag_ingest",
            description="Ingest text content into the RAG knowledge base for future retrieval.",
            inputSchema={
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
        ),
        types.Tool(
            name="rag_delete_source",
            description="Delete all chunks belonging to a specific source from the knowledge base.",
            inputSchema={
                "type": "object",
                "properties": {
                    "source": {
                        "type": "string",
                        "description": "Source identifier to delete",
                    }
                },
                "required": ["source"],
            },
        ),
        types.Tool(
            name="rag_status",
            description="Get RAG service health status and total chunk count.",
            inputSchema={"type": "object", "properties": {}},
        ),
        types.Tool(
            name="rag_list_sources",
            description="List all ingested sources and their chunk counts.",
            inputSchema={"type": "object", "properties": {}},
        ),
    ]


@server.call_tool()
async def call_tool(name: str, arguments: dict) -> list[types.TextContent]:
    await _ensure_fastapi_running()

    dispatch = {
        "rag_retrieve": lambda: rag_retrieve(**arguments),
        "rag_ingest": lambda: rag_ingest(**arguments),
        "rag_delete_source": lambda: rag_delete_source(**arguments),
        "rag_status": lambda: rag_status(),
        "rag_list_sources": lambda: rag_list_sources(),
    }

    if name not in dispatch:
        raise ValueError(f"Unknown tool: {name!r}")

    result = await dispatch[name]()
    return [types.TextContent(type="text", text=result)]


async def main() -> None:
    async with stdio_server() as (read_stream, write_stream):
        await server.run(
            read_stream,
            write_stream,
            server.create_initialization_options(),
        )


if __name__ == "__main__":
    asyncio.run(main())
