package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Wrath-y/local-rag/internal/config"
	"github.com/google/uuid"
)

// Instance 是可供编排器读取的脱敏状态，不保存配置内容或密钥。
type Instance struct {
	SchemaVersion string `json:"schema_version"`
	Service       string `json:"service"`
	Identity
	Ownership      string `json:"ownership"`
	State          string `json:"state"`
	HealthURL      string `json:"health_url"`
	LogPath        string `json:"log_path"`
	StartedThisRun bool   `json:"started_this_run"`
	InstanceID     string `json:"instance_id,omitempty"`
}

type options struct {
	config, listen, pidDir, logDir, workdir string
	background, foreground                  bool
	healthTimeout, stopTimeout              time.Duration
}

func parseOptions(args []string, out io.Writer) (options, error) {
	o := options{}
	f := flag.NewFlagSet("lifecycle", flag.ContinueOnError)
	f.SetOutput(out)
	f.StringVar(&o.config, "config", envDefault("RAG_CONFIG", "config.yaml"), "配置文件路径")
	f.StringVar(&o.listen, "listen", "", "监听地址，例如 127.0.0.1:8765")
	f.StringVar(&o.workdir, "workdir", ".", "服务工作目录")
	f.StringVar(&o.pidDir, "pid-dir", ".run", "进程身份与锁目录")
	f.StringVar(&o.logDir, "log-dir", "logs", "日志目录")
	f.BoolVar(&o.background, "background", false, "明确后台运行")
	f.BoolVar(&o.foreground, "foreground", false, "前台运行（默认）")
	f.DurationVar(&o.healthTimeout, "health-timeout", 30*time.Second, "启动健康超时")
	f.DurationVar(&o.stopTimeout, "stop-timeout", 20*time.Second, "优雅停止超时")
	if err := f.Parse(args); err != nil {
		return o, err
	}
	if f.NArg() != 0 || (o.foreground && o.background) || o.healthTimeout <= 0 || o.stopTimeout <= 0 {
		return o, fmt.Errorf("参数冲突、未知参数或超时无效")
	}
	var err error
	for _, path := range []*string{&o.config, &o.workdir, &o.pidDir, &o.logDir} {
		*path, err = filepath.Abs(*path)
		if err != nil {
			return o, err
		}
	}
	return o, nil
}

func envDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func readInstance(path string) (Instance, error) {
	i := Instance{}
	data, err := os.ReadFile(path)
	if err != nil {
		return i, err
	}
	err = json.Unmarshal(data, &i)
	if err == nil && (i.SchemaVersion != "1.0" || i.Service != "local-rag") {
		err = fmt.Errorf("实例身份文件格式不匹配；不会操作其中 PID")
	}
	return i, err
}
func saveInstance(path string, i Instance) error {
	data, err := json.MarshalIndent(i, "", "  ")
	if err != nil {
		return err
	}
	if err = os.WriteFile(path+".tmp", append(data, '\n'), 0600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}
func printInstance(out io.Writer, i Instance) error { return json.NewEncoder(out).Encode(i) }

// Probe 校验 HTTP 状态、服务身份、协议与核心能力，拒绝返回 200 的无关程序。
func Probe(ctx context.Context, url string) error {
	return probeInstance(ctx, url, "")
}

func probeInstance(ctx context.Context, url, instanceID string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	client := http.Client{Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if instanceID != "" && resp.Header.Get("X-Local-RAG-Instance") != instanceID {
		return fmt.Errorf("健康响应不属于本轮启动的实例")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("健康检查 HTTP %d", resp.StatusCode)
	}
	var doc struct {
		Service      string `json:"service"`
		Schema       string `json:"schema_version"`
		Status       string `json:"status"`
		Capabilities []struct {
			Name  string `json:"name"`
			State string `json:"state"`
		} `json:"capabilities"`
		Dependencies []struct {
			Name  string `json:"name"`
			State string `json:"state"`
		} `json:"dependencies"`
	}
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 1<<20))
	if err = decoder.Decode(&doc); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return fmt.Errorf("健康响应包含额外内容")
	}
	if doc.Service != "local-rag" || doc.Schema != "1.0" || (doc.Status != "ok" && doc.Status != "degraded") {
		return fmt.Errorf("健康服务身份、协议或状态不匹配")
	}
	capabilities := map[string]string{}
	for _, c := range doc.Capabilities {
		capabilities[c.Name] = c.State
	}
	for _, name := range []string{"snapshot_lifecycle", "traverse", "paths", "task_polling", "bm25", "vector"} {
		if capabilities[name] != "available" {
			return fmt.Errorf("核心能力 %s 不可用", name)
		}
	}
	dependencies := map[string]string{}
	for _, d := range doc.Dependencies {
		dependencies[d.Name] = d.State
	}
	for _, name := range []string{"sqlite", "graph_migrations", "graph_worker", "core_graph_query"} {
		if dependencies[name] != "available" {
			return fmt.Errorf("核心依赖 %s 不可用", name)
		}
	}
	return nil
}

func healthAddress(listen string) (string, error) {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "", err
	}
	if host == "" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	if host == "::" {
		host = "::1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/health", nil
}

