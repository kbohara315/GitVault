//go:build linux

package backup

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"
)

func processOwnershipSupported() error { return nil }
func configureOwnedProcess(cmd *exec.Cmd, suspend bool) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Ptrace: suspend}
}

// A traced child stops after exec and before its first instruction. Registering
// the broker against that process-group leader is therefore atomic with respect
// to Git starting a network request or spawning a credential helper.
func waitOwnedStart(cmd *exec.Cmd) error {
	var status unix.WaitStatus
	for {
		_, err := unix.Wait4(cmd.Process.Pid, &status, unix.WUNTRACED, nil)
		if err == unix.EINTR {
			continue
		}
		if err != nil || !status.Stopped() || status.StopSignal() != unix.SIGTRAP {
			return ErrGitUnsupported
		}
		return nil
	}
}

func releaseOwnedStart(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return ErrGitUnsupported
	}
	// PTRACE_CONT would keep tracing signal-delivery stops (notably SIGCHLD
	// from Git's transport helper). Detach at the exec trap and deliver no signal
	// so the registered child proceeds as an ordinary owned process-group leader.
	if err := unix.PtraceDetach(cmd.Process.Pid); err != nil {
		return ErrGitUnsupported
	}
	return nil
}

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
