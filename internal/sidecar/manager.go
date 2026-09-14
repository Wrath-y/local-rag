package sidecar

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Wrath-y/local-rag/internal/lifecycle"
)

// Config 仅在配置的嵌入或重排依赖本地模型时启动 sidecar。
type Config struct {
	Provider         string
	Port             int
	PythonPath       string
	PythonExecutable string
	ModelDir         string
	EmbedModel       string
	RerankModel      string
	HealthInterval   time.Duration
	HealthRetries    int
	StartupTimeout   time.Duration
	ShutdownTimeout  time.Duration
}

// Manager 将启停和重启串行化；停止取消健康等待后不再创建新进程。
type Manager struct {
	cfg      Config
	opMu     sync.Mutex
	mu       sync.Mutex
	cmd      *exec.Cmd
	identity lifecycle.Identity
	waitDone chan error
	running  bool
	stopping bool
	ctx      context.Context
	cancel   context.CancelFunc
	loopDone chan struct{}
	token    string
}

func New(cfg Config) *Manager { return &Manager{cfg: cfg} }

func (m *Manager) Start() error {
	if m.cfg.Provider != "local" {
		return nil
	}
	m.opMu.Lock()
	defer m.opMu.Unlock()
	m.mu.Lock()
	if m.stopping {
		m.mu.Unlock()
		return fmt.Errorf("sidecar 已停止，不能再次启动")
	}
	if m.running {
		m.mu.Unlock()
		return nil
	}
	if m.cmd != nil {
		m.mu.Unlock()
		return fmt.Errorf("前一个 sidecar 尚未完成清理，请检查进程诊断")
	}
	m.ctx, m.cancel = context.WithCancel(context.Background())
	ctx := m.ctx
	m.mu.Unlock()
	if err := m.launch(); err != nil {
		return fmt.Errorf("sidecar 启动失败：%w", err)
	}
	if err := m.waitHealthy(ctx, m.startupTimeout()); err != nil {
		m.stopProcess(true)
		return fmt.Errorf("sidecar 健康检查失败：%w", err)
	}
	m.mu.Lock()
	if m.stopping {
		m.mu.Unlock()
		m.stopProcess(false)
		return fmt.Errorf("sidecar 启动已取消")
	}
	m.running = true
	m.loopDone = make(chan struct{})
	done := m.loopDone
	m.mu.Unlock()
	go func() { defer close(done); m.healthLoop(ctx) }()
	return nil
}

// launch 只从持有 opMu 的路径调用，实例令牌由本轮生成并只经环境传给子进程。
func (m *Manager) launch() error {
	m.mu.Lock()
	stopping := m.stopping
	m.mu.Unlock()
	if stopping {
		return fmt.Errorf("sidecar 正在停止")
	}
	pythonPath := m.cfg.PythonPath
	if pythonPath == "" {
		pythonPath = "sidecar/main.py"
	}
	python := m.cfg.PythonExecutable
	if python == "" {
		python = pythonExecutable(runtime.GOOS)
	}
	args := []string{pythonPath, "--port", strconv.Itoa(m.cfg.Port), "--parent-pid", strconv.Itoa(os.Getpid())}
	for _, value := range []struct{ flag, value string }{{"--embed-model", m.cfg.EmbedModel}, {"--rerank-model", m.cfg.RerankModel}, {"--model-dir", m.cfg.ModelDir}} {
		if value.value != "" {
			args = append(args, value.flag, value.value)
		}
	}
	random := make([]byte, 24)
	if _, err := rand.Read(random); err != nil {
		return fmt.Errorf("无法创建 sidecar 实例标识")
	}
	token := hex.EncodeToString(random)
	cmd := exec.Command(python, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "LOCAL_RAG_SIDECAR_TOKEN=") && !strings.HasPrefix(value, "HF_HUB_OFFLINE=") && !strings.HasPrefix(value, "TRANSFORMERS_OFFLINE=") {
			cmd.Env = append(cmd.Env, value)
		}
	}
	cmd.Env = append(cmd.Env, "LOCAL_RAG_SIDECAR_TOKEN="+token, "HF_HUB_OFFLINE=1", "TRANSFORMERS_OFFLINE=1", "HF_HUB_DISABLE_TELEMETRY=1")
	identity, err := lifecycle.StartProcess(cmd)
	if err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	m.mu.Lock()
	m.cmd, m.identity, m.waitDone, m.token = cmd, identity, done, token
	m.mu.Unlock()
	return nil
}

func pythonExecutable(goos string) string {
	if goos == "windows" {
		return "python"
	}
	return "python3"
}

