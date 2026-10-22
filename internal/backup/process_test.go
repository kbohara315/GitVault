//go:build linux

package backup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func helperProcess(t *testing.T, ctx context.Context, o GitRunnerOptions, mode, root string) (processOutput, error) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return runOwnedProcess(ctx, executable, []string{"-test.run=^TestGitProcessHelper$", "--", "gitvault-helper", mode, root}, []string{"LANG=C"}, root, nil, o)
}

// Executed only in child test binaries; the ordinary top-level invocation also
// passes (never skips). No shell, inherited user environment or network is used.
func TestGitProcessHelper(t *testing.T) {
	var args []string
	for i, s := range os.Args {
		if s == "gitvault-helper" {
			args = os.Args[i+1:]
			break
		}
	}
	if args == nil {
		return
	}
	if len(args) != 2 {
		os.Exit(91)
	}
	mode, root := args[0], args[1]
	if mode == "flood" {
		for {
			if _, err := os.Stdout.Write(make([]byte, 8192)); err != nil {
				os.Exit(0)
			}
		}
	}
	if mode == "writer" {
		f, err := os.OpenFile(filepath.Join(root, "writes"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			os.Exit(93)
		}
		if _, err := f.WriteString("x"); err != nil {
			os.Exit(92)
		}
		if err := os.WriteFile(filepath.Join(root, "writer.pid"), []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
			os.Exit(92)
		}
		for {
			_, _ = f.WriteString("x")
			_, _ = os.Stdout.Write([]byte("x"))
			_, _ = os.Stderr.Write([]byte("synthetic diagnostic\n"))
			time.Sleep(5 * time.Millisecond)
		}
	}
	if mode != "tree" && mode != "early-exit" {
		os.Exit(94)
	}
	if err := os.WriteFile(filepath.Join(root, "leader.pid"), []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		os.Exit(98)
	}
	executable, err := os.Executable()
	if err != nil {
		os.Exit(95)
	}
	child := exec.Command(executable, "-test.run=^TestGitProcessHelper$", "--", "gitvault-helper", "writer", root)
	child.Env = []string{"LANG=C"}
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	if err := child.Start(); err != nil {
		os.Exit(96)
	}
	if mode == "early-exit" {
		for i := 0; i < 1000; i++ {
			if _, err := os.Stat(filepath.Join(root, "writer.pid")); err == nil {
				os.Exit(0)
			}
			time.Sleep(time.Millisecond)
		}
		os.Exit(97)
	}
	_ = child.Wait()
	os.Exit(0)
}

func waitForWriter(t *testing.T, root string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(filepath.Join(root, "writes"))
		_, pidErr := os.Stat(filepath.Join(root, "writer.pid"))
		if err == nil && len(b) > 0 && pidErr == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("descendant fixture never started writing")
}
func assertWriterStopped(t *testing.T, root string) {
	t.Helper()
	leader, err := os.ReadFile(filepath.Join(root, "leader.pid"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join("/proc", string(leader), "stat")); !os.IsNotExist(err) && !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("direct child was not reaped before return: %v", err)
	}
	before, err := os.ReadFile(filepath.Join(root, "writes"))
	if err != nil || len(before) == 0 {
		t.Fatalf("no real writer evidence: %q %v", before, err)
	}
	pidBytes, err := os.ReadFile(filepath.Join(root, "writer.pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(pidBytes))
	if err != nil {
		t.Fatal(err)
	}
	// A killed descendant may remain an orphan zombie until the host's PID 1
	// reaps it. It must not be runnable or hold the inherited stdout pipe.
	for i := 0; i < 50; i++ {
		stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if os.IsNotExist(err) || errors.Is(err, syscall.ESRCH) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		_, tail, ok := strings.Cut(string(stat), ") ")
		if ok && strings.HasPrefix(tail, "Z ") {
			break
		}
		if i == 49 {
			t.Fatalf("descendant still alive after return: %s", stat)
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(75 * time.Millisecond)
	after, err := os.ReadFile(filepath.Join(root, "writes"))
	if err != nil || string(before) != string(after) {
		t.Fatalf("descendant kept writing after return: %d -> %d, %v", len(before), len(after), err)
	}
}

func TestGitCancellation(t *testing.T) {
	for _, mode := range []string{"tree", "early-exit"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			o := DefaultGitRunnerOptions()
			o.CommandTimeout = 10 * time.Second
			o.DrainTimeout = 200 * time.Millisecond
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type result struct {
				out processOutput
				err error
			}
			done := make(chan result, 1)
			go func() { out, err := helperProcess(t, ctx, o, mode, root); done <- result{out, err} }()
			if mode == "tree" {
				waitForWriter(t, root)
				cancel()
			}
			select {
			case got := <-done:
				if mode == "tree" && !errors.Is(got.err, context.Canceled) {
					t.Fatalf("wrong cancel result: %v", got.err)
				}
				if mode == "early-exit" && got.err != nil {
					t.Fatalf("early parent exit hung or failed: %v", got.err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("inherited stdout/stderr prevented bounded return")
			}
			assertWriterStopped(t, root)
		})
	}
}

func TestGitDeadline(t *testing.T) {
	for _, kind := range []string{"caller-context", "runner-budget"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			o := DefaultGitRunnerOptions()
			o.CommandTimeout = 5 * time.Second
			o.DrainTimeout = 200 * time.Millisecond
			ctx := context.Background()
			var cancel context.CancelFunc
			if kind == "caller-context" {
				ctx, cancel = context.WithTimeout(ctx, 300*time.Millisecond)
				defer cancel()
			} else {
				o.CommandTimeout = 300 * time.Millisecond
			}
			start := time.Now()
			out, err := helperProcess(t, ctx, o, "tree", root)
			if !errors.Is(err, context.DeadlineExceeded) || len(out.stdout) != 0 || time.Since(start) > 2*time.Second {
				t.Fatalf("deadline did not bound/reap tree: %v", err)
			}
			assertWriterStopped(t, root)
		})
	}
}
