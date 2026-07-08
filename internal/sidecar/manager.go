package sidecar

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"
)

// Config holds sidecar manager configuration.
type Config struct {
	Provider       string        // "local" means we need sidecar
	Port           int
	PythonPath     string        // path to sidecar/main.py
	HealthInterval time.Duration
	HealthRetries  int
	StartupTimeout time.Duration
}

// Manager manages the lifecycle of the Python sidecar process.
type Manager struct {
	cfg     Config
	cmd     *exec.Cmd
	mu      sync.Mutex
	running bool
	stopCh  chan struct{}
}

// New creates a new sidecar Manager.
func New(cfg Config) *Manager {
	return &Manager{
		cfg:    cfg,
		stopCh: make(chan struct{}),
	}
}

// Start launches the sidecar if provider=="local". No-op otherwise.
func (m *Manager) Start() error {
	if m.cfg.Provider != "local" {
		return nil
	}

	if err := m.launch(); err != nil {
		return fmt.Errorf("sidecar launch: %w", err)
	}

	// Wait for /health to respond 200.
	if err := m.waitHealthy(m.cfg.StartupTimeout); err != nil {
		m.killProcess()
		return fmt.Errorf("sidecar startup health check: %w", err)
	}

	m.mu.Lock()
	m.running = true
	m.stopCh = make(chan struct{})
	m.mu.Unlock()

	go m.healthLoop()
	return nil
}

// launch starts the subprocess.
func (m *Manager) launch() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	pythonPath := m.cfg.PythonPath
	if pythonPath == "" {
		pythonPath = "sidecar/main.py"
	}

	cmd := exec.Command("python3", pythonPath, "--port", fmt.Sprintf("%d", m.cfg.Port))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	m.cmd = cmd
	return nil
}

// waitHealthy polls /health until it returns 200 or the timeout elapses.
func (m *Manager) waitHealthy(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if m.checkHealth() {
				return nil
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("timed out after %s waiting for sidecar health", timeout)
			}
		}
	}
}

// checkHealth performs a single /health GET, returns true on HTTP 200.
func (m *Manager) checkHealth() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.URL()+"/health", nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// healthLoop monitors the sidecar and restarts it on consecutive failures.
func (m *Manager) healthLoop() {
	interval := m.cfg.HealthInterval
	if interval == 0 {
		interval = 10 * time.Second
	}
	retries := m.cfg.HealthRetries
	if retries == 0 {
		retries = 3
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	failures := 0
	for {
		select {
		case <-m.stopCh:
			return
		case <-ticker.C:
			if m.checkHealth() {
				failures = 0
				continue
			}
			failures++
			slog.Warn("sidecar health check failed", "consecutive_failures", failures)
			if failures >= retries {
				slog.Error("sidecar unresponsive, restarting", "failures", failures)
				m.restart()
				failures = 0
			}
		}
	}
}

// restart kills the current process and launches a new one.
func (m *Manager) restart() {
	m.killProcess()

	if err := m.launch(); err != nil {
		slog.Error("sidecar restart failed", "err", err)
		m.mu.Lock()
		m.running = false
		m.mu.Unlock()
		return
	}

	timeout := m.cfg.StartupTimeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	if err := m.waitHealthy(timeout); err != nil {
		slog.Error("sidecar restart health check failed", "err", err)
		m.killProcess()
		m.mu.Lock()
		m.running = false
		m.mu.Unlock()
		return
	}

	slog.Info("sidecar restarted successfully")
}

// killProcess sends Kill to the subprocess if it is set.
func (m *Manager) killProcess() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.cmd != nil && m.cmd.Process != nil {
		_ = m.cmd.Process.Kill()
		_ = m.cmd.Wait()
		m.cmd = nil
	}
	m.running = false
}

// Stop kills the sidecar process and halts the health loop.
func (m *Manager) Stop() {
	m.mu.Lock()
	ch := m.stopCh
	m.mu.Unlock()

	// Signal health loop to stop (non-blocking in case it was never started).
	select {
	case <-ch:
		// already closed
	default:
		close(ch)
	}

	m.killProcess()
}

// Running returns whether the sidecar is considered alive.
func (m *Manager) Running() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running
}

// URL returns the sidecar base URL.
func (m *Manager) URL() string {
	return fmt.Sprintf("http://127.0.0.1:%d", m.cfg.Port)
}
