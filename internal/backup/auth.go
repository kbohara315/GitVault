package backup

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// These errors deliberately contain no request, path, subprocess output or secret.
var (
	ErrCredentialBrokerUnsupported = errors.New("credential broker transport unsupported")
	ErrCredentialDenied            = errors.New("credential request denied")
	ErrCredentialBrokerClosed      = errors.New("credential broker closed")
)

const credentialHelperMode = "--gitvault-credential-helper"
const credentialInputLimit = 4096
const credentialSecretLimit = 2560
const credentialConnectionTimeout = 5 * time.Second

// CredentialBrokerOptions is captured once for a single repository task. Secret
// is copied; the caller remains responsible for clearing its original slice.
// Executable must identify this same installed executable, not a helper script.
type CredentialBrokerOptions struct {
	Context           context.Context
	PrivateDir        string
	AllowedHost       string
	AllowedRepository string // canonical owner/name, without .git
	Username          string
	Secret            []byte
	Executable        string
}

// CredentialBroker exposes only a nonsecret locator to the job. PermitGroup must
// be called after starting a direct child as a new process-group leader, BEFORE
// releasing that child's startup barrier. Never infer authority from environment
// variables. Keep the leader alive until Close; group identity is rechecked.
// Close revokes access, closes pending connections, waits, and clears owned secret
// bytes (best effort, not a claim of complete Go/runtime memory erasure).
type CredentialBroker struct{ platform credentialBrokerPlatform }

type credentialBrokerPlatform interface {
	permitGroup(int) error
	close() error
	locator() (string, int)
}

func NewCredentialBroker(o CredentialBrokerOptions) (*CredentialBroker, error) {
	if o.Context == nil || o.Context.Err() != nil || o.AllowedHost != "github.com" || !credentialRepository(o.AllowedRepository) || !credentialValue(o.Username, 256) || !credentialValue(string(o.Secret), credentialSecretLimit) || !filepath.IsAbs(o.PrivateDir) || !filepath.IsAbs(o.Executable) {
		return nil, ErrCredentialDenied
	}
	p, err := newCredentialBrokerPlatform(o)
	if err != nil {
		return nil, err
	}
	return &CredentialBroker{platform: p}, nil
}

func (b *CredentialBroker) PermitGroup(pid int) error { return b.platform.permitGroup(pid) }
func (b *CredentialBroker) Close() error              { return b.platform.close() }

// HelperArguments are nonsecret argv for direct helper execution. Git itself
// appends "get" (or store/erase, which this helper refuses).
func (b *CredentialBroker) HelperArguments() []string {
	path, pid := b.platform.locator()
	return []string{credentialHelperMode, "--socket", path, "--broker-pid", strconv.Itoa(pid)}
}

// GitHelperConfig returns a PRIVATE, ephemeral Git -c credential.helper value.
// Git's helper protocol necessarily invokes a shell for a helper with arguments.
// This fixed grammar uses exec and single-quoted literals; it never interpolates
// repository data or secrets. The runner must use argv, reset credential.helper
// first, set credential.useHttpPath=true, disable redirects and interactive
// prompting, and never save this value to Git config. Offline jobs MUST NOT use
// this value or instantiate a broker. The application must dispatch HelperMain
// before initializing UI, settings, logging, or secret-store adapters.
func (b *CredentialBroker) GitHelperConfig(executable string) (string, error) {
	if !credentialSameExecutable(executable) {
		return "", ErrCredentialDenied
	}
	args := append([]string{executable}, b.HelperArguments()...)
	for i := range args {
		args[i] = "'" + strings.ReplaceAll(args[i], "'", "'\\''") + "'"
	}
	return "!exec " + strings.Join(args, " "), nil
}

func credentialSameExecutable(path string) bool {
	if !filepath.IsAbs(path) || strings.ContainsAny(path, "\x00\r\n") {
		return false
	}
	own, err := os.Executable()
	if err != nil {
		return false
	}
	a, err := os.Stat(own)
	if err != nil {
		return false
	}
	b, err := os.Stat(path)
	return err == nil && b.Mode().IsRegular() && b.Mode().Perm()&0022 == 0 && b.Mode().Perm()&0111 != 0 && os.SameFile(a, b)
}

func credentialValue(s string, limit int) bool {
	if len(s) == 0 || len(s) > limit {
		return false
	}
	for _, c := range []byte(s) {
		if c < 33 || c > 126 {
			return false
		}
	}
	return true
}

