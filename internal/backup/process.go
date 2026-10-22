package backup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

var (
	ErrGitUnavailable     = errors.New("Git executable unavailable")
	ErrGitUnsupported     = errors.New("Git operation or process ownership unsupported")
	ErrGitOutputLimit     = errors.New("Git machine output limit exceeded")
	ErrUnsafeRepository   = errors.New("unsafe or unsupported Git repository")
	ErrGitTransportDenied = errors.New("Git transport denied")
)

// GitCommandError deliberately does not expose argv, the environment, raw
// stderr, or an operating-system error containing untrusted path/URL text.
type GitCommandError struct {
	ExitCode   int
	Diagnostic string
	cause      error
}

func (e *GitCommandError) Error() string {
	if e.cause != nil {
		return "controlled Git command failed: " + e.cause.Error()
	}
	return fmt.Sprintf("controlled Git command exited with status %d", e.ExitCode)
}
func (e *GitCommandError) Unwrap() error { return e.cause }

type processOutput struct {
	stdout     []byte
	diagnostic string
}

type ownedProcess struct {
	cmd    *exec.Cmd
	mu     sync.Mutex
	reaped bool
}

func (p *ownedProcess) terminate() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.reaped {
		terminateOwnedProcess(p.cmd)
	}
}
func (p *ownedProcess) wait() error {
	observeErr := observeOwnedExit(p.cmd)
	p.mu.Lock()
	defer p.mu.Unlock()
	terminateOwnedProcess(p.cmd)
	err := p.cmd.Wait()
	p.reaped = true
	if observeErr != nil {
		return ErrGitUnsupported
	}
	return err
}

type cappedOutput struct {
	mu       sync.Mutex
	data     bytes.Buffer
	limit    int
	overflow bool
	signal   chan<- struct{}
}

func (w *cappedOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(p)
	keep := min(n, w.limit-w.data.Len())
	_, _ = w.data.Write(p[:keep])
	if keep < n {
		w.overflow = true
		select {
		case w.signal <- struct{}{}:
		default:
		}
	}
	return n, nil
}

// Raw diagnostics have no safe general-purpose redaction rule: a Git error can
// contain any repository-controlled text. Count bounded bytes, retain none.
type suppressedDiagnostics struct {
	count     int
	limit     int
	truncated bool
}

func (w *suppressedDiagnostics) Write(p []byte) (int, error) {
	n := len(p)
	keep := min(n, w.limit-w.count)
	w.count += keep
	w.truncated = w.truncated || keep < n
	return n, nil
}
func (w *suppressedDiagnostics) summary() string {
	if w.count == 0 {
		return ""
	}
	if w.truncated {
		return fmt.Sprintf("Git diagnostic output suppressed (at least %d bytes; truncated)", w.count)
	}
	return fmt.Sprintf("Git diagnostic output suppressed (%d bytes)", w.count)
}

// runOwnedProcess is private: only fixed Git command builders and synthetic
// process-lifetime tests may call it. No shell or caller environment is added.
// Direct OS pipe ownership avoids Cmd.Wait waiting indefinitely for a grandchild
// that inherited stdout after the direct child has exited.
func runOwnedProcess(ctx context.Context, executable string, args, env []string, dir string, input []byte, o GitRunnerOptions) (processOutput, error) {
	var out processOutput
	if err := ctx.Err(); err != nil {
		return out, &GitCommandError{ExitCode: -1, cause: err}
	}
	if err := processOwnershipSupported(); err != nil {
		return out, &GitCommandError{ExitCode: -1, cause: err}
	}
	ctx, cancel := context.WithTimeout(ctx, o.CommandTimeout)
	defer cancel()
	cmd := exec.Command(executable, args...)
	cmd.Dir = dir
	cmd.Env = append([]string(nil), env...)
	cmd.WaitDelay = o.DrainTimeout
	if input != nil {
		cmd.Stdin = bytes.NewReader(input)
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		return out, &GitCommandError{ExitCode: -1, cause: ErrGitUnavailable}
	}
	defer stdoutR.Close()
	defer stdoutW.Close()
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		return out, &GitCommandError{ExitCode: -1, cause: ErrGitUnavailable}
	}
	defer stderrR.Close()
	defer stderrW.Close()
	cmd.Stdout = stdoutW
	cmd.Stderr = stderrW
	configureOwnedProcess(cmd)
	if err = cmd.Start(); err != nil {
		return out, &GitCommandError{ExitCode: -1, cause: ErrGitUnavailable}
	}
	_ = stdoutW.Close()
	_ = stderrW.Close()
	overflow := make(chan struct{}, 1)
	machine := &cappedOutput{limit: o.MachineOutputBytes, signal: overflow}
	diagnostic := &suppressedDiagnostics{limit: o.DiagnosticBytes}
	stdoutDone := make(chan struct{})
	stderrDone := make(chan struct{})
	go func() { defer close(stdoutDone); _, _ = io.Copy(machine, stdoutR) }()
	go func() { defer close(stderrDone); _, _ = io.Copy(diagnostic, stderrR) }()
	owner := &ownedProcess{cmd: cmd}
	waitDone := make(chan error, 1)
	go func() { waitDone <- owner.wait() }()
	var waitErr, cause error
	select {
	case waitErr = <-waitDone:
	case <-ctx.Done():
		cause = ctx.Err()
		owner.terminate()
		waitErr = <-waitDone
	case <-overflow:
		cause = ErrGitOutputLimit
		owner.terminate()
		waitErr = <-waitDone
	}
	// Also kill descendants after a successful early parent exit. This precedes
	// pipe drain and return; cancellation is not needed to clean that case up.
	// owner.wait has killed the group before reaping the reserved leader PID.
	drain := time.NewTimer(o.DrainTimeout)
	defer drain.Stop()
	for stdoutDone != nil || stderrDone != nil {
		select {
		case <-stdoutDone:
			stdoutDone = nil
		case <-stderrDone:
			stderrDone = nil
		case <-drain.C:
			_ = stdoutR.Close()
			_ = stderrR.Close()
			if stdoutDone != nil {
				<-stdoutDone
				stdoutDone = nil
			}
			if stderrDone != nil {
				<-stderrDone
				stderrDone = nil
			}
			if cause == nil {
				cause = ErrGitUnsupported
			}
		}
	}
	out.diagnostic = diagnostic.summary()
	if machine.overflow {
		cause = ErrGitOutputLimit
	}
	if cause == nil && ctx.Err() != nil {
		cause = ctx.Err()
	}
	if errors.Is(waitErr, ErrGitUnsupported) {
		cause = ErrGitUnsupported
	}
	if cause != nil {
		return out, &GitCommandError{ExitCode: cmd.ProcessState.ExitCode(), Diagnostic: out.diagnostic, cause: cause}
	}
	if waitErr != nil {
		return out, &GitCommandError{ExitCode: cmd.ProcessState.ExitCode(), Diagnostic: out.diagnostic}
	}
	out.stdout = append([]byte(nil), machine.data.Bytes()...)
	return out, nil
}
