package agent

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Session represents an agent conversation session.
type Session struct {
	ID        string `json:"id"`
	CreatedAt int64  `json:"created_at"`
	Metadata  string `json:"metadata"`
}

// SessionManager manages agent sessions stored in SQLite.
type SessionManager struct {
	db *sql.DB
}

// ToolTrace contains privacy-minimized metadata for one tool attempt or a
// terminal Agent outcome. Prompts and retrieved text are intentionally absent.
type ToolTrace struct {
	ID            int64         `json:"id"`
	SessionID     string        `json:"session_id"`
	CallID        string        `json:"call_id,omitempty"`
	Tool          string        `json:"tool,omitempty"`
	StartedAt     time.Time     `json:"started_at,omitempty"`
	Duration      time.Duration `json:"duration"`
	Outcome       string        `json:"outcome"`
	ResultCount   int           `json:"result_count"`
	EvidenceIDs   []int         `json:"evidence_ids,omitempty"`
	ErrorCategory string        `json:"error_category,omitempty"`
}

// NewSessionManager creates a SessionManager and ensures schema exists.
func NewSessionManager(db *sql.DB) (*SessionManager, error) {
	sm := &SessionManager{db: db}
	if err := sm.createTables(); err != nil {
		return nil, fmt.Errorf("agent: create tables: %w", err)
	}
	return sm, nil
}

// createTables creates the sessions and messages tables if they don't exist.
func (sm *SessionManager) createTables() error {
	schema := `
CREATE TABLE IF NOT EXISTS sessions (
    id         TEXT    PRIMARY KEY,
    created_at INTEGER NOT NULL,
    metadata   TEXT    NOT NULL DEFAULT '{}'
);

CREATE TABLE IF NOT EXISTS messages (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT    NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    role       TEXT    NOT NULL,
    content    TEXT    NOT NULL,
    timestamp  INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS agent_tool_traces (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id     TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    call_id        TEXT NOT NULL DEFAULT '',
    tool           TEXT NOT NULL DEFAULT '',
    started_at     INTEGER NOT NULL,
    duration_ms    INTEGER NOT NULL DEFAULT 0,
    outcome        TEXT NOT NULL,
    result_count   INTEGER NOT NULL DEFAULT 0,
    evidence_ids   TEXT NOT NULL DEFAULT '[]',
    error_category TEXT NOT NULL DEFAULT ''
);
`
	_, err := sm.db.Exec(schema)
	return err
}

// RecordToolTrace persists only diagnostic metadata. Failures are returned to
// callers that need to surface storage errors without recording user content.
func (sm *SessionManager) RecordToolTrace(trace ToolTrace) error {
	startedAt := trace.StartedAt
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	evidenceIDs, err := json.Marshal(trace.EvidenceIDs)
	if err != nil {
		return fmt.Errorf("agent: marshal trace evidence IDs: %w", err)
	}
	_, err = sm.db.Exec(`INSERT INTO agent_tool_traces (session_id, call_id, tool, started_at, duration_ms, outcome, result_count, evidence_ids, error_category) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		trace.SessionID, trace.CallID, trace.Tool, startedAt.Unix(), trace.Duration.Milliseconds(), trace.Outcome, trace.ResultCount, string(evidenceIDs), trace.ErrorCategory)
	if err != nil {
		return fmt.Errorf("agent: record tool trace: %w", err)
	}
	return nil
}

// ListToolTraces returns safe diagnostic metadata for a session in call order.
func (sm *SessionManager) ListToolTraces(sessionID string) ([]ToolTrace, error) {
	rows, err := sm.db.Query(`SELECT id, session_id, call_id, tool, started_at, duration_ms, outcome, result_count, evidence_ids, error_category FROM agent_tool_traces WHERE session_id = ? ORDER BY id ASC`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("agent: list tool traces: %w", err)
	}
	defer rows.Close()
	var traces []ToolTrace
	for rows.Next() {
		var trace ToolTrace
		var startedAt, durationMs int64
		var evidenceIDs string
		if err := rows.Scan(&trace.ID, &trace.SessionID, &trace.CallID, &trace.Tool, &startedAt, &durationMs, &trace.Outcome, &trace.ResultCount, &evidenceIDs, &trace.ErrorCategory); err != nil {
			return nil, fmt.Errorf("agent: scan tool trace: %w", err)
		}
		trace.StartedAt, trace.Duration = time.Unix(startedAt, 0), time.Duration(durationMs)*time.Millisecond
		if err := json.Unmarshal([]byte(evidenceIDs), &trace.EvidenceIDs); err != nil {
			return nil, fmt.Errorf("agent: decode trace evidence IDs: %w", err)
		}
		traces = append(traces, trace)
	}
	return traces, rows.Err()
}

// Create inserts a new session and returns its ID.
func (sm *SessionManager) Create(metadata string) (string, error) {
	if metadata == "" {
		metadata = "{}"
	}
	id := uuid.New().String()
	now := time.Now().Unix()
	_, err := sm.db.Exec(
		`INSERT INTO sessions (id, created_at, metadata) VALUES (?, ?, ?)`,
		id, now, metadata,
	)
	if err != nil {
		return "", fmt.Errorf("agent: create session: %w", err)
	}
	return id, nil
}

// List returns all sessions ordered by created_at descending.
func (sm *SessionManager) List() ([]Session, error) {
	rows, err := sm.db.Query(
		`SELECT id, created_at, metadata FROM sessions ORDER BY created_at DESC`,
	)
	if err != nil {
		return nil, fmt.Errorf("agent: list sessions: %w", err)
	}
	defer rows.Close()

	var sessions []Session
	for rows.Next() {
		var s Session
		if err := rows.Scan(&s.ID, &s.CreatedAt, &s.Metadata); err != nil {
			return nil, fmt.Errorf("agent: scan session: %w", err)
		}
		sessions = append(sessions, s)
	}
	return sessions, rows.Err()
}

// Delete removes a session and its messages (CASCADE).
func (sm *SessionManager) Delete(id string) error {
	res, err := sm.db.Exec(`DELETE FROM sessions WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("agent: delete session %q: %w", id, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("agent: session %q not found", id)
	}
	return nil
}

// AppendMessage adds a message to a session.
func (sm *SessionManager) AppendMessage(sessionID, role, content string) error {
	now := time.Now().Unix()
	_, err := sm.db.Exec(
		`INSERT INTO messages (session_id, role, content, timestamp) VALUES (?, ?, ?, ?)`,
		sessionID, role, content, now,
	)
	if err != nil {
		return fmt.Errorf("agent: append message: %w", err)
	}
	return nil
}

// LoadHistory returns all messages in a session as [{role, content}] ordered by id.
func (sm *SessionManager) LoadHistory(sessionID string) ([]map[string]string, error) {
	rows, err := sm.db.Query(
		`SELECT role, content FROM messages WHERE session_id = ? ORDER BY id ASC`,
		sessionID,
	)
	if err != nil {
		return nil, fmt.Errorf("agent: load history: %w", err)
	}
	defer rows.Close()

	var history []map[string]string
	for rows.Next() {
		var role, content string
		if err := rows.Scan(&role, &content); err != nil {
			return nil, fmt.Errorf("agent: scan message: %w", err)
		}
		history = append(history, map[string]string{"role": role, "content": content})
	}
	return history, rows.Err()
}
