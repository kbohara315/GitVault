package backup

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type GitRunnerOptions struct {
	CommandTimeout     time.Duration
	DrainTimeout       time.Duration
	MachineOutputBytes int
	DiagnosticBytes    int
}

func DefaultGitRunnerOptions() GitRunnerOptions {
	return GitRunnerOptions{time.Hour, time.Second, 32 << 20, 64 << 10}
}
func (o GitRunnerOptions) validate() error {
	if o.CommandTimeout <= 0 || o.CommandTimeout > 24*time.Hour || o.DrainTimeout <= 0 || o.DrainTimeout > 30*time.Second || o.MachineOutputBytes < 1 || o.MachineOutputBytes > 256<<20 || o.DiagnosticBytes < 1 || o.DiagnosticBytes > 1<<20 {
		return fmt.Errorf("invalid Git runner limits")
	}
	return nil
}

// GitRunner captures its executable, allowlisted environment and finite options
// once. ScratchRoot must be an application-owned private directory; each command
// uses a unique home beneath it and removes only that owned temporary directory.
// All command execution is private to the recovery engine, never UI supplied.
type GitRunner struct {
	executable  string
	scratchRoot string
	environment []string
	options     GitRunnerOptions
}

func NewGitRunner(scratchRoot string, o GitRunnerOptions) (*GitRunner, error) {
	if err := o.validate(); err != nil {
		return nil, err
	}
	if err := processOwnershipSupported(); err != nil {
		return nil, err
	}
	root, err := safeDirectory(scratchRoot)
	if err != nil {
		return nil, ErrUnsafeRepository
	}
	info, err := os.Stat(root)
	if err != nil || info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("Git scratch directory must be private")
	}
	path, err := exec.LookPath("git")
	if err != nil {
		return nil, ErrGitUnavailable
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return nil, ErrGitUnavailable
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return nil, ErrGitUnavailable
	}
	info, err = os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, ErrGitUnavailable
	}
	var env []string
	for _, key := range []string{"PATH", "SystemRoot", "SYSTEMROOT", "WINDIR", "TEMP", "TMP", "TMPDIR"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	return &GitRunner{path, root, env, o}, nil
}

type gitTransportPolicy int

const (
	gitOffline gitTransportPolicy = iota
	gitLocalSource
)

func safeDirectory(path string) (string, error) {
	if !filepath.IsAbs(path) || strings.ContainsAny(path, "\x00\r\n") {
		return "", ErrUnsafeRepository
	}
	clean := filepath.Clean(path)
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil || resolved != clean {
		return "", ErrUnsafeRepository
	}
	info, err := os.Stat(clean)
	if err != nil || !info.IsDir() {
		return "", ErrUnsafeRepository
	}
	return clean, nil
}

