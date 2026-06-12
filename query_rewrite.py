"""Query rewriting strategies for improved RAG retrieval.

Three strategies:
  expansion   — expand query with synonyms/related terms (default)
  hyde        — generate a hypothetical document, use its embedding
  multi_query — generate N query variants, retrieve each, merge results
"""

from __future__ import annotations

import os
from typing import Optional

import yaml

_cfg_path = os.path.join(os.path.dirname(__file__), "config.yaml")
with open(_cfg_path) as _f:
    _cfg = yaml.safe_load(_f)

_qr_cfg: dict = _cfg.get("query_rewrite", {})


def _get_llm():
    from llm.factory import get_provider
    llm_base = dict(_cfg.get("llm", {}))
    # allow query_rewrite to override the provider
    override = _qr_cfg.get("provider")
    if override:
        llm_base["provider"] = override
    return get_provider(llm_base)


async def _rewrite_expansion(query: str) -> str:
    llm = _get_llm()
    prompt = (
        "Rewrite the following search query to be more detailed and include "
        "synonyms or related terms. Output ONLY the rewritten query, no explanation:\n\n"
        f"{query}"
    )
    return await llm.complete([{"role": "user", "content": prompt}])


async def _rewrite_hyde(query: str) -> str:
    llm = _get_llm()
    prompt = (
        "Write a short passage (3-5 sentences) that would directly answer the "
        "following question. Output ONLY the passage, no explanation:\n\n"
        f"{query}"
    )
    return await llm.complete([{"role": "user", "content": prompt}])


async def _rewrite_multi_query(query: str, n: int = 3) -> list[str]:
    llm = _get_llm()
    prompt = (
        f"Generate {n} different versions of the following search query that "
        "convey the same meaning but use different wording. "
        "Output one query per line with no numbering or extra text:\n\n"
        f"{query}"
    )
    result = await llm.complete([{"role": "user", "content": prompt}])
    variants = [line.strip() for line in result.strip().splitlines() if line.strip()]
    return variants[:n] if variants else [query]


async def rewrite(query: str, strategy: Optional[str] = None) -> list[str]:
    """Return a list of rewritten queries.

    expansion / hyde  → single-element list
    multi_query       → list of N variants
    unknown strategy  → [original query] (graceful fallback)
    """
    strategy = strategy or _qr_cfg.get("strategy", "expansion")

    if strategy == "expansion":
        return [await _rewrite_expansion(query)]
    if strategy == "hyde":
        return [await _rewrite_hyde(query)]
    if strategy == "multi_query":
        return await _rewrite_multi_query(query)

    return [query]
