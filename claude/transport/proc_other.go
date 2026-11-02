//go:build !unix

package transport

import (
	"os"
	"os/exec"
	"syscall"
)

func setProcAttr(cmd *exec.Cmd) {}

// On platforms without process groups only the leader is signalled.
func signalGroup(pid int, sig syscall.Signal) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}

func groupAlive(pid int) bool { return false }

func sigTerm() syscall.Signal { return syscall.SIGTERM }
func sigKill() syscall.Signal { return syscall.SIGKILL }

func exitInfo(ps *os.ProcessState) (int, string) { return ps.ExitCode(), "" }
