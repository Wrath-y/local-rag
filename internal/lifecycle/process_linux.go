package lifecycle

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func Inspect(pid int) (Identity, error) {
	return inspectLinux(pid, "/proc")
}

func inspectLinux(pid int, procRoot string) (Identity, error) {
	processPath := filepath.Join(procRoot, strconv.Itoa(pid))
	data, err := os.ReadFile(filepath.Join(processPath, "stat"))
	if err != nil {
		return Identity{}, err
	}
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 {
		return Identity{}, fmt.Errorf("进程状态无效")
	}
	fields := strings.Fields(string(data)[end+1:])
	if len(fields) < 20 || fields[0] == "Z" {
		return Identity{}, fmt.Errorf("进程已退出或身份不可读")
	}
	path, err := os.Readlink(filepath.Join(processPath, "exe"))
	if err != nil {
		return Identity{}, err
	}
	group, err := strconv.Atoi(fields[2])
	if err != nil {
		return Identity{}, fmt.Errorf("进程组身份无效：%w", err)
	}
	boot, err := os.ReadFile(filepath.Join(procRoot, "sys/kernel/random/boot_id"))
	if err != nil {
		return Identity{}, fmt.Errorf("无法读取系统启动标识：%w", err)
	}
	bootID := strings.TrimSpace(string(boot))
	if bootID == "" {
		return Identity{}, fmt.Errorf("系统启动标识为空，无法确认进程身份")
	}
	return Identity{PID: pid, StartTime: bootID + ":" + fields[19], Executable: path, ProcessGroup: group}, nil
}

func groupMembers(parent Identity) ([]Identity, error) {
	if parent.ProcessGroup != parent.PID {
		return nil, nil
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	members := []Identity{}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid == parent.PID {
			continue
		}
		if member, err := Inspect(pid); err == nil && member.ProcessGroup == parent.ProcessGroup {
			members = append(members, member)
		}
	}
	return members, nil
}
