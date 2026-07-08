package sidecar

import (
	"testing"
)

func TestManager_SkipsWhenNotLocal(t *testing.T) {
	m := New(Config{Provider: "openai", Port: 8766})
	err := m.Start()
	if err != nil {
		t.Fatal(err)
	}
	if m.Running() {
		t.Error("should not be running")
	}
}

func TestManager_URLFormat(t *testing.T) {
	m := New(Config{Provider: "local", Port: 9999})
	want := "http://127.0.0.1:9999"
	if got := m.URL(); got != want {
		t.Errorf("URL() = %q, want %q", got, want)
	}
}

func TestManager_StopIdempotent(t *testing.T) {
	m := New(Config{Provider: "openai", Port: 8766})
	// Stop on a never-started manager must not panic.
	m.Stop()
	m.Stop()
}
