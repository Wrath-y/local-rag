package lifecycle

import (
	"context"
	"fmt"
	"golang.org/x/sys/windows"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"unsafe"
)

func prepareProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
}
func Inspect(pid int) (Identity, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return Identity{}, err
	}
	defer windows.CloseHandle(h)
	return inspectWindowsHandle(h, pid)
}

func inspectWindowsHandle(h windows.Handle, pid int) (Identity, error) {
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &creation, &exit, &kernel, &user); err != nil {
		return Identity{}, err
	}
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil || code != 259 {
		return Identity{}, fmt.Errorf("进程已退出")
	}
	path := make([]uint16, 32768)
	size := uint32(len(path))
	if err := windows.QueryFullProcessImageName(h, 0, &path[0], &size); err != nil {
		return Identity{}, err
	}
	return Identity{PID: pid, StartTime: fmt.Sprintf("%d:%d", creation.HighDateTime, creation.LowDateTime), Executable: windows.UTF16ToString(path[:size]), ProcessGroup: pid}, nil
}
func signalProcess(i Identity, force bool) error {
	if !force {
		// 命名事件允许从另一个终端优雅停止；没有控制台时也不退化为直接强杀。
		name, _ := windows.UTF16PtrFromString(stopEventName(i))
		if event, err := windows.OpenEvent(windows.EVENT_MODIFY_STATE, false, name); err == nil {
			defer windows.CloseHandle(event)
			return windows.SetEvent(event)
		}
		return windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(i.PID))
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE, false, uint32(i.PID))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	current, err := inspectWindowsHandle(h, i.PID)
	if err != nil || current != i {
		return fmt.Errorf("终止句柄的进程身份不匹配，保留 PID %d", i.PID)
	}
	// 主进程持有的 Job Object 在退出时关闭，内核负责回收其后代。
	return windows.TerminateProcess(h, 1)
}

func stopEventName(i Identity) string {
	return fmt.Sprintf("Local\\local-rag-stop-%d-%s", i.PID, strings.ReplaceAll(i.StartTime, ":", "-"))
}

func ShutdownContext() (context.Context, func(), error) {
	i, err := Inspect(os.Getpid())
	if err != nil {
		return nil, nil, err
	}
	name, _ := windows.UTF16PtrFromString(stopEventName(i))
	event, err := windows.CreateEvent(nil, 0, 0, name)
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	go func() {
		defer windows.CloseHandle(event)
		for {
			state, err := windows.WaitForSingleObject(event, 200)
			if err != nil || state == windows.WAIT_OBJECT_0 {
				cancel()
				return
			}
			select {
			case <-ctx.Done():
				return
			default:
			}
		}
	}()
	return ctx, cancel, nil
}

func groupMembers(parent Identity) ([]Identity, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	parents := map[uint32]uint32{}
	for err = windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		parents[entry.ProcessID] = entry.ParentProcessID
	}
	owned := map[uint32]Identity{uint32(parent.PID): parent}
	members := []Identity{}
	for changed := true; changed; {
		changed = false
		for pid, ppid := range parents {
			_, known := owned[pid]
			ancestor, ancestorKnown := owned[ppid]
			if !known && ancestorKnown {
				if member, err := Inspect(int(pid)); err == nil && windowsCreationTime(member.StartTime) >= windowsCreationTime(ancestor.StartTime) {
					// 旧孤儿仍可能保留已复用的父 PID；创建时间早于祖先时不能认领。
					owned[pid] = member
					members = append(members, member)
					changed = true
				}
			}
		}
	}
	return members, nil
}

func windowsCreationTime(value string) uint64 {
	parts := strings.SplitN(value, ":", 2)
	if len(parts) != 2 {
		return 0
	}
	high, _ := strconv.ParseUint(parts[0], 10, 32)
	low, _ := strconv.ParseUint(parts[1], 10, 32)
	return high<<32 | low
}
func lockFile(f *os.File) error {
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &windows.Overlapped{})
}

// EnterJob 在服务进程创建 sidecar 前绑定自身，父进程异常退出同样回收后代。
func EnterJob() (func(), error) {
	h, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(h, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		windows.CloseHandle(h)
		return nil, err
	}
	if err := windows.AssignProcessToJobObject(h, windows.CurrentProcess()); err != nil {
		windows.CloseHandle(h)
		return nil, err
	}
	// 保持句柄到进程退出，避免正常返回时关闭 Job 导致本进程被强杀。
	return func() {}, nil
}
