package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"path/filepath"
	"strings"
)

func (e *Engine) verifyBundle(ctx context.Context, bundle string, m Manifest) error {
	listed, err := e.runner.command(ctx, "", gitOffline, nil, "bundle", "list-heads", bundle)
	if err != nil {
		return err
	}
	advertised, err := parseBundleHeads(string(listed.stdout))
	if err != nil || !sameMap(advertised, expectedBundleHeads(m)) {
		return ErrGitUnsupported
	}
	tmp, err := e.fs.makeTemp(filepath.Join(e.root, "staging"), "verify-")
	if err != nil {
		return err
	}
	defer e.cleanup(tmp)
	fresh := tmp + ".git"
	defer e.cleanup(fresh)
	if _, err = e.runner.command(ctx, "", gitOffline, nil, "init", "--bare", "--object-format="+m.GitObjectFormat, "--", fresh); err != nil {
		return err
	}
	if _, err = e.runner.command(ctx, fresh, gitOffline, nil, "bundle", "verify", bundle); err != nil {
		return err
	}
	out, err := e.runner.command(ctx, fresh, gitOffline, nil, "bundle", "unbundle", bundle)
	if err != nil {
		return err
	}
	for ref, oid := range m.Refs {
		if _, ok := outLine(string(out.stdout), oid+" "+ref); !ok {
			return ErrGitUnsupported
		}
	}
	if m.Head.Kind == "detached" {
		if _, ok := outLine(string(out.stdout), m.Head.OID+" HEAD"); !ok {
			return ErrGitUnsupported
		}
	}
	for ref, oid := range m.Refs {
		if _, err = e.runner.command(ctx, fresh, gitOffline, nil, "update-ref", ref, oid); err != nil {
			return err
		}
	}
	switch m.Head.Kind {
	case "symbolic", "unborn":
		_, err = e.runner.command(ctx, fresh, gitOffline, nil, "symbolic-ref", "HEAD", m.Head.Target)
	case "detached":
		_, err = e.runner.command(ctx, fresh, gitOffline, nil, "update-ref", "--no-deref", "HEAD", m.Head.OID)
	}
	if err != nil {
		return err
	}
	refs, err := e.runner.command(ctx, fresh, gitOffline, nil, "for-each-ref", "--format=%(objectname) %(refname)")
	if err != nil {
		return err
	}
	got := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(refs.stdout)), "\n") {
		p := strings.SplitN(line, " ", 2)
		if len(p) == 2 {
			got[p[1]] = p[0]
		}
	}
	if !sameMap(got, m.Refs) {
		return ErrGitUnsupported
	}
	if m.Head.Kind == "symbolic" || m.Head.Kind == "unborn" {
		o, er := e.runner.command(ctx, fresh, gitOffline, nil, "symbolic-ref", "-q", "HEAD")
		if er != nil || strings.TrimSpace(string(o.stdout)) != m.Head.Target {
			return ErrGitUnsupported
		}
	}
	if _, err = e.runner.command(ctx, fresh, gitOffline, nil, "fsck", "--full", "--no-reflogs"); err != nil {
		return err
	}
	return nil
}

func expectedBundleHeads(m Manifest) map[string]string {
	want := make(map[string]string, len(m.Refs)+1)
	for ref, oid := range m.Refs {
		want[ref] = oid
	}
	switch m.Head.Kind {
	case "detached":
		want["HEAD"] = m.Head.OID
	case "symbolic":
		if oid, ok := m.Refs[m.Head.Target]; ok {
			want["HEAD"] = oid
		}
	}
	return want
}

func parseBundleHeads(output string) (map[string]string, error) {
	got := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) != 2 || !validOID(parts[0], 40) && !validOID(parts[0], 64) {
			return nil, ErrGitUnsupported
		}
		if _, exists := got[parts[1]]; exists {
			return nil, ErrGitUnsupported
		}
		got[parts[1]] = parts[0]
	}
	return got, nil
}
func zeroOID(format string) string {
	if format == "sha256" {
		return strings.Repeat("0", 64)
	}
	return strings.Repeat("0", 40)
}
func outLine(s, want string) (string, bool) {
	for _, l := range strings.Split(s, "\n") {
		if l == want {
			return l, true
		}
	}
	return "", false
}
func sameMap(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
func hashFile(ctx context.Context, path string, max int64) (string, int64, error) {
	f, e := openRegularNoFollow(path)
	if e != nil {
		return "", 0, e
	}
	defer f.Close()
	h := sha256.New()
	n, e := io.Copy(h, &contextReader{ctx: ctx, r: io.LimitReader(f, max+1)})
	if e != nil {
		return "", n, e
	}
	if n > max {
		return "", n, ErrGitOutputLimit
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *contextReader) Read(p []byte) (int, error) {
	if e := c.ctx.Err(); e != nil {
		return 0, e
	}
	return c.r.Read(p)
}
