//go:build linux || darwin

package lifecycle

import (
	"context"
	"golang.org/x/sys/unix"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

func prepareProcess(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }
func signalProcess(i Identity, force bool) error {
	signal := syscall.SIGTERM
	if force {
		signal = syscall.SIGKILL
	}
	// 仅对核对身份的独立组发送信号，不使用进程名或监听端口。
	if i.ProcessGroup == i.PID {
		return syscall.Kill(-i.PID, signal)
	}
	return syscall.Kill(i.PID, signal)
}
func lockFile(f *os.File) error { return unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB) }
func EnterJob() (func(), error) { return func() {}, nil }

func ShutdownContext() (context.Context, func(), error) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	return ctx, cancel, nil
}
