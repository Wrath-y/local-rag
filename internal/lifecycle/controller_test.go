package lifecycle

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func healthFixture() map[string]any {
	caps := []map[string]string{}
	for _, name := range []string{"snapshot_lifecycle", "traverse", "paths", "task_polling", "bm25", "vector"} {
		caps = append(caps, map[string]string{"name": name, "state": "available"})
	}
	deps := []map[string]string{}
	for _, name := range []string{"sqlite", "graph_migrations", "graph_worker", "core_graph_query"} {
		deps = append(deps, map[string]string{"name": name, "state": "available"})
	}
	return map[string]any{"service": "local-rag", "schema_version": "1.0", "status": "ok", "capabilities": caps, "dependencies": deps}
}

func TestProbeRejectsFalseHealthAndChecksInstance(t *testing.T) {
	for _, name := range []string{"healthy", "wrong_service", "wrong_schema", "unavailable", "missing_capability", "failed_dependency", "http_error", "wrong_instance", "invalid_json"} {
		t.Run(name, func(t *testing.T) {
			doc := healthFixture()
			switch name {
			case "wrong_service":
				doc["service"] = "other"
			case "wrong_schema":
				doc["schema_version"] = "2"
			case "unavailable":
				doc["status"] = "unavailable"
			case "missing_capability":
				doc["capabilities"] = nil
			case "failed_dependency":
				doc["dependencies"] = nil
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if name != "wrong_instance" {
					w.Header().Set("X-Local-RAG-Instance", "expected")
				}
				if name == "http_error" {
					w.WriteHeader(503)
				}
				if name == "invalid_json" {
					w.Write([]byte("ok"))
					return
				}
				_ = json.NewEncoder(w).Encode(doc)
			}))
			defer server.Close()
			err := probeInstance(context.Background(), server.URL, "expected")
			if (err == nil) != (name == "healthy") {
				t.Fatalf("场景 %s，错误 %v", name, err)
			}
		})
	}
}

func TestExternalReuseStopAndStatusNeverTerminate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(healthFixture()) }))
	defer server.Close()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config with spaces.yaml")
	if err := os.WriteFile(cfg, []byte("embedding:\n  provider: custom\nrerank:\n  provider: disabled\n"), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"--config", cfg, "--pid-dir", dir, "--listen", strings.TrimPrefix(server.URL, "http://"), "--background"}
	for _, action := range []string{"start", "start", "stop", "stop", "status"} {
		var out bytes.Buffer
		if err := Run(append([]string{action}, args...), &out); err != nil {
			t.Fatal(err)
		}
		var state Instance
		if err := json.Unmarshal(out.Bytes(), &state); err != nil {
			t.Fatal(err)
		}
		if state.Ownership != "external" || state.PID != 0 {
			t.Fatalf("外部服务归属错误：%s", out.String())
		}
		if err := Probe(context.Background(), server.URL); err != nil {
			t.Fatal("外部服务被影响", err)
		}
	}
}

func TestIdentityMismatchAndStalePIDPreserveLiveProcess(t *testing.T) {
	self, err := Inspect(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if !self.Matches() {
		t.Fatal("当前进程身份不匹配")
	}
	for _, mutate := range []func(*Identity){func(i *Identity) { i.StartTime += "invalid" }, func(i *Identity) { i.Executable += "invalid" }} {
		wrong := self
		mutate(&wrong)
		if err := Terminate(wrong, true); err == nil {
			t.Fatal("允许终止错误身份")
		}
		dir := t.TempDir()
		if err := saveInstance(filepath.Join(dir, "instance.json"), Instance{SchemaVersion: "1.0", Service: "local-rag", Identity: wrong, Ownership: "managed"}); err != nil {
			t.Fatal(err)
		}
		if err := Run([]string{"stop", "--pid-dir", dir}, &bytes.Buffer{}); err == nil {
			t.Fatal("陈旧 PID 未报告诊断")
		}
		if !self.Matches() {
			t.Fatal("当前进程受影响")
		}
	}
}

func TestLifecycleLockAndMalformedStateFailClosed(t *testing.T) {
	dir := t.TempDir()
	guard, err := lock(filepath.Join(dir, "lifecycle.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if err := Run([]string{"status", "--pid-dir", dir}, &bytes.Buffer{}); err == nil {
		t.Fatal("并发命令未被锁拒绝")
	}
	guard.Close()
	if err := os.WriteFile(filepath.Join(dir, "instance.json"), []byte("1234"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Run([]string{"stop", "--pid-dir", dir}, &bytes.Buffer{}); err == nil {
		t.Fatal("旧 PID 文件不应被当作授权")
	}
}

func TestDefaultsPreserveDailyLogAndForeground(t *testing.T) {
	o, err := parseOptions(nil, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if o.background || filepath.Base(o.logDir) != "logs" {
		t.Fatalf("默认参数错误：%+v", o)
	}
	if _, err := parseOptions([]string{"--foreground", "--background"}, &bytes.Buffer{}); err == nil {
		t.Fatal("冲突参数未报错")
	}
}
