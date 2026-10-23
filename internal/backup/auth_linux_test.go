package backup

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// The test executable uses the SAME exported private-mode dispatcher as main.
// FD 3 is a startup barrier, not a secret channel. Production integration must
// establish the same start/register/release ordering before Git can use a helper.
func TestCredentialHelperFixture(t *testing.T) {
	idx := -1
	for i, s := range os.Args {
		if s == "--credential-fixture" {
			idx = i
			break
		}
	}
	if idx < 0 {
		return
	}
	args := os.Args[idx+1:]
	if len(args) > 0 && args[0] == "--no-gate" {
		args = args[1:] // Git credential fill is held at its own stdin barrier.
	} else {
		gate := os.NewFile(3, "startup")
		var one [1]byte
		if _, err := io.ReadFull(gate, one[:]); err != nil {
			os.Exit(90)
		}
		_ = gate.Close()
	}
	if len(args) > 0 && args[0] == "--spawn-grandchild" {
		exe, _ := os.Executable()
		c := exec.Command(exe, append([]string{"-test.run=^TestCredentialHelperFixture$", "--", "--credential-fixture"}, args[1:]...)...)
		r, w, err := os.Pipe()
		if err != nil {
			os.Exit(91)
		}
		c.ExtraFiles = []*os.File{r}
		c.Env = []string{}
		c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := c.Start(); err != nil {
			os.Exit(92)
		}
		_ = r.Close()
		_, _ = w.Write([]byte{1})
		_ = w.Close()
		if err := c.Wait(); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	if len(args) == 6 && args[5] == "--hold-connection" {
		c, err := net.Dial("unix", args[2])
		if err != nil {
			os.Exit(94)
		}
		_, _ = io.WriteString(c, "protocol=https\n") // deliberately incomplete
		_, _ = os.Stdout.Write([]byte{1})
		data, _ := io.ReadAll(c)
		_ = c.Close()
		if len(data) != 0 {
			os.Exit(95)
		}
		os.Exit(0)
	}
	handled, code := HelperMain(context.Background(), args, os.Stdin, os.Stdout, os.Stderr)
	if !handled {
		os.Exit(93)
	}
	os.Exit(code)
}

func credentialTestBroker(t *testing.T, ctx context.Context) (*CredentialBroker, string, []byte) {
	t.Helper()
	// Unix socket paths have a small platform maximum. Do not use t.TempDir's
	// long test-name prefix. This is an exact owned private fixture directory.
	dir, err := os.MkdirTemp("/tmp/opencode", "gv-auth-")
	if err != nil {
		t.Fatal("fixture directory unavailable")
	}
	t.Cleanup(func() { _ = os.Remove(dir) })
	exe, err := os.Executable()
	if err != nil {
		t.Fatal("fixture executable unavailable")
	}
	// go test inherits the developer umask (002 here), yielding a group-writable
	// temporary binary. Qualify this synthetic installed-executable fixture; do
	// not relax the production rejection of writable executable images.
	if err := os.Chmod(exe, 0700); err != nil {
		t.Fatal("fixture executable permissions unavailable")
	}
	secret := []byte("synthetic-GV-PAT-sentinel-DO-NOT-LOG")
	b, err := NewCredentialBroker(CredentialBrokerOptions{Context: ctx, PrivateDir: dir, AllowedHost: "github.com", AllowedRepository: "fixture/private", Username: "x-access-token", Secret: secret, Executable: exe})
	if err != nil {
		t.Fatal("broker constructor failed")
	}
	t.Cleanup(func() { _ = b.Close(); clear(secret) })
	return b, dir, secret
}

type credentialTestChild struct {
	cmd    *exec.Cmd
	gate   *os.File
	out    bytes.Buffer
	stderr bytes.Buffer
}

func credentialStartHelper(t *testing.T, b *CredentialBroker, input string, permit, grandchild bool, operation string) *credentialTestChild {
	t.Helper()
	exe, _ := os.Executable()
	args := []string{"-test.run=^TestCredentialHelperFixture$", "--", "--credential-fixture"}
	if grandchild {
		args = append(args, "--spawn-grandchild")
	}
	args = append(args, b.HelperArguments()...)
	args = append(args, operation)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal("barrier creation failed")
	}
	c := &credentialTestChild{cmd: exec.Command(exe, args...), gate: w}
	c.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.cmd.ExtraFiles = []*os.File{r}
	c.cmd.Env = []string{}
	c.cmd.Stdin = strings.NewReader(input)
	c.cmd.Stdout, c.cmd.Stderr = &c.out, &c.stderr
	if err := c.cmd.Start(); err != nil {
		_ = r.Close()
		_ = w.Close()
		t.Fatal("fixture process failed to start")
	}
	_ = r.Close()
	t.Cleanup(func() {
		_ = w.Close()
		_ = c.cmd.Process.Kill()
	})
	if permit {
		if err := b.PermitGroup(c.cmd.Process.Pid); err != nil {
			t.Fatal("explicit process-group registration failed")
		}
	}
	return c
}

