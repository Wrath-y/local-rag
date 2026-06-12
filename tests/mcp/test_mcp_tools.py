import pytest
from unittest.mock import AsyncMock, MagicMock, patch


def _mock_response(json_data: dict, status_code: int = 200) -> MagicMock:
    r = MagicMock()
    r.json.return_value = json_data
    r.status_code = status_code
    r.raise_for_status = MagicMock()
    return r


@pytest.fixture()
def mock_client(monkeypatch):
    """Patch httpx.AsyncClient to return a controllable mock."""
    client = MagicMock()
    client.__aenter__ = AsyncMock(return_value=client)
    client.__aexit__ = AsyncMock(return_value=False)

    monkeypatch.setattr("httpx.AsyncClient", MagicMock(return_value=client))
    return client


# ── rag_retrieve ─────────────────────────────────────────────────────────────

@pytest.mark.asyncio
async def test_rag_retrieve_returns_chunks(mock_client):
    mock_client.post = AsyncMock(
        return_value=_mock_response({"chunks": ["chunk1", "chunk2"]})
    )
    from mcp_tools import rag_retrieve
    result = await rag_retrieve("What is Redis?")
    assert "chunk1" in result
    assert "chunk2" in result
    mock_client.post.assert_called_once()
    call_json = mock_client.post.call_args.kwargs["json"]
    assert call_json["text"] == "What is Redis?"


@pytest.mark.asyncio
async def test_rag_retrieve_empty(mock_client):
    mock_client.post = AsyncMock(return_value=_mock_response({"chunks": []}))
    from mcp_tools import rag_retrieve
    result = await rag_retrieve("nothing")
    assert result == "No relevant results found."


@pytest.mark.asyncio
async def test_rag_retrieve_with_top_k(mock_client):
    mock_client.post = AsyncMock(return_value=_mock_response({"chunks": ["c"]}))
    from mcp_tools import rag_retrieve
    await rag_retrieve("query", top_k=5)
    call_json = mock_client.post.call_args.kwargs["json"]
    assert call_json["top_k"] == 5


# ── rag_ingest ───────────────────────────────────────────────────────────────

@pytest.mark.asyncio
async def test_rag_ingest(mock_client):
    mock_client.post = AsyncMock(return_value=_mock_response({"message": "OK 3 chunks"}))
    from mcp_tools import rag_ingest
    result = await rag_ingest("some text", source="docs")
    assert "OK 3 chunks" in result
    call_json = mock_client.post.call_args.kwargs["json"]
    assert call_json["source"] == "docs"


# ── rag_delete_source ────────────────────────────────────────────────────────

@pytest.mark.asyncio
async def test_rag_delete_source(mock_client):
    mock_client.delete = AsyncMock(
        return_value=_mock_response({"message": "Deleted source 'docs'"})
    )
    from mcp_tools import rag_delete_source
    result = await rag_delete_source("docs")
    assert "docs" in result


# ── rag_status ───────────────────────────────────────────────────────────────

@pytest.mark.asyncio
async def test_rag_status(mock_client):
    mock_client.get = AsyncMock(side_effect=[
        _mock_response({"status": "ok"}),
        _mock_response({"total_chunks": 42}),
    ])
    from mcp_tools import rag_status
    result = await rag_status()
    assert "ok" in result
    assert "42" in result


# ── rag_list_sources ─────────────────────────────────────────────────────────

@pytest.mark.asyncio
async def test_rag_list_sources(mock_client):
    mock_client.get = AsyncMock(return_value=_mock_response({
        "sources": [
            {"source": "api-spec", "count": 10},
            {"source": "readme", "count": 5},
        ]
    }))
    from mcp_tools import rag_list_sources
    result = await rag_list_sources()
    assert "api-spec" in result
    assert "10" in result


@pytest.mark.asyncio
async def test_rag_list_sources_empty(mock_client):
    mock_client.get = AsyncMock(return_value=_mock_response({"sources": []}))
    from mcp_tools import rag_list_sources
    result = await rag_list_sources()
    assert result == "No sources ingested yet."
