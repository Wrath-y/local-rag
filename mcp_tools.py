"""Tool implementations for the MCP server — each function maps to one MCP tool."""

import httpx

_BASE = "http://127.0.0.1:8765"
_TIMEOUT = 10.0


async def rag_retrieve(query: str, top_k: int | None = None) -> str:
    payload: dict = {"text": query}
    if top_k is not None:
        payload["top_k"] = top_k
    async with httpx.AsyncClient(timeout=_TIMEOUT) as c:
        r = await c.post(f"{_BASE}/retrieve", json=payload)
        r.raise_for_status()
        chunks = r.json().get("chunks", [])
    return "\n---\n".join(chunks) if chunks else "No relevant results found."


async def rag_ingest(text: str, source: str = "manual") -> str:
    async with httpx.AsyncClient(timeout=30.0) as c:
        r = await c.post(f"{_BASE}/ingest", json={"text": text, "source": source})
        r.raise_for_status()
    return r.json().get("message", "Ingestion complete.")


async def rag_delete_source(source: str) -> str:
    async with httpx.AsyncClient(timeout=_TIMEOUT) as c:
        r = await c.delete(f"{_BASE}/source", params={"source": source})
        r.raise_for_status()
    return r.json().get("message", f"Source '{source}' deleted.")


async def rag_status() -> str:
    async with httpx.AsyncClient(timeout=5.0) as c:
        health = (await c.get(f"{_BASE}/health")).json()
        stats = (await c.get(f"{_BASE}/stats")).json()
    return (
        f"Status: {health.get('status', 'unknown')} | "
        f"Chunks: {stats.get('total_chunks', 0)}"
    )


async def rag_list_sources() -> str:
    async with httpx.AsyncClient(timeout=_TIMEOUT) as c:
        r = await c.get(f"{_BASE}/sources")
        r.raise_for_status()
        sources = r.json().get("sources", [])
    if not sources:
        return "No sources ingested yet."
    return "\n".join(f"- {s['source']}: {s['count']} chunks" for s in sources)