func credentialFinish(t *testing.T, c *credentialTestChild, success bool) {
	t.Helper()
	_, _ = c.gate.Write([]byte{1})
	_ = c.gate.Close()
	done := make(chan error, 1)
	go func() { done <- c.cmd.Wait() }()
	select {
	case err := <-done:
		if (err == nil) != success {
			t.Fatal("helper exit status violated expectation (output withheld)")
		}
	case <-time.After(10 * time.Second):
		_ = c.cmd.Process.Kill()
		<-done
		t.Fatal("helper did not terminate within bound")
	}
	if c.stderr.Len() != 0 {
		t.Fatal("helper emitted diagnostics")
	}
	if !success && c.out.Len() != 0 {
		t.Fatal("denied request wrote output")
	}
}

func TestCredentialBrokerScope(t *testing.T) {
	valid := credentialWireRequest("github.com", "fixture/private.git")
	t.Run("real-git-credential-fill", func(t *testing.T) {
		b, _, secret := credentialTestBroker(t, context.Background())
		exe, _ := os.Executable()
		config, err := b.GitHelperConfig(exe)
		if err != nil {
			t.Fatal("helper config unavailable")
		}
		// Installed main dispatches before flag parsing. A Go test executable
		// needs this static fixture-only routing prefix instead; all locator and
		// executable quoting bytes are the production GitHelperConfig output.
		quotedExe := "'" + strings.ReplaceAll(exe, "'", "'\\''") + "'"
		config = strings.Replace(config, quotedExe, quotedExe+" '-test.run=^TestCredentialHelperFixture$' '--' '--credential-fixture' '--no-gate'", 1)
		git, err := exec.LookPath("git")
		if err != nil {
			t.Fatal("required installed Git unavailable")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, git, "-c", "credential.helper=", "-c", "credential.helper="+config, "-c", "credential.useHttpPath=true", "-c", "credential.interactive=false", "credential", "fill")
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0"}
		var out, diagnostics bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &diagnostics
		stdin, err := cmd.StdinPipe()
		if err != nil || cmd.Start() != nil {
			t.Fatal("Git credential fixture could not start")
		}
		defer func() { _ = cmd.Process.Kill() }()
		if err := b.PermitGroup(cmd.Process.Pid); err != nil {
			_ = stdin.Close()
			_ = cmd.Wait()
			t.Fatal("Git process-group registration failed")
		}
		_, _ = io.WriteString(stdin, valid)
		_ = stdin.Close()
		if err := cmd.Wait(); err != nil || diagnostics.Len() != 0 || !bytes.Contains(out.Bytes(), secret) {
			t.Fatal("real Git helper protocol failed (output withheld)")
		}
		clear(out.Bytes())
	})
	t.Run("authorized-grandchild-byte-protocol", func(t *testing.T) {
		b, _, secret := credentialTestBroker(t, context.Background())
		c := credentialStartHelper(t, b, valid, true, true, "get")
		credentialFinish(t, c, true)
		expected := append([]byte("username=x-access-token\npassword="), secret...)
		expected = append(expected, '\n', '\n')
		if !bytes.Equal(c.out.Bytes(), expected) {
			t.Fatal("credential output was not exact protocol bytes")
		}
		clear(expected)
		c.out.Reset()
	})
	cases := []struct{ name, input, verb string }{
		{"wrong-host", credentialWireRequest("evil.invalid", "fixture/private.git"), "get"},
		{"wrong-path", credentialWireRequest("github.com", "fixture/other.git"), "get"},
		{"port", credentialWireRequest("github.com:443", "fixture/private.git"), "get"},
		{"http", strings.Replace(valid, "https", "http", 1), "get"},
		{"unknown-attribute", strings.Replace(valid, "\n\n", "\nurl=https://github.com/fixture/private.git\n\n", 1), "get"},
		{"duplicate", "protocol=https\nprotocol=https\nhost=github.com\npath=fixture/private.git\n\n", "get"},
		{"control", strings.ReplaceAll(valid, "\n", "\r\n"), "get"},
		{"oversized", "host=" + strings.Repeat("x", credentialInputLimit+10) + "\n\n", "get"},
		{"missing-attribute", "protocol=https\nhost=github.com\n", "get"},
		{"missing-git-suffix", credentialWireRequest("github.com", "fixture/private"), "get"},
		{"path-traversal", credentialWireRequest("github.com", "fixture/../private.git"), "get"},
		{"encoded-path", credentialWireRequest("github.com", "fixture/%70rivate.git"), "get"},
		{"store", valid, "store"},
		{"erase", valid, "erase"},
		{"unknown-verb", valid, "query"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, _, _ := credentialTestBroker(t, context.Background())
			c := credentialStartHelper(t, b, tc.input, true, false, tc.verb)
			credentialFinish(t, c, false)
		})
	}
	t.Run("unregistered-group", func(t *testing.T) {
		b, _, _ := credentialTestBroker(t, context.Background())
		c := credentialStartHelper(t, b, valid, false, false, "get")
		credentialFinish(t, c, false)
		if err := b.PermitGroup(os.Getpid()); !errors.Is(err, ErrCredentialDenied) {
			t.Fatal("broker accepted its own process group")
		}
	})
	t.Run("wrong-uid-and-reregistration", func(t *testing.T) {
		b, _, _ := credentialTestBroker(t, context.Background())
		c := credentialStartHelper(t, b, valid, true, false, "get")
		p := b.platform.(*linuxCredentialBroker)
		p.mu.Lock()
		allowed := p.authorized(&unix.Ucred{Pid: int32(c.cmd.Process.Pid), Uid: uint32(os.Getuid()) + 1})
		p.mu.Unlock()
		if allowed || !errors.Is(b.PermitGroup(c.cmd.Process.Pid), ErrCredentialDenied) {
			t.Fatal("broker accepted wrong UID or authority reassignment")
		}
		credentialFinish(t, c, true)
	})
	t.Run("different-executable-image", func(t *testing.T) {
		b, _, _ := credentialTestBroker(t, context.Background())
		cat, err := exec.LookPath("cat")
		if err != nil {
			t.Fatal("required fixture executable unavailable")
		}
		cmd := exec.Command(cat)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Env = []string{}
		input, err := cmd.StdinPipe()
		if err != nil || cmd.Start() != nil {
			t.Fatal("different-image fixture did not start")
		}
		defer func() { _ = input.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() }()
		if err := b.PermitGroup(cmd.Process.Pid); err != nil {
			t.Fatal("fixture group registration failed")
		}
		p := b.platform.(*linuxCredentialBroker)
		p.mu.Lock()
		allowed := p.authorized(&unix.Ucred{Pid: int32(cmd.Process.Pid), Uid: uint32(os.Getuid())})
		p.mu.Unlock()
		if allowed {
			t.Fatal("broker accepted a different executable in the permitted group")
		}
	})
	t.Run("raw-socket-cannot-self-authorize", func(t *testing.T) {
		b, _, _ := credentialTestBroker(t, context.Background())
		c := credentialStartHelper(t, b, valid, true, false, "get")
		socket, _ := b.platform.locator()
		conn, err := net.Dial("unix", socket)
		if err != nil {
			t.Fatal("socket unavailable")
		}
		_ = conn.SetDeadline(time.Now().Add(time.Second))
		_, _ = io.WriteString(conn, valid)
		data, _ := io.ReadAll(conn)
		_ = conn.Close()
		if len(data) != 0 {
			t.Fatal("same-UID outsider obtained credential bytes")
		}
		credentialFinish(t, c, true)
	})
}

