//go:build linux

package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testGitRunner(t *testing.T) *GitRunner {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	o := DefaultGitRunnerOptions()
	o.CommandTimeout = 20 * time.Second
	o.DrainTimeout = 300 * time.Millisecond
	r, err := NewGitRunner(root, o)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func gitMust(t *testing.T, r *GitRunner, dir string, input []byte, args ...string) []byte {
	t.Helper()
	out, err := r.command(context.Background(), dir, gitOffline, input, args...)
	if err != nil {
		t.Fatalf("fixture command %s: %v", args[0], err)
	}
	return out.stdout
}
func gitFixture(t *testing.T, r *GitRunner, format string) (string, string) {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "source.git")
	gitMust(t, r, "", nil, "init", "--bare", "--object-format="+format, "--initial-branch=trunk", "--", repo)
	tree := strings.TrimSpace(string(gitMust(t, r, repo, []byte{}, "mktree")))
	oid := strings.TrimSpace(string(gitMust(t, r, repo, []byte("synthetic commit\n"), "-c", "user.name=Synthetic", "-c", "user.email=synthetic@example.invalid", "-c", "commit.gpgSign=false", "commit-tree", tree)))
	gitMust(t, r, repo, nil, "update-ref", "refs/heads/trunk", oid)
	return repo, oid
}

