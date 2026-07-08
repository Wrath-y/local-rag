package agent

import (
	"database/sql"
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
`
	_, err := sm.db.Exec(schema)
	return err
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