func credentialRepository(s string) bool {
	p := strings.Split(s, "/")
	if len(p) != 2 || len(p[0]) > 39 || len(p[1]) > 100 || strings.HasSuffix(p[1], ".git") {
		return false
	}
	for i, part := range p {
		if part == "" || part == "." || part == ".." || strings.ToLower(part) != part || strings.HasPrefix(part, "-") || strings.HasSuffix(part, "-") {
			return false
		}
		for _, c := range part {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || i == 1 && (c == '_' || c == '.')) {
				return false
			}
		}
	}
	return true
}

// credentialRequest accepts exactly three Git attributes, ending at blank or EOF.
// Reader buffering is bounded. Unknown/duplicate attributes, controls, HTTP ports,
// URLs, username/password attributes and alternate path spellings are rejected.
func credentialRequest(r io.Reader) (host, path string, err error) {
	s := bufio.NewScanner(io.LimitReader(r, credentialInputLimit+1))
	s.Buffer(make([]byte, 256), credentialInputLimit+1)
	// Unlike ScanLines, retain CR so that CRLF/control input is refused.
	s.Split(func(data []byte, atEOF bool) (int, []byte, error) {
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			return i + 1, data[:i], nil
		}
		if atEOF && len(data) != 0 {
			return len(data), data, nil
		}
		return 0, nil, nil
	})
	seen := make(map[string]bool, 3)
	total := 0
	for s.Scan() {
		line := s.Text()
		total += len(line) + 1
		if total > credentialInputLimit {
			return "", "", ErrCredentialDenied
		}
		if line == "" {
			if credentialRequestScope(seen, host, path) {
				return host, path, nil
			}
			return "", "", ErrCredentialDenied
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || seen[key] || !credentialValue(value, 256) {
			return "", "", ErrCredentialDenied
		}
		seen[key] = true
		switch key {
		case "protocol":
			if value != "https" {
				return "", "", ErrCredentialDenied
			}
		case "host":
			host = value
		case "path":
			path = value
		default:
			return "", "", ErrCredentialDenied
		}
	}
	// Git credential_write terminates a helper request with EOF, not necessarily
	// a blank line. Network IPC uses a blank line and a read deadline instead.
	if s.Err() == nil && credentialRequestScope(seen, host, path) {
		return host, path, nil
	}
	return "", "", ErrCredentialDenied
}

func credentialRequestScope(seen map[string]bool, host, path string) bool {
	return len(seen) == 3 && seen["protocol"] && host == "github.com" && strings.HasSuffix(path, ".git") && credentialRepository(strings.TrimSuffix(path, ".git"))
}

// HelperMain handles only the private mode. args excludes argv[0]. Errors are
// intentionally silent: no Git/server data is ever copied to stderr. Only a
// successful get writes credential bytes, to the designated Git stdout pipe.
func HelperMain(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) (handled bool, exitCode int) {
	if len(args) == 0 || args[0] != credentialHelperMode {
		return false, 0
	}
	if ctx == nil || ctx.Err() != nil || len(args) != 6 || args[1] != "--socket" || !filepath.IsAbs(args[2]) || args[3] != "--broker-pid" || args[5] != "get" {
		return true, 1
	}
	ctx, cancel := context.WithTimeout(ctx, credentialConnectionTimeout)
	defer cancel()
	// Git supplies pollable pipes. Set their deadlines and close them when the
	// private helper is cancelled; do not leave blocked I/O goroutines behind.
	// In-memory readers/writers used by callers/tests must be nonblocking.
	if r, ok := stdin.(interface{ SetReadDeadline(time.Time) error }); ok {
		_ = r.SetReadDeadline(time.Now().Add(credentialConnectionTimeout))
	}
	if w, ok := stdout.(interface{ SetWriteDeadline(time.Time) error }); ok {
		_ = w.SetWriteDeadline(time.Now().Add(credentialConnectionTimeout))
	}
	stop := context.AfterFunc(ctx, func() {
		if r, ok := stdin.(io.Closer); ok {
			_ = r.Close()
		}
		if w, ok := stdout.(io.Closer); ok {
			_ = w.Close()
		}
	})
	defer stop()
	pid, err := strconv.Atoi(args[4])
	if err != nil || pid <= 0 {
		return true, 1
	}
	host, path, err := credentialRequest(stdin)
	if err != nil {
		return true, 1
	}
	if err := credentialHelperExchange(ctx, args[2], pid, host, path, stdout); err != nil {
		return true, 1
	}
	return true, 0
}

func credentialWireRequest(host, path string) string {
	return fmt.Sprintf("protocol=https\nhost=%s\npath=%s\n\n", host, path)
}
