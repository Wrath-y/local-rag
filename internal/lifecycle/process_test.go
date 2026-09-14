package lifecycle

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func TestProcessGroupHelper(t *testing.T) {
	mode := os.Getenv("LIFECYCLE_HELPER_MODE")
	if mode == "" {
		return
	}
	ch := make(chan os.Signal, 2)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	if mode == "child" {
		_ = os.WriteFile(os.Getenv("LIFECYCLE_CHILD_READY"), []byte(strconv.Itoa(os.Getpid())), 0600)
		for range ch {
		}
		return
	}
	child := exec.Command(os.Args[0], "-test.run=TestProcessGroupHelper")
	child.Env = append(os.Environ(), "LIFECYCLE_HELPER_MODE=child")
	if err := child.Start(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	<-ch
	os.Exit(0)
}

func TestStopReclaimsChildAfterParentExits(t *testing.T) {
	ready := filepath.Join(t.TempDir(), "ready")
	cmd := exec.Command(os.Args[0], "-test.run=TestProcessGroupHelper")
	cmd.Env = append(os.Environ(), "LIFECYCLE_HELPER_MODE=parent", "LIFECYCLE_CHILD_READY="+ready)
	identity, err := StartProcess(cmd)
	if err != nil {
		t.Fatal(err)
	}
	go cmd.Wait()
	t.Cleanup(func() {
		if identity.Matches() {
			_ = Terminate(identity, true)
		}
	})
	deadline := time.Now().Add(5 * time.Second)
	var child Identity
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(ready)
		if err == nil {
			pid, _ := strconv.Atoi(string(data))
			child, err = Inspect(pid)
			if err == nil {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if child.PID == 0 {
		t.Fatal("子进程未就绪")
	}
	t.Cleanup(func() {
		if child.Matches() {
			_ = Terminate(child, true)
		}
	})
	if err := StopProcess(identity, 300*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if identity.Matches() || child.Matches() {
		t.Fatal("父或子进程仍运行")
	}
}
