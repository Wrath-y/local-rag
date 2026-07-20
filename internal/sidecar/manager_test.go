package sidecar

import (
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"
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

// TestHelperProcess is a Go helper-process fixture.
// It is NOT a real test; it is re-spawned as a subprocess.
//
// Behaviour:
//   - Waits for SIGTERM.
//   - On receipt, writes a sentinel file whose path is passed via
//     GO_SENTINEL_FILE, then exits 0.
//
// A SIGKILL kills the process instantly so the handler never runs and
// the sentinel is never written.  This lets the parent test distinguish
// SIGTERM-first (sentinel present) from SIGKILL-first (sentinel absent).
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	sentinel := os.Getenv("GO_SENTINEL_FILE")

	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGTERM)
	<-ch

	// Write sentinel to prove SIGTERM was received.
	if sentinel != "" {
		_ = os.WriteFile(sentinel, []byte("sigterm"), 0600)
	}
	os.Exit(0)
}

// TestManager_StopTerminatesCooperativeProcess verifies that Stop sends
// SIGTERM first so cooperative children can clean up, rather than jumping
// straight to SIGKILL (which would prevent Python multiprocessing
// resource_tracker from running its cleanup).
//
// The sentinel file mechanism:
//   - The helper child writes a file only when it receives SIGTERM.
//   - SIGKILL kills the process before the handler can run → no file.
//   - SIGTERM-first → handler runs → file written → assertion passes.
func TestManager_StopTerminatesCooperativeProcess(t *testing.T) {
	// Sentinel file written by the child iff it received SIGTERM.
	sentinel := filepath.Join(t.TempDir(), "sigterm.received")

	// Locate the test binary that is already running (us).
	exe, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatalf("cannot resolve test binary path: %v", err)
	}

	// Spawn the cooperative helper.
	cmd := exec.Command(exe, "-test.run=TestHelperProcess", "-test.v")
	cmd.Env = append(os.Environ(),
		"GO_WANT_HELPER_PROCESS=1",
		"GO_SENTINEL_FILE="+sentinel,
	)
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		t.Fatalf("could not start helper process: %v", err)
	}

	// Give the helper a moment to reach the signal.Notify call.
	time.Sleep(80 * time.Millisecond)

	// Wire the started cmd directly into a Manager (skipping Start/launch
	// so we don't need a real Python sidecar).
	m := New(Config{
		Provider:        "local",
		Port:            8765,
		ShutdownTimeout: time.Second,
	})
	m.mu.Lock()
	m.cmd = cmd
	m.running = true
	m.mu.Unlock()

	// Stop must complete within ShutdownTimeout + buffer.
	done := make(chan struct{})
	go func() {
		m.Stop()
		close(done)
	}()

	select {
	case <-done:
		// good
	case <-time.After(3 * time.Second):
		t.Fatal("Stop() did not return within 3s")
	}

	// Manager state must be cleared.
	if m.Running() {
		t.Error("manager still reports running=true after Stop()")
	}

	// The sentinel file must exist — proving SIGTERM was sent, not SIGKILL.
	if _, err := os.Stat(sentinel); os.IsNotExist(err) {
		t.Error("sentinel file not written: Stop() used SIGKILL instead of SIGTERM")
	}

	// Confirm the child is no longer alive (not a zombie).
	if err := cmd.Process.Signal(syscall.Signal(0)); err == nil {
		t.Error("child process appears still alive after Stop()")
	}
}
