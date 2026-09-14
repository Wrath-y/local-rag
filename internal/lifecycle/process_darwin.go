package lifecycle

import (
	"fmt"
	"golang.org/x/sys/unix"
	"os/exec"
	"strconv"
	"strings"
)

func Inspect(pid int) (Identity, error) {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return Identity{}, err
	}
	if info.Proc.P_stat == 5 {
		return Identity{}, fmt.Errorf("进程已退出")
	}
	path, err := exec.Command("/bin/ps", "-ww", "-p", strconv.Itoa(pid), "-o", "comm=").Output()
	if err != nil {
		return Identity{}, err
	}
	return Identity{PID: pid, StartTime: fmt.Sprintf("%d.%06d", info.Proc.P_starttime.Sec, info.Proc.P_starttime.Usec), Executable: strings.TrimSpace(string(path)), ProcessGroup: int(info.Eproc.Pgid)}, nil
}

func groupMembers(parent Identity) ([]Identity, error) {
	if parent.ProcessGroup != parent.PID {
		return nil, nil
	}
	processes, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, err
	}
	members := []Identity{}
	for _, process := range processes {
		if int(process.Eproc.Pgid) == parent.ProcessGroup && int(process.Proc.P_pid) != parent.PID {
			if member, err := Inspect(int(process.Proc.P_pid)); err == nil {
				members = append(members, member)
			}
		}
	}
	return members, nil
}
