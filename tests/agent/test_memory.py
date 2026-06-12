import os
import tempfile
import pytest

# Redirect DB to a temp file for each test
@pytest.fixture(autouse=True)
def isolated_db(tmp_path, monkeypatch):
    db_path = str(tmp_path / "test_agent.db")
    import agent.db as db_mod
    monkeypatch.setattr(db_mod, "_DB_PATH", db_path)
    # Reset thread-local connection so each test gets a fresh DB
    import threading
    monkeypatch.setattr(db_mod, "_local", threading.local())
    yield


def test_new_session_creates_row():
    from agent.memory import new_session, list_sessions
    sid = new_session()
    assert len(sid) == 36  # UUID format
    sessions = list_sessions()
    assert any(s["id"] == sid for s in sessions)


def test_append_and_load_history():
    from agent.memory import new_session, append_message, load_history
    sid = new_session()
    append_message(sid, "user", "hello")
    append_message(sid, "assistant", "world")
    history = load_history(sid)
    assert len(history) == 2
    assert history[0] == {"role": "user", "content": "hello"}
    assert history[1] == {"role": "assistant", "content": "world"}


def test_delete_session_cascades():
    from agent.memory import new_session, append_message, delete_session, list_sessions, load_history
    sid = new_session()
    append_message(sid, "user", "test")
    delete_session(sid)
    sessions = list_sessions()
    assert not any(s["id"] == sid for s in sessions)
    assert load_history(sid) == []


def test_list_sessions_ordered_newest_first():
    from agent.memory import new_session, list_sessions
    import time
    s1 = new_session({"label": "first"})
    time.sleep(0.01)
    s2 = new_session({"label": "second"})
    sessions = list_sessions()
    ids = [s["id"] for s in sessions]
    assert ids.index(s2) < ids.index(s1)


def test_session_metadata_preserved():
    from agent.memory import new_session, list_sessions
    sid = new_session({"title": "test chat"})
    sessions = list_sessions()
    matched = next(s for s in sessions if s["id"] == sid)
    assert matched["metadata"]["title"] == "test chat"
