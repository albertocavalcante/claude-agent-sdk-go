//go:build unix

package transport

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func setProcAttr(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

// signalGroup sends sig to the process group led by pid.
func signalGroup(pid int, sig syscall.Signal) error { return syscall.Kill(-pid, sig) }

// groupAlive reports whether any process remains in the group led by pid.
func groupAlive(pid int) bool {
	err := syscall.Kill(-pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func sigTerm() syscall.Signal { return syscall.SIGTERM }
func sigKill() syscall.Signal { return syscall.SIGKILL }

// exitInfo maps a finished process to an exit code (-1 if signalled) and signal name.
func exitInfo(ps *os.ProcessState) (int, string) {
	ws, ok := ps.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() {
		return ps.ExitCode(), ""
	}
	switch ws.Signal() {
	case syscall.SIGKILL:
		return -1, "killed"
	case syscall.SIGTERM:
		return -1, "terminated"
	case syscall.SIGINT:
		return -1, "interrupt"
	default:
		return -1, ws.Signal().String()
	}
}