// Run 执行受锁保护的生命周期命令；错误始终交由入口返回非零状态。
func Run(args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("需要 start、stop 或 status 子命令")
	}
	action := args[0]
	if action != "start" && action != "stop" && action != "status" {
		return fmt.Errorf("未知命令 %q", action)
	}
	o, err := parseOptions(args[1:], out)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(o.pidDir, 0700); err != nil {
		return err
	}
	guard, err := lock(filepath.Join(o.pidDir, "lifecycle.lock"))
	if err != nil {
		return err
	}
	defer guard.Close()
	statePath := filepath.Join(o.pidDir, "instance.json")
	i, readErr := readInstance(statePath)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return fmt.Errorf("无法读取实例身份：%w", readErr)
	}
	if action == "status" {
		if errors.Is(readErr, os.ErrNotExist) {
			i = Instance{SchemaVersion: "1.0", Service: "local-rag", State: "stopped"}
		}
		if i.Ownership == "managed" && !i.Matches() {
			i.State = "stale"
		}
		if i.State != "stopped" && i.State != "stale" {
			if err := probeInstance(context.Background(), i.HealthURL, i.InstanceID); err != nil {
				i.State = "unhealthy"
			} else {
				i.State = "running"
			}
		}
		return printInstance(out, i)
	}
	if action == "stop" {
		if errors.Is(readErr, os.ErrNotExist) {
			return printInstance(out, Instance{SchemaVersion: "1.0", Service: "local-rag", State: "stopped"})
		}
		if i.Ownership == "external" {
			return printInstance(out, i)
		}
		if !i.Matches() {
			if _, err := Inspect(i.PID); err != nil {
				i.State = "stopped"
				_ = os.Remove(statePath)
				return printInstance(out, i)
			}
			i.State = "stale"
			_ = printInstance(out, i)
			return fmt.Errorf("进程身份不匹配，未终止任何进程；请核查 %s", statePath)
		}
		if err := StopProcess(i.Identity, o.stopTimeout); err != nil {
			return err
		}
		i.State = "stopped"
		if err := os.Remove(statePath); err != nil {
			return err
		}
		return printInstance(out, i)
	}
	cfg, err := config.Load(o.config)
	if err != nil {
		return fmt.Errorf("配置读取失败：%w", err)
	}
	if o.listen == "" {
		o.listen = fmt.Sprintf("127.0.0.1:%d", cfg.Server.Port)
	}
	healthURL, err := healthAddress(o.listen)
	if err != nil {
		return fmt.Errorf("监听地址无效：%w", err)
	}
	if readErr == nil && i.Ownership == "managed" && i.Matches() {
		if i.HealthURL != healthURL {
			return fmt.Errorf("已有受管实例使用另一监听地址；请先显式停止")
		}
		if err := probeInstance(context.Background(), healthURL, i.InstanceID); err != nil {
			return fmt.Errorf("已有实例健康异常，保留原进程：%w", err)
		}
		i.StartedThisRun = false
		return printInstance(out, i)
	}
	if err := Probe(context.Background(), healthURL); err == nil {
		i = Instance{SchemaVersion: "1.0", Service: "local-rag", Ownership: "external", State: "running", HealthURL: healthURL}
		if err := saveInstance(statePath, i); err != nil {
			return err
		}
		return printInstance(out, i)
	}
	listener, err := net.Listen("tcp", o.listen)
	if err != nil {
		return fmt.Errorf("监听地址被占用且服务身份或健康不匹配：%w", err)
	}
	listener.Close()
	if err = os.MkdirAll(o.logDir, 0750); err != nil {
		return err
	}
	logPath := filepath.Join(o.logDir, "rag-server-"+time.Now().Format("20060102")+".log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer logFile.Close()
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(executable, "serve", "--config", o.config, "--listen", o.listen)
	cmd.Dir = o.workdir
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	instanceID := uuid.NewString()
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "RAG_INSTANCE_ID=") {
			cmd.Env = append(cmd.Env, value)
		}
	}
	cmd.Env = append(cmd.Env, "RAG_INSTANCE_ID="+instanceID)
	signalContext, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	identity, err := StartProcess(cmd)
	if err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	i = Instance{SchemaVersion: "1.0", Service: "local-rag", Identity: identity, Ownership: "managed", State: "starting", HealthURL: healthURL, LogPath: logPath, StartedThisRun: true}
	i.InstanceID = instanceID
	if err = saveInstance(statePath, i); err != nil {
		_ = StopProcess(identity, o.stopTimeout)
		return err
	}
	ctx, cancel := context.WithTimeout(signalContext, o.healthTimeout)
	defer cancel()
	var healthErr error
	for {
		if !identity.Matches() {
			healthErr = fmt.Errorf("服务提前退出，请查看 %s", logPath)
			break
		}
		healthErr = probeInstance(ctx, healthURL, instanceID)
		if healthErr == nil {
			break
		}
		select {
		case <-ctx.Done():
			healthErr = fmt.Errorf("健康检查超时：%w；日志 %s", healthErr, logPath)
			goto finished
		case <-time.After(150 * time.Millisecond):
		}
	}
finished:
	if healthErr != nil {
		stopErr := StopProcess(identity, o.stopTimeout)
		i.State = "failed"
		_ = saveInstance(statePath, i)
		return errors.Join(healthErr, stopErr)
	}
	i.State = "running"
	if err := saveInstance(statePath, i); err != nil {
		_ = StopProcess(identity, o.stopTimeout)
		return err
	}
	if err := printInstance(out, i); err != nil {
		return err
	}
	if o.background {
		return nil
	}
	guard.Close()
	select {
	case <-signalContext.Done():
		err = StopProcess(identity, o.stopTimeout)
	case err = <-done:
	}
	// 前台结束时重新获取锁，避免删除其他命令刚写入的新实例。
	if cleanupLock, lockErr := lock(filepath.Join(o.pidDir, "lifecycle.lock")); lockErr == nil {
		defer cleanupLock.Close()
		current, e := readInstance(statePath)
		if e == nil && current.Identity == identity && !identity.Matches() {
			_ = os.Remove(statePath)
		}
	}
	if err != nil && !strings.Contains(err.Error(), "signal: terminated") {
		return err
	}
	return nil
}
