import json
import pytest
from unittest.mock import AsyncMock, patch
import threading


@pytest.fixture(autouse=True)
def isolated_db(tmp_path, monkeypatch):
    import agent.db as db_mod
    monkeypatch.setattr(db_mod, "_DB_PATH", str(tmp_path / "test.db"))
    monkeypatch.setattr(db_mod, "_local", threading.local())


@pytest.fixture(autouse=True)
def fake_llm_cfg(monkeypatch):
    import agent.loop as loop_mod
    monkeypatch.setattr(loop_mod, "_cfg", {
        "llm": {"provider": "anthropic", "model": "test", "api_key_env": "X"}
    })


@pytest.mark.asyncio
async def test_plain_text_reply():
    from agent.memory import new_session
    from agent.loop import run

    sid = new_session()

    with patch("agent.loop._llm") as mock_llm:
        mock_llm.complete = AsyncMock(return_value="Hello back!")
        reply = await run(sid, "Hello")

    assert reply == "Hello back!"

    from agent.memory import load_history
    history = load_history(sid)
    assert history[-1] == {"role": "assistant", "content": "Hello back!"}


@pytest.mark.asyncio
async def test_tool_call_then_answer():
    from agent.memory import new_session
    from agent.loop import run

    sid = new_session()

    tool_response = [{"type": "tool_use", "name": "rag_retrieve", "input": {"query": "redis"}}]
    final_response = "Redis is an in-memory data store."

    with patch("agent.loop._llm") as mock_llm, \
         patch("agent.loop.execute_tool", new=AsyncMock(return_value="chunk1")) as mock_tool:
        mock_llm.complete = AsyncMock(side_effect=[tool_response, final_response])
        reply = await run(sid, "What is Redis?")

    assert reply == final_response
    mock_tool.assert_called_once_with("rag_retrieve", {"query": "redis"})

    from agent.memory import load_history
    history = load_history(sid)
    roles = [m["role"] for m in history]
    assert roles == ["user", "tool", "assistant"]


@pytest.mark.asyncio
async def test_max_tool_rounds_fallback():
    from agent.memory import new_session
    from agent.loop import run, MAX_TOOL_ROUNDS

    sid = new_session()

    # Always return a tool call → should hit MAX_TOOL_ROUNDS
    tool_response = [{"type": "tool_use", "name": "rag_retrieve", "input": {"query": "x"}}]

    with patch("agent.loop._llm") as mock_llm, \
         patch("agent.loop.execute_tool", new=AsyncMock(return_value="result")):
        mock_llm.complete = AsyncMock(return_value=tool_response)
        reply = await run(sid, "Loop forever")

    assert "maximum" in reply.lower() or "rephrase" in reply.lower()
    assert mock_llm.complete.call_count == MAX_TOOL_ROUNDS


@pytest.mark.asyncio
async def test_empty_input_handled():
    from agent.memory import new_session
    from agent.loop import run

    sid = new_session()
    with patch("agent.loop._llm") as mock_llm:
        mock_llm.complete = AsyncMock(return_value="I need more input.")
        reply = await run(sid, "")

    assert isinstance(reply, str)
