import httpx

_BASE = "http://127.0.0.1:8765"
_TIMEOUT = 10.0

# Tool schemas (OpenAI function-calling format, accepted by both OpenAI and Anthropic)
TOOLS = [
    {
        "name": "rag_retrieve",
        "description": "Semantically retrieve relevant document chunks from the knowledge base. Use when the user asks a question that might be answered by ingested documents.",
        "parameters": {
            "type": "object",
            "properties": {
                "query": {"type": "string", "description": "The question or search query"}
            },
            "required": ["query"],
        },
    },
    {
        "name": "rag_ingest",
        "description": "Ingest text content into the knowledge base for future retrieval.",
        "parameters": {
            "type": "object",
            "properties": {
                "text": {"type": "string", "description": "Text content to ingest"},
                "source": {"type": "string", "description": "Source identifier (default: manual)"},
            },
            "required": ["text"],
        },
    },
    {
        "name": "rag_status",
        "description": "Get RAG service health status and total chunk count.",
        "parameters": {"type": "object", "properties": {}},
    },
    {
        "name": "rag_list_sources",
        "description": "List all ingested sources and their chunk counts.",
        "parameters": {"type": "object", "properties": {}},
    },
    {
        "name": "rag_delete_source",
        "description": "Delete all chunks from a specific source in the knowledge base.",
        "parameters": {
            "type": "object",
            "properties": {
                "source": {"type": "string", "description": "Source identifier to delete"}
            },
            "required": ["source"],
        },
    },
    {
        "name": "set_chunk_strategy",
        "description": (
            "Switch the document chunking strategy used for future ingestions. "
            "Choices: 'fixed' (sentence-level fixed size), 'structure' (Markdown structure-aware), "
            "'semantic' (embedding-similarity based), 'agentic' (LLM-driven smart chunking with "
            "per-chunk summary; higher cost, best for high-value documents). "
            "Only affects newly ingested documents; already-ingested chunks are not re-chunked."
        ),
        "parameters": {
            "type": "object",
            "properties": {
                "strategy": {
                    "type": "string",
                    "enum": ["fixed", "structure", "semantic", "agentic"],
                    "description": "The chunking strategy to activate.",
                }
            },
            "required": ["strategy"],
        },
    },
    {
        "name": "get_chunk_strategy",
        "description": "Get the current document chunking strategy and related parameters.",
        "parameters": {"type": "object", "properties": {}},
    },
]


async def execute_tool(name: str, args: dict) -> str:
    async with httpx.AsyncClient(timeout=_TIMEOUT) as client:
        if name == "rag_retrieve":
            r = await client.post(f"{_BASE}/retrieve", json={"text": args["query"]})
            r.raise_for_status()
            chunks = r.json().get("chunks", [])
            return "\n---\n".join(chunks) if chunks else "No relevant results found."

        if name == "rag_ingest":
            payload = {"text": args["text"], "source": args.get("source", "manual")}
            r = await client.post(f"{_BASE}/ingest", json=payload)
            r.raise_for_status()
            return r.json().get("message", "Ingestion complete.")

        if name == "rag_status":
            health = (await client.get(f"{_BASE}/health")).json()
            stats = (await client.get(f"{_BASE}/stats")).json()
            return (
                f"Status: {health.get('status', 'unknown')} | "
                f"Chunks: {stats.get('total_chunks', 0)}"
            )

        if name == "rag_list_sources":
            r = await client.get(f"{_BASE}/sources")
            r.raise_for_status()
            sources = r.json().get("sources", [])
            if not sources:
                return "No sources ingested yet."
            return "\n".join(f"- {s['source']}: {s['count']} chunks" for s in sources)

        if name == "rag_delete_source":
            r = await client.delete(f"{_BASE}/source", params={"source": args["source"]})
            r.raise_for_status()
            return r.json().get("message", f"Source '{args['source']}' deleted.")

        if name == "set_chunk_strategy":
            strategy = (args.get("strategy") or "").strip().lower()
            r = await client.put(
                f"{_BASE}/config/chunk-strategy",
                json={"strategy": strategy},
            )
            if r.status_code >= 400:
                # 透传后端的验证错误信息为自然语言，便于 agent 重试或向用户解释
                try:
                    detail = r.json().get("detail", r.text)
                except Exception:
                    detail = r.text
                return f"Failed to set chunk strategy: {detail}"
            data = r.json()
            return (
                f"Chunk strategy set to '{data.get('strategy')}'. "
                f"{data.get('note', '')}".strip()
            )

        if name == "get_chunk_strategy":
            r = await client.get(f"{_BASE}/config/chunk-strategy")
            r.raise_for_status()
            data = r.json()
            sem = data.get("semantic", {})
            ag = data.get("agentic", {})
            return (
                f"Current strategy: {data.get('strategy')} "
                f"(valid: {data.get('valid')}); "
                f"structure_aware={data.get('structure_aware')}; "
                f"semantic.threshold_percentile={sem.get('threshold_percentile')}, "
                f"min_chunk_size={sem.get('min_chunk_size')}, "
                f"max_chunk_size={sem.get('max_chunk_size')}; "
                f"agentic.generate_summary={ag.get('generate_summary')}, "
                f"max_llm_input_tokens={ag.get('max_llm_input_tokens')}"
            )

    raise ValueError(f"Unknown tool: {name}")