func (m *Manager) startupTimeout() time.Duration {
	if m.cfg.StartupTimeout > 0 {
		return m.cfg.StartupTimeout
	}
	return 30 * time.Second
}

func (m *Manager) waitHealthy(parent context.Context, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		m.mu.Lock()
		identity := m.identity
		m.mu.Unlock()
		if !identity.Matches() {
			return fmt.Errorf("sidecar 提前退出；请检查 Python 环境及已下载的配置模型")
		}
		if m.checkHealthContext(ctx) {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("等待 sidecar 健康超时或启动被取消")
		case <-ticker.C:
		}
	}
}

func (m *Manager) checkHealth() bool { return m.checkHealthContext(context.Background()) }

// checkHealthContext 同时核验本轮实例、身份、协议与已加载模型，拒绝无关的 HTTP 200。
func (m *Manager) checkHealthContext(parent context.Context) bool {
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.URL()+"/health", nil)
	if err != nil {
		return false
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var health struct {
		Service  string            `json:"service"`
		Schema   string            `json:"schema_version"`
		Status   string            `json:"status"`
		Instance string            `json:"instance_id"`
		Models   map[string]string `json:"models"`
	}
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 65536))
	if decoder.Decode(&health) != nil || decoder.Decode(new(any)) != io.EOF {
		return false
	}
	m.mu.Lock()
	token := m.token
	m.mu.Unlock()
	if token == "" || health.Service != "local-rag-sidecar" || health.Schema != "1.0" || health.Status != "ok" || health.Instance != token {
		return false
	}
	return (m.cfg.EmbedModel == "" || health.Models["embedding"] == m.cfg.EmbedModel) && (m.cfg.RerankModel == "" || health.Models["rerank"] == m.cfg.RerankModel)
}

func (m *Manager) healthLoop(ctx context.Context) {
	interval := m.cfg.HealthInterval
	if interval <= 0 {
		interval = 10 * time.Second
	}
	retries := m.cfg.HealthRetries
	if retries <= 0 {
		retries = 3
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	failures := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if m.checkHealthContext(ctx) {
				failures = 0
				continue
			}
			failures++
			if failures >= retries {
				slog.Warn("sidecar 连续健康失败，尝试重启", "failures", failures)
				m.restart(ctx)
				failures = 0
			}
		}
	}
}

func (m *Manager) restart(ctx context.Context) {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	if ctx.Err() != nil {
		return
	}
	if err := m.stopProcess(false); err != nil {
		return
	}
	if ctx.Err() != nil {
		return
	}
	if err := m.launch(); err != nil {
		slog.Error("sidecar 重启失败", "reason", err.Error())
		return
	}
	if err := m.waitHealthy(ctx, m.startupTimeout()); err != nil {
		m.stopProcess(true)
		slog.Error("sidecar 重启健康失败", "reason", err.Error())
		return
	}
	m.mu.Lock()
	m.running = !m.stopping
	m.mu.Unlock()
}

// stopProcess 只在 opMu 内执行；Wait 只有一个读取者，身份无法确认时不发送信号。
func (m *Manager) stopProcess(force bool) error {
	m.mu.Lock()
	cmd, identity, done := m.cmd, m.identity, m.waitDone
	m.cmd, m.waitDone, m.running = nil, nil, false
	m.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	// 兼容直接注入子进程的内部验收；真实启动均已在创建时保存身份。
	if identity.PID == 0 {
		identity, _ = lifecycle.Inspect(cmd.Process.Pid)
	}
	if done == nil {
		done = make(chan error, 1)
		go func() { done <- cmd.Wait() }()
	}
	timeout := m.cfg.ShutdownTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	var err error
	if force {
		err = lifecycle.Terminate(identity, true)
	} else {
		err = lifecycle.StopProcess(identity, timeout)
	}
	if err != nil && identity.Matches() {
		slog.Error("sidecar 停止失败，保留诊断", "pid", identity.PID, "reason", err.Error())
		m.mu.Lock()
		m.cmd, m.identity, m.waitDone = cmd, identity, done
		m.mu.Unlock()
		return err
	}
	select {
	case <-done:
	case <-time.After(timeout):
	}
	return nil
}

// Stop 先取消健康循环，再等待串行清理，重复或并发停止均安全。
func (m *Manager) Stop() {
	m.mu.Lock()
	m.stopping = true
	if m.cancel != nil {
		m.cancel()
	}
	done := m.loopDone
	m.mu.Unlock()
	m.opMu.Lock()
	m.stopProcess(false)
	m.opMu.Unlock()
	if done != nil {
		<-done
	}
}

func (m *Manager) Running() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running
}

func (m *Manager) URL() string { return fmt.Sprintf("http://127.0.0.1:%d", m.cfg.Port) }