func TestCredentialSentinelIsolation(t *testing.T) {
	b, dir, secret := credentialTestBroker(t, context.Background())
	c := credentialStartHelper(t, b, credentialWireRequest("github.com", "fixture/private.git"), true, false, "get")
	// Inspect actual kernel process captures while child is held behind barrier.
	for _, name := range []string{"cmdline", "environ"} {
		data, err := os.ReadFile("/proc/" + strconv.Itoa(c.cmd.Process.Pid) + "/" + name)
		if err != nil || bytes.Contains(data, secret) {
			t.Fatal("process capture unavailable or contained sentinel")
		}
	}
	exe, _ := os.Executable()
	helperConfig, err := b.GitHelperConfig(exe)
	if err != nil || strings.Contains(helperConfig, string(secret)) || !strings.HasPrefix(helperConfig, "!exec '") {
		t.Fatal("unsafe helper config")
	}
	if _, err := b.GitHelperConfig("/bin/sh"); !errors.Is(err, ErrCredentialDenied) {
		t.Fatal("arbitrary helper executable accepted")
	}
	socket, _ := b.platform.locator()
	for path, want := range map[string]os.FileMode{filepath.Dir(socket): 0700, socket: 0600} {
		info, err := os.Lstat(path)
		if err != nil || info.Mode().Perm() != want {
			t.Fatal("broker permissions incorrect")
		}
	}
	err = filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if strings.Contains(path, string(secret)) || entry.Type().IsRegular() {
			return ErrCredentialDenied // no credential/config/helper files at all
		}
		return nil
	})
	if err != nil {
		t.Fatal("broker created unexpected regular file or secret locator")
	}
	for _, safeErr := range []error{ErrCredentialDenied, ErrCredentialBrokerClosed, ErrCredentialBrokerUnsupported} {
		if strings.Contains(safeErr.Error(), string(secret)) {
			t.Fatal("error leaked sentinel")
		}
	}
	credentialFinish(t, c, true)
	if !bytes.Contains(c.out.Bytes(), secret) || bytes.Contains(c.stderr.Bytes(), secret) {
		t.Fatal("sentinel did not stay on designated pipe")
	}
	clear(c.out.Bytes())
}

