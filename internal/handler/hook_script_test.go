package handler

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHookScript_ClassifiesFailOpenOutcomes(t *testing.T) {
	root := testRepositoryRoot(t)
	script, err := os.ReadFile(filepath.Join(root, ".claude", "hook.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(script), "--max-time 3") {
		t.Fatal("primary hook request must retain its three-second curl limit")
	}

	for _, tc := range []struct {
		name       string
		curlStatus string
		body       string
		wantOutput string
		wantReport string
	}{
		{"injected", "0", `{"additional_context":"SENTINEL_CONTEXT"}`, "SENTINEL_CONTEXT", ""},
		{"no_results", "0", `{"additional_context":""}`, "", ""},
		{"timeout", "28", "", "", `"outcome":"timeout"`},
		{"service_unavailable", "22", "", "", `"outcome":"service_unavailable"`},
		{"malformed_response", "0", `{`, "", `"outcome":"invalid_response"`},
		{"missing_context", "0", `{}`, "", `"reason_code":"missing_context_field"`},
		{"non_string_context", "0", `{"additional_context":3}`, "", `"reason_code":"non_string_context"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output, reports, elapsed := runHookScript(t, root, filepath.Join(root, ".claude", "hook.sh"), tc.curlStatus, tc.body, "")
			if output != tc.wantOutput {
				t.Fatalf("stdout = %q, want %q", output, tc.wantOutput)
			}
			if elapsed > 3*time.Second {
				t.Fatalf("hook exceeded its primary request budget: %s", elapsed)
			}
			if tc.wantReport == "" {
				if reports != "" {
					t.Fatalf("unexpected client report: %q", reports)
				}
				return
			}
			if !strings.Contains(reports, tc.wantReport) {
				t.Fatalf("missing report %q in %q", tc.wantReport, reports)
			}
			if strings.Contains(output, "SENTINEL_PROMPT") || strings.Contains(output, "SENTINEL_TRANSCRIPT") {
				t.Fatalf("fail-open stdout leaked input: %q", output)
			}
		})
	}
}

func TestCodexHookScript_ClassifiesFailOpenOutcomes(t *testing.T) {
	root := testRepositoryRoot(t)
	script := filepath.Join(root, ".codex", "hooks", "rag-user-prompt-submit.sh")
	data, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "--max-time 3") {
		t.Fatal("primary hook request must retain its three-second curl limit")
	}

	for _, tc := range []struct {
		name        string
		curlStatus  string
		body        string
		wantContext string
		wantReport  string
	}{
		{"injected", "0", `{"additional_context":"SENTINEL_CONTEXT"}`, "SENTINEL_CONTEXT", ""},
		{"no_results", "0", `{"additional_context":""}`, "", ""},
		{"timeout", "28", "", "", `"outcome":"timeout"`},
		{"service_unavailable", "22", "", "", `"outcome":"service_unavailable"`},
		{"malformed_response", "0", `{`, "", `"outcome":"invalid_response"`},
		{"missing_context", "0", `{}`, "", `"reason_code":"missing_context_field"`},
		{"non_string_context", "0", `{"additional_context":3}`, "", `"reason_code":"non_string_context"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output, reports, elapsed := runHookScript(t, root, script, tc.curlStatus, tc.body, "")
			if elapsed > 3*time.Second {
				t.Fatalf("hook exceeded its primary request budget: %s", elapsed)
			}

			if tc.wantContext == "" {
				if output != "" {
					t.Fatalf("stdout = %q, want no additional context", output)
				}
			} else {
				var response struct {
					HookSpecificOutput struct {
						HookEventName     string `json:"hookEventName"`
						AdditionalContext string `json:"additionalContext"`
					} `json:"hookSpecificOutput"`
				}
				if err := json.Unmarshal([]byte(output), &response); err != nil {
					t.Fatalf("stdout is not valid Codex hook JSON: %v: %q", err, output)
				}
				if response.HookSpecificOutput.HookEventName != "UserPromptSubmit" || response.HookSpecificOutput.AdditionalContext != tc.wantContext {
					t.Fatalf("unexpected hook output: %#v", response.HookSpecificOutput)
				}
			}

			if tc.wantReport == "" {
				if reports != "" {
					t.Fatalf("unexpected client report: %q", reports)
				}
				return
			}
			if !strings.Contains(reports, tc.wantReport) {
				t.Fatalf("missing report %q in %q", tc.wantReport, reports)
			}
		})
	}
}

func TestHookScript_TelemetryFailureDoesNotDelayOrChangeFailOpenResult(t *testing.T) {
	root := testRepositoryRoot(t)
	output, reports, elapsed := runHookScript(t, root, filepath.Join(root, ".claude", "hook.sh"), "28", "", "2")
	if output != "" {
		t.Fatalf("timeout must not print stdout: %q", output)
	}
	if elapsed >= time.Second {
		t.Fatalf("background telemetry delayed hook completion: %s", elapsed)
	}
	if reports != "" {
		t.Fatalf("failed telemetry must remain silent: %q", reports)
	}
}

func runHookScript(t *testing.T, root, script, curlStatus, body, reportSleep string) (string, string, time.Duration) {
	t.Helper()
	temp := t.TempDir()
	project := filepath.Join(temp, "project")
	if err := os.Mkdir(project, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, ".rag-mode"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	reportLog := filepath.Join(temp, "reports")
	bin := filepath.Join(temp, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	const mockCurl = `#!/bin/bash
if [[ "$*" == *"/hook/outcome"* ]]; then
  if [ -n "$MOCK_REPORT_SLEEP" ]; then sleep "$MOCK_REPORT_SLEEP"; fi
  printf '%s' "$*" >> "$MOCK_REPORT_LOG"
  exit 1
fi
cat >/dev/null
printf '%s' "$MOCK_CURL_BODY"
exit "$MOCK_CURL_STATUS"
`
	if err := os.WriteFile(filepath.Join(bin, "curl"), []byte(mockCurl), 0o700); err != nil {
		t.Fatal(err)
	}
	payload := `{"prompt":"SENTINEL_PROMPT","transcript_path":"SENTINEL_TRANSCRIPT","cwd":"` + project + `"}`
	cmd := exec.Command("bash", script)
	cmd.Dir = root
	cmd.Stdin = strings.NewReader(payload)
	cmd.Env = append(os.Environ(), "PATH="+bin+":/bin:/usr/bin:/opt/homebrew/bin:"+os.Getenv("PATH"), "MOCK_CURL_STATUS="+curlStatus, "MOCK_CURL_BODY="+body, "MOCK_REPORT_LOG="+reportLog, "MOCK_REPORT_SLEEP="+reportSleep)
	started := time.Now()
	output, err := cmd.CombinedOutput()
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("hook script exited unsuccessfully: %v: %s", err, output)
	}
	if reportSleep == "" {
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			if data, readErr := os.ReadFile(reportLog); readErr == nil && len(data) > 0 {
				return string(output), string(data), elapsed
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	reports, _ := os.ReadFile(reportLog)
	return string(output), string(reports), elapsed
}

func testRepositoryRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}
