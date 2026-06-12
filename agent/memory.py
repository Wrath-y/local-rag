import json
import time
import uuid

from .db import get_conn


def new_session(metadata: dict | None = None) -> str:
    sid = str(uuid.uuid4())
    get_conn().execute(
        "INSERT INTO sessions(id, created_at, metadata) VALUES (?, ?, ?)",
        (sid, int(time.time()), json.dumps(metadata or {})),
    )
    get_conn().commit()
    return sid


def append_message(session_id: str, role: str, content: str) -> None:
    get_conn().execute(
        "INSERT INTO messages(session_id, role, content, timestamp) VALUES (?, ?, ?, ?)",
        (session_id, role, content, int(time.time())),
    )
    get_conn().commit()


def load_history(session_id: str) -> list[dict]:
    rows = get_conn().execute(
        "SELECT role, content FROM messages WHERE session_id = ? ORDER BY timestamp",
        (session_id,),
    ).fetchall()
    return [{"role": role, "content": content} for role, content in rows]


def list_sessions() -> list[dict]:
    rows = get_conn().execute(
        "SELECT id, created_at, metadata FROM sessions ORDER BY created_at DESC"
    ).fetchall()
    return [{"id": r[0], "created_at": r[1], "metadata": json.loads(r[2])} for r in rows]


def delete_session(session_id: str) -> None:
    get_conn().execute("DELETE FROM sessions WHERE id = ?", (session_id,))
    get_conn().commit()
