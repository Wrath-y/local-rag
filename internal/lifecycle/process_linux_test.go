package lifecycle

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

func TestInspectLinuxIncludesBootIdentity(t *testing.T) {
	pid := os.Getpid()
	identity, err := Inspect(pid)
	if err != nil {
		t.Fatal(err)
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		t.Fatal(err)
	}
	stat, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(stat)[strings.LastIndexByte(string(stat), ')')+1:])
	if len(fields) < 20 {
		t.Fatal("内核进程状态缺少启动时间")
	}
	bootID := strings.TrimSpace(string(boot))
	if bootID == "" {
		t.Fatal("内核启动标识为空")
	}
	wantStart := bootID + ":" + fields[19]
	if identity.StartTime != wantStart {
		t.Fatalf("启动身份 = %q，期望 %q", identity.StartTime, wantStart)
	}
	executable, err := os.Readlink(filepath.Join("/proc", strconv.Itoa(pid), "exe"))
	if err != nil {
		t.Fatal(err)
	}
	group, err := syscall.Getpgid(pid)
	if err != nil {
		t.Fatal(err)
	}
	if identity.PID != pid || identity.Executable != executable || identity.ProcessGroup != group || !identity.Matches() {
		t.Fatalf("自进程身份与内核不一致：%+v", identity)
	}
	for _, staleStart := range []string{fields[19], "another-boot:" + fields[19]} {
		stale := identity
		stale.StartTime = staleStart
		if stale.Matches() {
			t.Fatalf("陈旧启动身份 %q 不应匹配当前进程", staleStart)
		}
	}
}

func TestInspectLinuxRejectsUnavailableBootIdentity(t *testing.T) {
	// 仅替换 proc 根目录，保留真实自进程 stat 和 exe；证明缺少 boot ID 时
	// 不会退回仅依赖 tick 的身份，即使其余进程身份字段均有效。
	pid := os.Getpid()
	processDir := strconv.Itoa(pid)
	stat, err := os.ReadFile(filepath.Join("/proc", processDir, "stat"))
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Readlink(filepath.Join("/proc", processDir, "exe"))
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct {
		name    string
		present bool
		bootID  string
	}{
		{name: "缺失"},
		{name: "空文件", present: true},
		{name: "仅空白", present: true, bootID: " \n\t"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			procRoot := t.TempDir()
			processPath := filepath.Join(procRoot, processDir)
			if err := os.MkdirAll(processPath, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(processPath, "stat"), stat, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(executable, filepath.Join(processPath, "exe")); err != nil {
				t.Fatal(err)
			}
			if fixture.present {
				bootPath := filepath.Join(procRoot, "sys/kernel/random/boot_id")
				if err := os.MkdirAll(filepath.Dir(bootPath), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(bootPath, []byte(fixture.bootID), 0600); err != nil {
					t.Fatal(err)
				}
			}
			identity, err := inspectLinux(pid, procRoot)
			if err == nil || identity != (Identity{}) {
				t.Fatalf("缺少可用系统启动标识时应拒绝身份，得到 %+v，错误 %v", identity, err)
			}
		})
	}
}
