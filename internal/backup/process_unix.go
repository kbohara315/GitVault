//go:build linux

package backup

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"
)

func processOwnershipSupported() error    { return nil }
func configureOwnedProcess(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }
func terminateOwnedProcess(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = unix.Kill(-cmd.Process.Pid, unix.SIGKILL)
	}
}

// Keep the leader's PID reserved until the final group kill. Calling Cmd.Wait
// first would reap it, allowing a stale process-group ID to be reused before
// cleanup. WNOWAIT observes exit without consuming the wait status.
func observeOwnedExit(cmd *exec.Cmd) error {
	var info unix.Siginfo
	for {
		err := unix.Waitid(unix.P_PID, cmd.Process.Pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if err == unix.EINTR {
			continue
		}
		return err
	}
}
