package backup

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type GitFormatCapabilities struct {
	ObjectFormat    string
	OIDHexLength    int
	FullBundle      bool
	IndependentRefs bool
	DetachedHead    bool
	ObjectIntegrity bool
}
type GitCapabilities struct{ SHA1, SHA256 GitFormatCapabilities }

// ProbeCapabilities proves operations in owned disposable repositories. It
// creates no snapshot, uses no external repository/configuration/network, and
// does not infer capability from a version string. Both formats are required by
// this qualification probe; partial evidence accompanies an unsupported error.
func (r *GitRunner) ProbeCapabilities(ctx context.Context) (GitCapabilities, error) {
	var caps GitCapabilities
	root, err := os.MkdirTemp(r.scratchRoot, "git-capabilities-")
	if err != nil {
		return caps, ErrGitUnavailable
	}
	defer os.RemoveAll(root)
	for _, format := range []string{"sha1", "sha256"} {
		c, err := r.probeFormat(ctx, root, format)
		if format == "sha1" {
			caps.SHA1 = c
		} else {
			caps.SHA256 = c
		}
		if err != nil {
			return caps, err
		}
	}
	return caps, nil
}

func (r *GitRunner) probeFormat(ctx context.Context, root, format string) (GitFormatCapabilities, error) {
	c := GitFormatCapabilities{ObjectFormat: format, OIDHexLength: 40}
	if format == "sha256" {
		c.OIDHexLength = 64
	}
	repo := filepath.Join(root, format+"-source.git")
	if _, err := r.command(ctx, "", gitOffline, nil, "init", "--bare", "--object-format="+format, "--initial-branch=qualification", "--", repo); err != nil {
		return c, fmt.Errorf("object-format initialization capability: %w", err)
	}
	treeOut, err := r.command(ctx, repo, gitOffline, []byte{}, "mktree")
	if err != nil {
		return c, err
	}
	tree := strings.TrimSpace(string(treeOut.stdout))
	if !validOID(tree, c.OIDHexLength) {
		return c, ErrGitUnsupported
	}
	makeCommit := func(message string) (string, error) {
		out, err := r.command(ctx, repo, gitOffline, []byte(message), "-c", "user.name=Synthetic Qualification", "-c", "user.email=qualification@example.invalid", "-c", "commit.gpgSign=false", "commit-tree", tree)
		if err != nil {
			return "", err
		}
		oid := strings.TrimSpace(string(out.stdout))
		if !validOID(oid, c.OIDHexLength) {
			return "", ErrGitUnsupported
		}
		return oid, nil
	}
	branch, err := makeCommit("named history\n")
	if err != nil {
		return c, err
	}
	detached, err := makeCommit("detached-only history\n")
	if err != nil {
		return c, err
	}
	for _, args := range [][]string{{"update-ref", "refs/heads/qualification", branch}, {"update-ref", "--no-deref", "HEAD", detached}} {
		if _, err := r.command(ctx, repo, gitOffline, nil, args...); err != nil {
			return c, err
		}
	}
	mirror := filepath.Join(root, format+"-mirror.git")
	if _, err := r.command(ctx, "", gitLocalSource, nil, "clone", "--mirror", "--no-local", "--", repo, mirror); err != nil {
		return c, err
	}
	for _, check := range []struct {
		args []string
		want string
	}{
		{[]string{"rev-parse", "--show-object-format"}, format},
		{[]string{"rev-parse", "--verify", "HEAD"}, detached},
		{[]string{"rev-parse", "--verify", "refs/heads/qualification"}, branch},
	} {
		out, err := r.command(ctx, mirror, gitOffline, nil, check.args...)
		if err != nil {
			return c, err
		}
		if strings.TrimSpace(string(out.stdout)) != check.want {
			return c, ErrGitUnsupported
		}
	}
	bundle := filepath.Join(root, format+".bundle")
	if _, err := r.command(ctx, mirror, gitOffline, nil, "bundle", "create", bundle, "--all"); err != nil {
		return c, err
	}
	fresh := filepath.Join(root, format+"-independent.git")
	if _, err := r.command(ctx, "", gitOffline, nil, "init", "--bare", "--object-format="+format, "--initial-branch=qualification", "--", fresh); err != nil {
		return c, err
	}
	if _, err := r.command(ctx, fresh, gitOffline, nil, "bundle", "verify", bundle); err != nil {
		return c, err
	}
	c.FullBundle = true
	out, err := r.command(ctx, fresh, gitOffline, nil, "bundle", "unbundle", bundle)
	if err != nil {
		return c, err
	}
	if !strings.Contains(string(out.stdout), branch+" refs/heads/qualification\n") || !strings.Contains(string(out.stdout), detached+" HEAD\n") {
		return c, ErrGitUnsupported
	}
	for _, args := range [][]string{{"update-ref", "refs/heads/qualification", branch}, {"update-ref", "--no-deref", "HEAD", detached}} {
		if _, err := r.command(ctx, fresh, gitOffline, nil, args...); err != nil {
			return c, err
		}
	}
	out, err = r.command(ctx, fresh, gitOffline, nil, "for-each-ref", "--format=%(objectname) %(refname)")
	if err != nil {
		return c, err
	}
	if string(out.stdout) != branch+" refs/heads/qualification\n" {
		return c, ErrGitUnsupported
	}
	c.IndependentRefs = true
	out, err = r.command(ctx, fresh, gitOffline, nil, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return c, err
	}
	if strings.TrimSpace(string(out.stdout)) != detached {
		return c, ErrGitUnsupported
	}
	c.DetachedHead = true
	if _, err := r.command(ctx, fresh, gitOffline, nil, "fsck", "--full", "--no-reflogs"); err != nil {
		return c, err
	}
	c.ObjectIntegrity = true
	return c, nil
}
