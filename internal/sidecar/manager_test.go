package sidecar

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Wrath-y/local-rag/internal/lifecycle"
)

// waitForFile polls path until the file appears or timeout elapses.
// Returns nil when the file is present, error on timeout.
func waitForFile(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		time.Sleep(2 * time.Millisecond)
	}
	return fmt.Errorf("timed out after %s waiting for %s", timeout, path)
}

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

func TestPythonExecutableMatchesHostConvention(t *testing.T) {
	tests := map[string]string{
		"windows": "python",
		"darwin":  "python3",
		"linux":   "python3",
	}
	for goos, want := range tests {
		if got := pythonExecutable(goos); got != want {
			t.Errorf("pythonExecutable(%q) = %q, want %q", goos, got, want)
		}
	}
}

func TestManager_StopIdempotent(t *testing.T) {
	m := New(Config{Provider: "openai", Port: 8766})
	// Stop on a never-started manager must not panic.
	m.Stop()
	m.Stop()
}

// TestHelperProcess is a Go helper-process fixture.
// It is NOT a real test; it is re-spawned as a subprocess by other tests.
//
// Modes (selected by GO_HELPER_MODE):
//
//   - "cooperative" (default): registers a SIGTERM handler, writes the ready
//     file, waits for SIGTERM, writes the sentinel file, then exits 0.
//
//   - "ignore-term": sets SIG_IGN for SIGTERM, writes the ready file, then
//     blocks forever.  Only SIGKILL can terminate this process.
//
// Environment variables consumed:
//
//	GO_WANT_HELPER_PROCESS=1    (must be set; activates helper mode)
//	GO_HELPER_MODE              (mode selector; defaults to "cooperative")
//	GO_READY_FILE               (path helper writes "ready" to once fully set up)
//	GO_SENTINEL_FILE            (cooperative mode only: path written on SIGTERM receipt)
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}

	mode := os.Getenv("GO_HELPER_MODE")
	if mode == "" {
		mode = "cooperative"
	}
	readyPath := os.Getenv("GO_READY_FILE")
	sentinel := os.Getenv("GO_SENTINEL_FILE")

	switch mode {
	case "cooperative":
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGTERM, os.Interrupt)

		// Signal readiness only after the handler is registered, ensuring the
		// parent cannot race-send SIGTERM before this process is listening.
		if readyPath != "" {
			_ = os.WriteFile(readyPath, []byte("ready"), 0600)
		}

		<-ch // wait for SIGTERM

		// Write sentinel to prove SIGTERM was received (not SIGKILL).
		if sentinel != "" {
			_ = os.WriteFile(sentinel, []byte("sigterm"), 0600)
		}
		os.Exit(0)

	case "ignore-term":
		// Capture SIGTERM via signal.Notify so the Go runtime overrides the OS
		// default termination action, then drain the channel in a goroutine.
		// This is equivalent to SIG_IGN for our purposes: the process survives
		// any number of SIGTERMs and only terminates on SIGKILL.
		//
		// Note: signal.Ignore() does NOT prevent termination on compiled Go
		// binaries because Go's runtime restores the default OS handler for
		// unhandled signals.  signal.Notify+drain is the correct approach.
		sigCh := make(chan os.Signal, 8)
		signal.Notify(sigCh, syscall.SIGTERM, os.Interrupt)
		go func() {
			for range sigCh {
			} // discard all SIGTERMs
		}()

		// Signal readiness only after SIGTERM handling is set up.
		if readyPath != "" {
			_ = os.WriteFile(readyPath, []byte("ready"), 0600)
		}

		// Block forever; only SIGKILL terminates this process.
		select {}

	default:
		fmt.Fprintf(os.Stderr, "TestHelperProcess: unknown GO_HELPER_MODE %q\n", mode)
		os.Exit(1)
	}
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
//
// Coordination uses a ready sentinel file instead of a fixed sleep, making
// the test deterministic regardless of scheduler timing.
func TestManager_StopTerminatesCooperativeProcess(t *testing.T) {
	tmp := t.TempDir()
	sentinel := filepath.Join(tmp, "sigterm.received")
	readyPath := filepath.Join(tmp, "ready")

	exe, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatalf("cannot resolve test binary path: %v", err)
	}

	// Spawn the cooperative helper.
	cmd := exec.Command(exe, "-test.run=TestHelperProcess", "-test.v")
	cmd.Env = append(os.Environ(),
		"GO_WANT_HELPER_PROCESS=1",
		"GO_HELPER_MODE=cooperative",
		"GO_READY_FILE="+readyPath,
		"GO_SENTINEL_FILE="+sentinel,
	)
	cmd.Stdout = nil
	cmd.Stderr = nil
	identity, err := lifecycle.StartProcess(cmd)
	if err != nil {
		t.Fatalf("could not start helper process: %v", err)
	}

	// Poll until the helper has registered its SIGTERM handler (not a fixed sleep).
	if err := waitForFile(readyPath, 5*time.Second); err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("helper readiness timeout: %v", err)
	}

	// Wire the started cmd directly into a Manager (skipping Start/launch
	// so we don't need a real Python sidecar).
	m := New(Config{
		Provider:        "local",
		Port:            8765,
		ShutdownTimeout: time.Second,
	})
	m.mu.Lock()
	m.cmd = cmd
	m.identity = identity
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

