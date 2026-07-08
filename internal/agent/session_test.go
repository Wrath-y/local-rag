package agent_test

import (
	"testing"

	_ "modernc.org/sqlite"

	"database/sql"

	"github.com/Wrath-y/local-rag/internal/agent"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	// Enable foreign keys for CASCADE to work.
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		t.Fatalf("enable foreign keys: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestSessionCreate_And_List(t *testing.T) {
	db := openTestDB(t)
	sm, err := agent.NewSessionManager(db)
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}

	id1, err := sm.Create("{}")
	if err != nil {
		t.Fatalf("Create session 1: %v", err)
	}
	if id1 == "" {
		t.Fatal("expected non-empty session ID")
	}

	id2, err := sm.Create(`{"user": "test"}`)
	if err != nil {
		t.Fatalf("Create session 2: %v", err)
	}
	if id1 == id2 {
		t.Fatal("expected unique session IDs")
	}

	sessions, err := sm.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(sessions))
	}
	// ORDER BY created_at DESC — most recent first (id2 was created last).
	if sessions[0].ID != id2 && sessions[1].ID != id1 {
		// Allow either order if timestamps are equal (fast test machines).
		ids := map[string]bool{sessions[0].ID: true, sessions[1].ID: true}
		if !ids[id1] || !ids[id2] {
			t.Fatalf("unexpected session IDs in list: %v", sessions)
		}
	}
}

func TestAppendMessage_And_LoadHistory(t *testing.T) {
	db := openTestDB(t)
	sm, err := agent.NewSessionManager(db)
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}

	id, err := sm.Create("{}")
	if err != nil {
		t.Fatalf("Create session: %v", err)
	}

	if err := sm.AppendMessage(id, "user", "hello"); err != nil {
		t.Fatalf("AppendMessage user: %v", err)
	}
	if err := sm.AppendMessage(id, "assistant", "hi there"); err != nil {
		t.Fatalf("AppendMessage assistant: %v", err)
	}

	history, err := sm.LoadHistory(id)
	if err != nil {
		t.Fatalf("LoadHistory: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(history))
	}
	if history[0]["role"] != "user" || history[0]["content"] != "hello" {
		t.Errorf("message 0 mismatch: %v", history[0])
	}
	if history[1]["role"] != "assistant" || history[1]["content"] != "hi there" {
		t.Errorf("message 1 mismatch: %v", history[1])
	}
}

func TestDeleteSession(t *testing.T) {
	db := openTestDB(t)
	sm, err := agent.NewSessionManager(db)
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}

	id, err := sm.Create("{}")
	if err != nil {
		t.Fatalf("Create session: %v", err)
	}

	// Add a message so we can verify CASCADE.
	if err := sm.AppendMessage(id, "user", "test"); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	if err := sm.Delete(id); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	sessions, err := sm.List()
	if err != nil {
		t.Fatalf("List after delete: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("expected 0 sessions after delete, got %d", len(sessions))
	}

	// Messages should be gone via CASCADE.
	history, err := sm.LoadHistory(id)
	if err != nil {
		t.Fatalf("LoadHistory after delete: %v", err)
	}
	if len(history) != 0 {
		t.Fatalf("expected 0 messages after session delete, got %d", len(history))
	}

	// Deleting non-existent session should error.
	if err := sm.Delete("nonexistent"); err == nil {
		t.Fatal("expected error when deleting non-existent session")
	}
}
