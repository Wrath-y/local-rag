import pytest
from unittest.mock import AsyncMock, patch


@pytest.fixture(autouse=True)
def fake_env(monkeypatch):
    monkeypatch.setenv("ANTHROPIC_API_KEY", "sk-test")


@pytest.fixture(autouse=True)
def patch_llm(monkeypatch):
    """Patch get_provider so no real LLM calls are made."""
    mock_provider = AsyncMock()
    with patch("query_rewrite._get_llm", return_value=mock_provider):
        yield mock_provider


# ── expansion ────────────────────────────────────────────────────────────────

@pytest.mark.asyncio
async def test_expansion_returns_single_string(patch_llm):
    patch_llm.complete = AsyncMock(return_value="Redis cache eviction LRU policy timeout")
    from query_rewrite import rewrite
    result = await rewrite("redis问题", strategy="expansion")
    assert isinstance(result, list)
    assert len(result) == 1
    assert "Redis" in result[0]


@pytest.mark.asyncio
async def test_expansion_llm_called_with_query(patch_llm):
    patch_llm.complete = AsyncMock(return_value="expanded query")
    from query_rewrite import rewrite
    await rewrite("my query", strategy="expansion")
    patch_llm.complete.assert_called_once()
    prompt_content = patch_llm.complete.call_args[0][0][0]["content"]
    assert "my query" in prompt_content


# ── hyde ─────────────────────────────────────────────────────────────────────

@pytest.mark.asyncio
async def test_hyde_returns_single_string(patch_llm):
    patch_llm.complete = AsyncMock(return_value="Redis is an in-memory data store...")
    from query_rewrite import rewrite
    result = await rewrite("what is redis", strategy="hyde")
    assert len(result) == 1
    assert "Redis" in result[0]


# ── multi_query ───────────────────────────────────────────────────────────────

@pytest.mark.asyncio
async def test_multi_query_returns_list(patch_llm):
    patch_llm.complete = AsyncMock(return_value="query variant 1\nquery variant 2\nquery variant 3")
    from query_rewrite import rewrite
    result = await rewrite("tell me about redis", strategy="multi_query")
    assert isinstance(result, list)
    assert len(result) == 3
    assert result[0] == "query variant 1"


@pytest.mark.asyncio
async def test_multi_query_strips_empty_lines(patch_llm):
    patch_llm.complete = AsyncMock(return_value="\nvariant A\n\nvariant B\n")
    from query_rewrite import rewrite
    result = await rewrite("q", strategy="multi_query")
    assert "" not in result
    assert len(result) == 2


@pytest.mark.asyncio
async def test_multi_query_fallback_on_empty_response(patch_llm):
    patch_llm.complete = AsyncMock(return_value="   ")
    from query_rewrite import rewrite
    result = await rewrite("original query", strategy="multi_query")
    assert result == ["original query"]


# ── unknown strategy ──────────────────────────────────────────────────────────

@pytest.mark.asyncio
async def test_unknown_strategy_returns_original(patch_llm):
    from query_rewrite import rewrite
    result = await rewrite("original", strategy="nonexistent")
    assert result == ["original"]
    patch_llm.complete.assert_not_called()