// TestManager_StopKillsUncooperativeProcessAfterTimeout verifies that Stop
// escalates to SIGKILL when a process ignores SIGTERM and does not exit
// within ShutdownTimeout, and that the process is fully reaped afterward.
//
// This is an existing-behaviour regression test: Task 1's stopGracefully()
// already implements the forced SIGKILL fallback, so this test passes
// immediately without any production-code changes.
//
// Helper coordination uses a ready sentinel file, making the test
// deterministic for both cooperative and uncooperative scenarios.
func TestManager_StopKillsUncooperativeProcessAfterTimeout(t *testing.T) {
	tmp := t.TempDir()
	readyPath := filepath.Join(tmp, "ready")

	exe, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatalf("cannot resolve test binary path: %v", err)
	}

	// Spawn the uncooperative helper that ignores SIGTERM.
	cmd := exec.Command(exe, "-test.run=TestHelperProcess", "-test.v")
	cmd.Env = append(os.Environ(),
		"GO_WANT_HELPER_PROCESS=1",
		"GO_HELPER_MODE=ignore-term",
		"GO_READY_FILE="+readyPath,
	)
	cmd.Stdout = nil
	cmd.Stderr = nil
	identity, err := lifecycle.StartProcess(cmd)
	if err != nil {
		t.Fatalf("could not start helper process: %v", err)
	}

	// Poll until the helper has registered SIGTERM as ignored.
	if err := waitForFile(readyPath, 5*time.Second); err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("helper readiness timeout: %v", err)
	}

	// Wire into Manager with a very short ShutdownTimeout.
	// The helper ignores SIGTERM, so Stop must escalate to SIGKILL after 25ms.
	m := New(Config{
		Provider:        "local",
		Port:            8765,
		ShutdownTimeout: 25 * time.Millisecond,
	})
	m.mu.Lock()
	m.cmd = cmd
	m.identity = identity
	m.running = true
	m.mu.Unlock()

	// Stop must return after SIGTERM timeout elapses and SIGKILL reaps the child.
	done := make(chan struct{})
	go func() {
		m.Stop()
		close(done)
	}()

	select {
	case <-done:
		// good — SIGKILL fallback fired and process was reaped
	case <-time.After(3 * time.Second):
		t.Fatal("Stop() did not return within 3s (SIGKILL fallback may be broken)")
	}

	// Manager state must be cleared.
	if m.Running() {
		t.Error("manager still reports running=true after Stop()")
	}

	// Process must be reaped — Signal(0) should fail for a dead process.
	if err := cmd.Process.Signal(syscall.Signal(0)); err == nil {
		t.Error("child process appears still alive after Stop(); was not reaped by SIGKILL")
	}
}

// 伪健康服务即使返回 200，也不能替代本轮子进程及其模型能力。
func TestHealthRequiresInstanceSchemaAndConfiguredModels(t *testing.T) {
	valid := map[string]any{"service": "local-rag-sidecar", "schema_version": "1.0", "status": "ok", "instance_id": "current", "models": map[string]string{"embedding": "embed", "rerank": "rerank"}}
	for _, test := range []struct {
		name, key string
		value     any
		want      bool
	}{
		{"ready", "", nil, true},
		{"other service", "service", "other", false},
		{"wrong schema", "schema_version", "2.0", false},
		{"old process", "instance_id", "previous", false},
		{"unavailable", "status", "unavailable", false},
		{"missing model", "models", map[string]string{"embedding": "embed"}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			doc := map[string]any{}
			for key, value := range valid {
				doc[key] = value
			}
			if test.key != "" {
				doc[test.key] = test.value
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(doc) }))
			defer server.Close()
			m := New(Config{Port: server.Listener.Addr().(*net.TCPAddr).Port, EmbedModel: "embed", RerankModel: "rerank"})
			m.token = "current"
			if got := m.checkHealth(); got != test.want {
				t.Fatalf("健康结果 %v，期望 %v", got, test.want)
			}
		})
	}
}

// 重启排队期间同时停止，取消必须先于任何后续进程创建。
func TestConcurrentStopPreventsQueuedRestart(t *testing.T) {
	m := New(Config{Provider: "local", PythonExecutable: "missing-sidecar-must-never-launch"})
	m.ctx, m.cancel = context.WithCancel(context.Background())
	m.opMu.Lock()
	restarted := make(chan struct{})
	go func() { m.restart(m.ctx); close(restarted) }()
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); m.Stop() }()
	}
	select {
	case <-m.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("停止未取消重启上下文")
	}
	m.opMu.Unlock()
	wg.Wait()
	<-restarted
	if m.cmd != nil || m.Running() {
		t.Fatal("停止后仍存在 sidecar 进程")
	}
	if err := m.Start(); err == nil {
		t.Fatal("已停止的管理器不应重新启动")
	}
}