func TestGitEnvironmentIsolation(t *testing.T) {
	root := t.TempDir()
	sentinel := filepath.Join(root, "executed")
	helper := filepath.Join(root, "hostile-helper")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nprintf executed >> '"+sentinel+"'\nexit 91\n"), 0700); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(root, "hostile.gitconfig")
	if err := os.WriteFile(global, []byte("[core]\n fsmonitor = "+helper+"\n hooksPath = "+root+"\n[credential]\n helper = !"+helper+"\n[init]\n templateDir = "+root+"\n[url \"ext::"+helper+"\"]\n insteadOf = https://example.invalid/\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{
		"HOME": root, "XDG_CONFIG_HOME": root, "GIT_CONFIG_GLOBAL": global, "GIT_CONFIG_SYSTEM": global, "GIT_CONFIG_NOSYSTEM": "0",
		"GIT_CONFIG_COUNT": "1", "GIT_CONFIG_KEY_0": "core.fsmonitor", "GIT_CONFIG_VALUE_0": helper,
		"GIT_CONFIG_PARAMETERS": "'core.fsmonitor'='" + helper + "'", "GIT_DIR": root, "GIT_WORK_TREE": root, "GIT_INDEX_FILE": filepath.Join(root, "external-index"),
		"GIT_OBJECT_DIRECTORY": filepath.Join(root, "external-objects"), "GIT_ALTERNATE_OBJECT_DIRECTORIES": root, "GIT_NAMESPACE": "injected",
		"GIT_EXEC_PATH": root, "GIT_TEMPLATE_DIR": root, "GIT_SSH_COMMAND": helper, "GIT_ASKPASS": helper, "SSH_ASKPASS": helper,
		"GIT_TRACE": filepath.Join(root, "trace"), "GIT_TRACE_PACKET": filepath.Join(root, "packet-trace"), "LD_PRELOAD": "/synthetic-do-not-load.so",
	} {
		t.Setenv(key, value)
	}
	r := testGitRunner(t)
	repo, oid := gitFixture(t, r, "sha1")
	// Repository-local executable config must also be overridden, not just HOME.
	config := filepath.Join(repo, "config")
	f, err := os.OpenFile(config, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString("\n[core]\n fsmonitor = " + helper + "\n hooksPath = " + root + "\n[credential]\n helper = !" + helper + "\n[core]\n pager = " + helper + "\n[uploadpack]\n packObjectsHook = " + helper + "\n")
	if closeErr := f.Close(); err != nil || closeErr != nil {
		t.Fatal(err, closeErr)
	}
	if err := os.WriteFile(filepath.Join(root, "reference-transaction"), []byte("#!/bin/sh\nprintf hook >> '"+sentinel+"'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	checked, err := r.ValidateGitRepository(context.Background(), repo)
	if err != nil || checked.ObjectFormat != "sha1" || !checked.Bare {
		t.Fatalf("source inspection: %+v %v", checked, err)
	}
	gitMust(t, r, repo, nil, "update-ref", "refs/heads/copied", oid)
	if got := strings.TrimSpace(string(gitMust(t, r, repo, nil, "rev-parse", "--verify", "refs/heads/copied"))); got != oid {
		t.Fatal("isolated command changed refs")
	}
	// A replacement ref must not substitute the object read by a machine command.
	blob := strings.TrimSpace(string(gitMust(t, r, repo, []byte("synthetic blob"), "hash-object", "-w", "--stdin")))
	gitMust(t, r, repo, nil, "update-ref", "refs/replace/"+oid, blob)
	if got := string(gitMust(t, r, repo, nil, "cat-file", "-t", oid)); got != "commit\n" {
		t.Fatal("replacement refs were honored")
	}
	dest := filepath.Join(t.TempDir(), "isolated-mirror.git")
	if _, err := r.command(context.Background(), "", gitLocalSource, nil, "clone", "--mirror", "--no-local", "--", repo, dest); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(gitMust(t, r, dest, nil, "rev-parse", "--verify", "refs/heads/trunk"))); got != oid {
		t.Fatal("local clone ref mismatch")
	}
	for _, path := range []string{sentinel, filepath.Join(root, "external-index"), filepath.Join(root, "external-objects"), filepath.Join(root, "trace"), filepath.Join(root, "packet-trace")} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("inherited executable/config/output escaped isolation at %s: %v", filepath.Base(path), err)
		}
	}
	// Generated repositories must not copy files from hostile templates.
	if _, err := os.Stat(filepath.Join(repo, "hostile-helper")); !os.IsNotExist(err) {
		t.Fatal("inherited template directory was used")
	}
	worktree := filepath.Join(t.TempDir(), "worktree")
	gitMust(t, r, "", nil, "init", "--initial-branch=trunk", "--", worktree)
	workConfig := filepath.Join(worktree, ".git", "config")
	originalWorkConfig, err := os.ReadFile(workConfig)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workConfig, append(originalWorkConfig, []byte("\n[core]\n fsmonitor = "+helper+"\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	if got := gitMust(t, r, worktree, nil, "config", "--get", "core.fsmonitor"); string(got) != "false\n" {
		t.Fatalf("local fsmonitor override missing: %q", got)
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatal("repository-local fsmonitor executed")
	}
	if inspected, err := r.ValidateGitRepository(context.Background(), worktree); err != nil || inspected.Bare || inspected.GitDir != filepath.Join(worktree, ".git") {
		t.Fatalf("worktree inspection: %+v %v", inspected, err)
	}
	for _, variant := range []string{
		"[include]\n path = /synthetic/forbidden\n", "[includeIf \"gitdir:**\"]\n path = /synthetic/forbidden\n",
		"[extensions]\n partialClone = origin\n", "[remote \"origin\"]\n promisor = true\n", "[remote \"origin\"]\n partialCloneFilter = blob:none\n",
		"[core]\n alternateRefsCommand = forbidden\n", "[core]\n worktree = /synthetic/external\n", "[extensions]\n worktreeConfig = true\n",
	} {
		original, err := os.ReadFile(config)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(config, append(append([]byte{}, original...), []byte("\n"+variant)...), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := r.ValidateGitRepository(context.Background(), repo); !errors.Is(err, ErrUnsafeRepository) {
			t.Fatalf("unsafe local config accepted: %q, %v", variant, err)
		}
		if err := os.WriteFile(config, original, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"objects/info/alternates", "objects/info/http-alternates", "commondir", "shallow", "config.worktree"} {
		path := filepath.Join(repo, filepath.FromSlash(name))
		if err := os.WriteFile(path, []byte("/synthetic/external\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := r.ValidateGitRepository(context.Background(), repo); !errors.Is(err, ErrUnsafeRepository) {
			t.Fatalf("external/partial object store accepted: %s, %v", name, err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
}

func TestGitTransportPolicy(t *testing.T) {
	r := testGitRunner(t)
	repo, oid := gitFixture(t, r, "sha1")
	for _, source := range []string{"https://127.0.0.1:1/never.git", "ssh://127.0.0.1:1/never.git", "git://127.0.0.1:1/never.git", "file://" + repo, repo, "ext::/synthetic/helper", "synthetic-host:path"} {
		t.Run(source, func(t *testing.T) {
			dest := filepath.Join(t.TempDir(), "denied.git")
			out, err := r.command(context.Background(), "", gitOffline, nil, "clone", "--mirror", "--no-local", "--", source, dest)
			if !errors.Is(err, ErrGitTransportDenied) || len(out.stdout) != 0 {
				t.Fatal("offline transport allowed")
			}
			if _, err := os.Lstat(dest); !os.IsNotExist(err) {
				t.Fatal("offline policy launched clone before rejection")
			}
			out, err = r.command(context.Background(), "", gitLocalSource, nil, "clone", "--mirror", "--no-local", "--", source, filepath.Join(t.TempDir(), "local.git"))
			if source == repo {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("local policy accepted URL/helper/scp syntax")
			}
		})
	}
	dest := filepath.Join(t.TempDir(), "local.git")
	if _, err := r.command(context.Background(), "", gitLocalSource, nil, "clone", "--mirror", "--no-local", "--", repo, dest); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(gitMust(t, r, dest, nil, "rev-parse", "--verify", "HEAD"))); got != oid {
		t.Fatal("explicit local acquisition did not preserve observed HEAD")
	}
	if _, err := r.command(context.Background(), "", gitLocalSource, nil, "clone", "--mirror", "--no-local", "--", repo, dest); !errors.Is(err, ErrGitTransportDenied) {
		t.Fatal("existing clone destination accepted")
	}
	for _, args := range [][]string{{"fetch", repo}, {"clone", repo, dest}, {"clone", "--mirror", "--no-local", "--", "-option", dest}} {
		if _, err := r.command(context.Background(), "", gitLocalSource, nil, args...); err == nil {
			t.Fatal("local policy accepted uncontrolled command")
		}
	}
	if _, err := r.command(context.Background(), "", gitTransportPolicy(99), nil, "status"); !errors.Is(err, ErrGitTransportDenied) {
		t.Fatal("unknown transport policy accepted")
	}
}

func TestGitOutputLimits(t *testing.T) {
	r := testGitRunner(t)
	repo, oid := gitFixture(t, r, "sha1")
	r.options.MachineOutputBytes = len(oid) + 1
	got := gitMust(t, r, repo, nil, "rev-parse", "--verify", "HEAD")
	if string(got) != oid+"\n" {
		t.Fatal("exact stdout limit changed data")
	}
	r.options.MachineOutputBytes--
	out, err := r.command(context.Background(), repo, gitOffline, nil, "rev-parse", "--verify", "HEAD")
	if !errors.Is(err, ErrGitOutputLimit) || len(out.stdout) != 0 {
		t.Fatalf("overflow was truncated into valid machine output: %q %v", out.stdout, err)
	}
	r.options.MachineOutputBytes = 1024
	r.options.DiagnosticBytes = 32
	sentinel := "synthetic-secret-DO-NOT-RETURN"
	out, err = r.command(context.Background(), repo, gitOffline, nil, "cat-file", "-t", strings.Repeat(sentinel, 20))
	if err == nil || strings.Contains(err.Error(), sentinel) || strings.Contains(out.diagnostic, sentinel) {
		t.Fatal("diagnostic/argument sentinel leaked")
	}
	var commandErr *GitCommandError
	if !errors.As(err, &commandErr) || !strings.Contains(commandErr.Diagnostic, "truncated") || strings.Contains(commandErr.Diagnostic, sentinel) {
		t.Fatalf("diagnostic bound/redaction not recorded: %v", err)
	}
	// A running producer must be stopped on overflow, not just have its output
	// silently truncated while it runs until the full command timeout.
	o := r.options
	o.CommandTimeout = 10 * time.Second
	o.MachineOutputBytes = 1024
	start := time.Now()
	out, err = helperProcess(t, context.Background(), o, "flood", t.TempDir())
	if !errors.Is(err, ErrGitOutputLimit) || len(out.stdout) != 0 || time.Since(start) > 2*time.Second {
		t.Fatalf("running overflow not stopped: %v", err)
	}
	for _, bad := range []GitRunnerOptions{{}, {CommandTimeout: -1}, {time.Second, time.Second, 257 << 20, 32}, {time.Second, time.Second, 32, 2 << 20}} {
		if _, err := NewGitRunner(r.scratchRoot, bad); err == nil {
			t.Fatal("invalid options accepted")
		}
	}
}

func TestGitCapabilities(t *testing.T) {
	r := testGitRunner(t)
	caps, err := r.ProbeCapabilities(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []GitFormatCapabilities{caps.SHA1, caps.SHA256} {
		if !c.FullBundle || !c.IndependentRefs || !c.DetachedHead || !c.ObjectIntegrity {
			t.Fatalf("operations not proved: %+v", c)
		}
		want := 40
		if c.ObjectFormat == "sha256" {
			want = 64
		}
		if c.OIDHexLength != want {
			t.Fatalf("wrong object ID width: %+v", c)
		}
	}
	if caps.SHA1.ObjectFormat != "sha1" || caps.SHA256.ObjectFormat != "sha256" {
		t.Fatalf("formats guessed: %+v", caps)
	}
	entries, err := os.ReadDir(r.scratchRoot)
	if err != nil || len(entries) != 0 {
		t.Fatalf("probe left temporary output: %v %v", entries, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.ProbeCapabilities(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled probe not cancelled: %v", err)
	}
}
