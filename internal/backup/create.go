package backup

import (
	"context"
	"fmt"
	"strings"
	"time"
)

func (e *Engine) capture(ctx context.Context, mirror string, req Request) (Manifest, error) {
	repo, err := e.runner.ValidateGitRepository(ctx, mirror)
	if err != nil {
		return Manifest{}, err
	}
	refsOut, err := e.runner.command(ctx, mirror, gitOffline, nil, "for-each-ref", "--format=%(objectname) %(refname)")
	if err != nil {
		return Manifest{}, err
	}
	refs := map[string]string{}
	for _, line := range strings.Split(strings.TrimSuffix(string(refsOut.stdout), "\n"), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, " ", 2)
		if len(parts) != 2 || !ValidRefName(parts[1]) {
			return Manifest{}, ErrGitUnsupported
		}
		refs[parts[1]] = parts[0]
	}
	head := Head{}
	out, err := e.runner.command(ctx, mirror, gitOffline, nil, "symbolic-ref", "-q", "HEAD")
	if err == nil {
		target := strings.TrimSpace(string(out.stdout))
		_, ok := refs[target]
		if ok {
			head = Head{Kind: "symbolic", Target: target}
		} else if strings.HasPrefix(target, "refs/heads/") {
			head = Head{Kind: "unborn", Target: target}
		} else {
			return Manifest{}, ErrGitUnsupported
		}
	} else {
		out, err = e.runner.command(ctx, mirror, gitOffline, nil, "rev-parse", "--verify", "HEAD")
		if err != nil {
			return Manifest{}, ErrGitUnsupported
		}
		oid := strings.TrimSpace(string(out.stdout))
		n := 40
		if repo.ObjectFormat == "sha256" {
			n = 64
		}
		if !validOID(oid, n) {
			return Manifest{}, ErrGitUnsupported
		}
		head = Head{Kind: "detached", OID: oid}
	}
	if head.Kind == "unborn" && len(refs) == 0 {
		return Manifest{}, ErrEmptyRepository
	}
	id, err := NewID()
	if err != nil {
		return Manifest{}, err
	}
	app := req.ApplicationVersion
	if app == "" {
		app = "0.1.0"
	}
	m := Manifest{FormatVersion: 1, SnapshotID: id, CreatedAt: time.Now().UTC(), ApplicationVersion: app, Repository: req.Repository, GitObjectFormat: repo.ObjectFormat, Bundle: Bundle{File: BundleFilename, Kind: "full", SHA256: strings.Repeat("0", 64), SizeBytes: 0, Prerequisites: []string{}}, Head: head, Refs: refs, Coverage: FullCoverage()}
	if err = m.Validate(e.options.Limits); err != nil {
		return Manifest{}, fmt.Errorf("capture manifest: %w", err)
	}
	return m, nil
}
