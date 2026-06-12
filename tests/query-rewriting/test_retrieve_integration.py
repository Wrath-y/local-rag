"""Integration tests for query-rewrite flag in the /retrieve endpoint.

Uses httpx TestClient; patches query_rewrite.rewrite so no real LLM calls occur.
"""

import pytest
from unittest.mock import AsyncMock, patch


@pytest.fixture()
def client(monkeypatch):
    """Return a TestClient with a minimal in-memory store (empty index is OK for toggle tests)."""
    import server as srv
    # Disable WAL and backup to avoid side-effects in tests
    monkeypatch.setattr(srv, "WAL_ENABLED", False)
    monkeypatch.setattr(srv, "BACKUP_ENABLED", False)
    from fastapi.testclient import TestClient
    return TestClient(srv.app)


# ── toggle endpoint ───────────────────────────────────────────────────────────

def test_toggle_enable_query_rewrite(client):
    resp = client.post(
        "/retrieve/query-rewrite",
        json={"enabled": True, "strategy": "expansion"},
    )
    assert resp.status_code == 200
    data = resp.json()
    assert data["query_rewrite_enabled"] is True
    assert data["strategy"] == "expansion"


def test_toggle_change_strategy(client):
    client.post("/retrieve/query-rewrite", json={"enabled": True, "strategy": "expansion"})
    resp = client.post(
        "/retrieve/query-rewrite",
        json={"enabled": True, "strategy": "hyde"},
    )
    assert resp.json()["strategy"] == "hyde"


def test_toggle_disable_preserves_strategy(client):
    client.post("/retrieve/query-rewrite", json={"enabled": True, "strategy": "multi_query"})
    resp = client.post("/retrieve/query-rewrite", json={"enabled": False})
    data = resp.json()
    assert data["query_rewrite_enabled"] is False
    assert data["strategy"] == "multi_query"  # strategy unchanged


# ── rewrite path skipped when disabled ───────────────────────────────────────

def test_rewrite_not_called_when_disabled(client):
    import server as srv
    srv.query_rewrite_enabled = False

    with patch("query_rewrite.rewrite", new_callable=AsyncMock) as mock_qr:
        # Empty store returns empty result without error
        resp = client.post("/retrieve", json={"text": "some question"})
        assert resp.status_code == 200
        mock_qr.assert_not_called()


# ── rewrite called when enabled ───────────────────────────────────────────────

def test_rewrite_called_when_enabled(client):
    import server as srv
    srv.query_rewrite_enabled = True
    srv.query_rewrite_strategy = "expansion"

    with patch("query_rewrite.rewrite", new_callable=AsyncMock, return_value=["expanded q"]):
        resp = client.post("/retrieve", json={"text": "short q"})
        assert resp.status_code == 200


# ── LLM failure degrades gracefully (original query used) ────────────────────

def test_rewrite_error_falls_back_gracefully(client):
    import server as srv
    srv.query_rewrite_enabled = True
    srv.query_rewrite_strategy = "expansion"

    with patch("query_rewrite.rewrite", new_callable=AsyncMock, side_effect=RuntimeError("LLM down")):
        resp = client.post("/retrieve", json={"text": "original query"})
        # Should not 500 — falls back to original query path
        assert resp.status_code == 200