func (r *GitRunner) command(ctx context.Context, dir string, policy gitTransportPolicy, input []byte, args ...string) (processOutput, error) {
	if err := ctx.Err(); err != nil {
		return processOutput{}, &GitCommandError{ExitCode: -1, cause: err}
	}
	if len(input) > r.options.MachineOutputBytes {
		return processOutput{}, ErrGitOutputLimit
	}
	if err := r.checkTransport(ctx, policy, args); err != nil {
		return processOutput{}, err
	}
	if dir != "" {
		var err error
		dir, err = safeDirectory(dir)
		if err != nil {
			return processOutput{}, err
		}
	}
	home, err := os.MkdirTemp(r.scratchRoot, "git-command-")
	if err != nil {
		return processOutput{}, ErrGitUnavailable
	}
	defer os.RemoveAll(home)
	empty := filepath.Join(home, "empty")
	if err = os.Mkdir(empty, 0700); err != nil {
		return processOutput{}, ErrGitUnavailable
	}
	if dir == "" {
		dir = home
	}
	env := append([]string(nil), r.environment...)
	env = append(env, "HOME="+home, "USERPROFILE="+home, "XDG_CONFIG_HOME="+home, "XDG_CONFIG_DIRS="+empty, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_SYSTEM="+os.DevNull, "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_ATTR_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=", "SSH_ASKPASS=", "GIT_PROTOCOL_FROM_USER=0", "GIT_NO_REPLACE_OBJECTS=1", "GIT_NO_LAZY_FETCH=1", "GIT_OPTIONAL_LOCKS=0", "GIT_PAGER=", "GIT_EDITOR=false", "GIT_SEQUENCE_EDITOR=false", "LC_ALL=C", "LANG=C", "TZ=UTC", "TMPDIR="+home, "TMP="+home, "TEMP="+home)
	env = append(env, "GIT_CEILING_DIRECTORIES="+filepath.Dir(dir), "GIT_DISCOVERY_ACROSS_FILESYSTEM=0")
	argv := []string{"--no-pager", "--no-optional-locks", "--no-replace-objects"}
	settings := []string{"core.hooksPath=" + empty, "init.templateDir=" + empty, "credential.helper=", "credential.interactive=false", "core.askPass=", "core.fsmonitor=false", "core.untrackedCache=false", "core.useReplaceRefs=false", "core.attributesFile=" + os.DevNull, "core.excludesFile=" + os.DevNull, "core.editor=false", "sequence.editor=false", "gc.auto=0", "maintenance.auto=false", "submodule.recurse=false", "fetch.recurseSubmodules=false", "protocol.allow=never", "protocol.file.allow=never", "protocol.ext.allow=never", "protocol.http.allow=never", "protocol.https.allow=never", "protocol.ssh.allow=never", "protocol.git.allow=never", "transfer.fsckObjects=true", "fetch.fsckObjects=true", "receive.fsckObjects=true"}
	if policy == gitLocalSource {
		settings = append(settings, "protocol.file.allow=always")
	}
	for _, s := range settings {
		argv = append(argv, "-c", s)
	}
	argv = append(argv, args...)
	return runOwnedProcess(ctx, r.executable, argv, env, dir, input, r.options)
}

func (r *GitRunner) checkTransport(ctx context.Context, policy gitTransportPolicy, args []string) error {
	if policy == gitOffline {
		// No transport-bearing builtins, external aliases, checkout/filter paths
		// or low-level HTTP commands are admitted by the offline engine policy.
		verbArgs := args
		for len(verbArgs) >= 2 && verbArgs[0] == "-c" {
			key, value, ok := strings.Cut(verbArgs[1], "=")
			if !ok || key != "user.name" && key != "user.email" && !(key == "commit.gpgSign" && value == "false") {
				return ErrGitTransportDenied
			}
			verbArgs = verbArgs[2:]
		}
		if len(verbArgs) == 0 {
			return ErrGitTransportDenied
		}
		switch verbArgs[0] {
		case "init", "config", "rev-parse", "for-each-ref", "show-ref", "symbolic-ref", "update-ref", "hash-object", "mktree", "commit-tree", "cat-file", "bundle", "fsck":
			return nil
		default:
			return ErrGitTransportDenied
		}
	}
	if policy != gitLocalSource || len(args) != 6 || args[0] != "clone" || args[1] != "--mirror" || args[2] != "--no-local" || args[3] != "--" {
		return ErrGitTransportDenied
	}
	source, destination := args[4], args[5]
	if !filepath.IsAbs(source) || !filepath.IsAbs(destination) || strings.ContainsAny(source, "\x00\r\n") || strings.ContainsAny(destination, "\x00\r\n") {
		return ErrGitTransportDenied
	}
	if _, err := safeDirectory(filepath.Dir(destination)); err != nil {
		return ErrGitTransportDenied
	}
	if _, err := os.Lstat(destination); !os.IsNotExist(err) {
		return ErrGitTransportDenied
	}
	_, err := r.ValidateGitRepository(ctx, source)
	return err
}