func TestBrokerCancellationAndOfflineIsolation(t *testing.T) {
	t.Run("cancel-revokes-before-request-and-zeroes-owned-copy", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		b, dir, _ := credentialTestBroker(t, ctx)
		c := credentialStartHelper(t, b, credentialWireRequest("github.com", "fixture/private.git"), true, false, "get")
		p := b.platform.(*linuxCredentialBroker)
		ownedSecret := p.secret
		cancel()
		if err := b.Close(); err != nil {
			t.Fatal("close failed")
		}
		for _, v := range ownedSecret {
			if v != 0 {
				t.Fatal("owned credential bytes were not cleared")
			}
		}
		credentialFinish(t, c, false)
		if !errors.Is(b.PermitGroup(c.cmd.Process.Pid), ErrCredentialBrokerClosed) {
			t.Fatal("closed broker accepted authority")
		}
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 0 {
			t.Fatal("broker locator survived revocation")
		}
	})
	t.Run("bounded-connection-drain", func(t *testing.T) {
		b, _, _ := credentialTestBroker(t, context.Background())
		socket, _ := b.platform.locator()
		var conns []net.Conn
		for i := 0; i < credentialMaxConnections*3; i++ {
			c, err := net.DialTimeout("unix", socket, time.Second)
			if err == nil {
				conns = append(conns, c)
			}
		}
		defer func() {
			for _, c := range conns {
				_ = c.Close()
			}
		}()
		done := make(chan struct{})
		go func() { _ = b.Close(); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("broker did not drain pending connections")
		}
	})
	t.Run("cancel-closes-authorized-incomplete-request", func(t *testing.T) {
		b, _, _ := credentialTestBroker(t, context.Background())
		exe, _ := os.Executable()
		args := append([]string{"-test.run=^TestCredentialHelperFixture$", "--", "--credential-fixture"}, b.HelperArguments()...)
		args = append(args, "--hold-connection")
		gateR, gateW, err := os.Pipe()
		if err != nil {
			t.Fatal("barrier unavailable")
		}
		defer gateR.Close()
		defer gateW.Close()
		cmd := exec.Command(exe, args...)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.ExtraFiles = []*os.File{gateR}
		cmd.Env = []string{}
		var diagnostics bytes.Buffer
		cmd.Stderr = &diagnostics
		ready, err := cmd.StdoutPipe()
		if err != nil || cmd.Start() != nil {
			t.Fatal("incomplete-request fixture unavailable")
		}
		defer func() { _ = cmd.Process.Kill() }()
		if err := b.PermitGroup(cmd.Process.Pid); err != nil {
			t.Fatal("fixture process registration failed")
		}
		_, _ = gateW.Write([]byte{1})
		_ = gateW.Close()
		if r, ok := ready.(*os.File); ok {
			_ = r.SetReadDeadline(time.Now().Add(2 * time.Second))
		}
		var one [1]byte
		if _, err := io.ReadFull(ready, one[:]); err != nil {
			t.Fatal("incomplete-request fixture did not connect")
		}
		p := b.platform.(*linuxCredentialBroker)
		deadline := time.Now().Add(2 * time.Second)
		for {
			p.mu.Lock()
			n := len(p.conns)
			p.mu.Unlock()
			if n != 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("broker did not accept authenticated connection")
			}
			time.Sleep(time.Millisecond)
		}
		start := time.Now()
		_ = b.Close()
		if time.Since(start) > time.Second || cmd.Wait() != nil || diagnostics.Len() != 0 {
			t.Fatal("authenticated incomplete request was not revoked cleanly")
		}
	})
	t.Run("offline-dispatch-does-not-read-or-write-or-connect", func(t *testing.T) {
		var out, diagnostics bytes.Buffer
		handled, code := HelperMain(context.Background(), []string{"restore"}, credentialPanicReader{}, &out, &diagnostics)
		if handled || code != 0 || out.Len() != 0 || diagnostics.Len() != 0 {
			t.Fatal("ordinary offline operation entered credential helper")
		}
	})
	t.Run("malformed-private-invocation-refused-before-input", func(t *testing.T) {
		b, _, _ := credentialTestBroker(t, context.Background())
		good := append(b.HelperArguments(), "get")
		for _, args := range [][]string{
			{credentialHelperMode},
			append(append([]string(nil), good...), "extra"),
			{credentialHelperMode, "--socket", "relative", "--broker-pid", "1", "get"},
			{credentialHelperMode, "--socket", "/unused", "--broker-pid", "invalid", "get"},
			{credentialHelperMode, "--socket", "/unused", "--broker-pid", "0", "get"},
			{credentialHelperMode, "--socket", "/unused", "--broker-pid", "1", "store"},
		} {
			var out, diagnostics bytes.Buffer
			handled, code := HelperMain(context.Background(), args, credentialPanicReader{}, &out, &diagnostics)
			if !handled || code != 1 || out.Len() != 0 || diagnostics.Len() != 0 {
				t.Fatal("malformed helper invocation was not silently refused")
			}
		}
	})
	t.Run("constructor-rejects-unsafe-scopes", func(t *testing.T) {
		b, dir, secret := credentialTestBroker(t, context.Background())
		_ = b.Close()
		exe, _ := os.Executable()
		base := CredentialBrokerOptions{Context: context.Background(), PrivateDir: dir, AllowedHost: "github.com", AllowedRepository: "fixture/private", Username: "x-access-token", Secret: secret, Executable: exe}
		for _, mutate := range []func(*CredentialBrokerOptions){
			func(o *CredentialBrokerOptions) { o.AllowedHost = "evil.invalid" },
			func(o *CredentialBrokerOptions) { o.AllowedRepository = "fixture/../private" },
			func(o *CredentialBrokerOptions) { o.AllowedRepository = "Fixture/private" },
			func(o *CredentialBrokerOptions) { o.Secret = []byte("x\npassword=evil") },
			func(o *CredentialBrokerOptions) { o.Username = "x\npassword=evil" },
			func(o *CredentialBrokerOptions) { o.Executable = "/bin/sh" },
			func(o *CredentialBrokerOptions) { o.PrivateDir = filepath.Dir(dir) },
		} {
			o := base
			mutate(&o)
			candidate, err := NewCredentialBroker(o)
			if candidate != nil || !errors.Is(err, ErrCredentialDenied) {
				t.Fatal("unsafe constructor options accepted")
			}
		}
	})
}

type credentialPanicReader struct{}

func (credentialPanicReader) Read([]byte) (int, error) { panic("offline credential input read") }
