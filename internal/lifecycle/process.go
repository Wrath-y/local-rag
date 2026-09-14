package lifecycle

import (
	"fmt"
	"os"
	"os/exec"
	"time"
)

// Identity 同时记录内核启动时间和可执行路径，不能只凭 PID 终止进程。
type Identity struct {
	PID          int    `json:"pid"`
	StartTime    string `json:"start_time"`
	Executable   string `json:"executable"`
	ProcessGroup int    `json:"process_group"`
}

func (i Identity) Matches() bool {
	current, err := Inspect(i.PID)
	return err == nil && i.PID > 0 && i.StartTime != "" && i.Executable != "" && current.StartTime == i.StartTime && current.Executable == i.Executable && current.ProcessGroup == i.ProcessGroup
}

// StartProcess 创建专属进程组；调用方只管理自己启动的进程。
func StartProcess(cmd *exec.Cmd) (Identity, error) {
	prepareProcess(cmd)
	if err := cmd.Start(); err != nil {
		return Identity{}, err
	}
	i, err := Inspect(cmd.Process.Pid)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return i, err
	}
	// macOS 的 Python framework 启动器会再次 exec；稳定后才落盘实际映像身份。
	stable := 0
	for attempts := 0; attempts < 30 && stable < 3; attempts++ {
		time.Sleep(50 * time.Millisecond)
		current, inspectErr := Inspect(cmd.Process.Pid)
		if inspectErr != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return Identity{}, fmt.Errorf("子进程在身份确认前退出：%w", inspectErr)
		}
		if current == i {
			stable++
		} else {
			stable = 0
			i = current
		}
	}
	if stable < 3 {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return Identity{}, fmt.Errorf("子进程映像身份无法稳定确认")
	}
	return i, nil
}

// Terminate 每次发送信号前重新核对身份，保护陈旧 PID 和 PID 复用。
func Terminate(i Identity, force bool) error {
	if !i.Matches() {
		return fmt.Errorf("进程身份已变化，保留 PID %d", i.PID)
	}
	return signalProcess(i, force)
}

func StopProcess(i Identity, timeout time.Duration) error {
	if !i.Matches() {
		return nil
	}
	// 先快照受管组的成员身份；父进程先退出时仍能核对并回收拒绝退出的子进程。
	members, err := groupMembers(i)
	if err != nil {
		return fmt.Errorf("无法核验子进程归属：%w", err)
	}
	pending := func() bool {
		if i.Matches() {
			return true
		}
		for _, member := range members {
			if member.Matches() {
				return true
			}
		}
		return false
	}
	if err := Terminate(i, false); err != nil {
		return err
	}
	deadline := time.Now().Add(timeout)
	for pending() && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if pending() {
		if i.Matches() {
			if err := Terminate(i, true); err != nil {
				return err
			}
		}
		for _, member := range members {
			if member.Matches() {
				if err := Terminate(member, true); err != nil {
					return err
				}
			}
		}
		deadline = time.Now().Add(3 * time.Second)
		for pending() && time.Now().Before(deadline) {
			time.Sleep(100 * time.Millisecond)
		}
	}
	if pending() {
		return fmt.Errorf("进程 %d 未能停止", i.PID)
	}
	return nil
}

func lock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := lockFile(f); err != nil {
		f.Close()
		return nil, fmt.Errorf("另一个生命周期命令正在执行：%w", err)
	}
	return f, nil
}
