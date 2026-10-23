package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Inspect parses a snapshot manifest without granting mutation or deletion authority.
func (e *Engine) Inspect(ctx context.Context, snapshot string) (Manifest, error) {
	if err := ctx.Err(); err != nil {
		return Manifest{}, err
	}
	p, err := canonicalDirectory(snapshot, false)
	if err != nil {
		return Manifest{}, ErrUnsafeRepository
	}
	f, err := openRegularNoFollow(filepath.Join(p, "manifest.json"))
	if err != nil {
		return Manifest{}, err
	}
	defer f.Close()
	return ReadManifest(f, e.options.Limits)
}

// Verify checks the manifest, payload checksum and an independent full import.
func (e *Engine) Verify(ctx context.Context, snapshot string) (Verification, error) {
	m, err := e.Inspect(ctx, snapshot)
	if err != nil {
		return Verification{}, err
	}
	bundle := filepath.Join(snapshot, BundleFilename)
	h, n, err := hashFile(ctx, bundle, e.options.Limits.BundleBytes)
	if err != nil {
		return Verification{}, err
	}
	if n != m.Bundle.SizeBytes || h != m.Bundle.SHA256 {
		return Verification{}, fmt.Errorf("bundle checksum mismatch")
	}
	if err = e.verifyBundle(ctx, bundle, m); err != nil {
		return Verification{}, err
	}
	return Verification{Manifest: CheckPassed, Hash: CheckPassed, IndependentRefs: CheckPassed, Head: CheckPassed, ObjectIntegrity: CheckPassed, OfflineRestore: CheckPassed}, nil
}

// Restore privately copies the snapshot, then publishes a new bare repository without replacement.
func (e *Engine) Restore(ctx context.Context, input, newDestination string) (Result, error) {
	if err := ctx.Err(); err != nil {
		return resultFailure(err)
	}
	if !filepath.IsAbs(newDestination) || strings.ContainsAny(newDestination, "\x00\r\n") {
		return resultFailure(ErrUnsafeRepository)
	}
	dest := filepath.Clean(newDestination)
	parent, err := canonicalDirectory(filepath.Dir(dest), false)
	if err != nil || validateDirectoryTrust(parent) != nil || pathsOverlap(dest, e.root) || pathsOverlap(input, dest) {
		return resultFailure(ErrUnsafeRepository)
	}
	unlock, err := e.lock(ctx, false)
	if err != nil {
		return resultFailure(err)
	}
	defer unlock()
	if err = e.validateRootIdentity(); err != nil {
		return resultFailure(err)
	}
	input, err = canonicalDirectory(input, false)
	if err != nil {
		return resultFailure(ErrUnsafeRepository)
	}
	stage, err := e.fs.makeTemp(filepath.Join(e.root, "staging"), "restore-")
	if err != nil {
		return resultFailure(err)
	}
	defer e.cleanup(stage)
	manifestPath := filepath.Join(input, "manifest.json")
	privManifest := filepath.Join(stage, "manifest.json")
	if err = e.fs.copyBounded(ctx, manifestPath, privManifest, e.options.Limits.ManifestBytes); err != nil {
		return resultFailure(err)
	}
	mfile, err := os.Open(privManifest)
	if err != nil {
		return resultFailure(err)
	}
	m, err := ReadManifest(mfile, e.options.Limits)
	ce := mfile.Close()
	if err != nil {
		return resultFailure(err)
	}
	if ce != nil {
		return resultFailure(ce)
	}
	bundlePath := filepath.Join(input, BundleFilename)
	privateBundle := filepath.Join(stage, BundleFilename)
	if err = e.fs.copyBounded(ctx, bundlePath, privateBundle, e.options.Limits.BundleBytes); err != nil {
		return resultFailure(err)
	}
	sha, n, err := hashFile(ctx, privateBundle, e.options.Limits.BundleBytes)
	if err != nil || n != m.Bundle.SizeBytes || sha != m.Bundle.SHA256 {
		if err == nil {
			err = fmt.Errorf("bundle checksum mismatch")
		}
		return resultFailure(err)
	}
	if err = e.verifyBundle(ctx, privateBundle, m); err != nil {
		return resultFailure(err)
	}
	fresh := filepath.Join(stage, "restored.git")
	if _, err = e.runner.command(ctx, "", gitOffline, nil, "init", "--bare", "--object-format="+m.GitObjectFormat, "--", fresh); err != nil {
		return resultFailure(err)
	}
	if _, err = e.runner.command(ctx, fresh, gitOffline, nil, "bundle", "verify", privateBundle); err != nil {
		return resultFailure(err)
	}
	out, err := e.runner.command(ctx, fresh, gitOffline, nil, "bundle", "unbundle", privateBundle)
	if err != nil {
		return resultFailure(err)
	}
	for ref, oid := range m.Refs {
		if _, ok := outLine(string(out.stdout), oid+" "+ref); !ok {
			return resultFailure(ErrGitUnsupported)
		}
		if _, err = e.runner.command(ctx, fresh, gitOffline, nil, "update-ref", ref, oid); err != nil {
			return resultFailure(err)
		}
	}
	switch m.Head.Kind {
	case "symbolic", "unborn":
		_, err = e.runner.command(ctx, fresh, gitOffline, nil, "symbolic-ref", "HEAD", m.Head.Target)
	case "detached":
		_, err = e.runner.command(ctx, fresh, gitOffline, nil, "update-ref", "--no-deref", "HEAD", m.Head.OID)
	default:
		err = ErrGitUnsupported
	}
	if err != nil {
		return resultFailure(err)
	}
	if _, err = e.runner.command(ctx, fresh, gitOffline, nil, "fsck", "--full", "--no-reflogs"); err != nil {
		return resultFailure(err)
	}
	if err = e.fs.syncTree(fresh); err != nil {
		return resultFailure(err)
	}
	if err = e.validateRootIdentity(); err != nil {
		return resultFailure(err)
	}
	if err = e.fs.publish(fresh, filepath.Join(parent, filepath.Base(dest))); err != nil {
		return resultFailure(err)
	}
	identity := ArtifactIdentity{SnapshotID: m.SnapshotID, Provider: m.Repository.Provider, RepositoryID: m.Repository.ID, Path: dest}
	state := Published
	if err = e.fs.syncDirectory(parent); err != nil {
		state = PublishedDurabilityUncertain
		r, _ := NewResult(ResultFacts{Outcome: OutcomeFailed, Publication: state, Artifact: identity, Warnings: []Warning{{"directory_sync_uncertain", "Restoration is visible; directory durability could not be confirmed."}}})
		return r, err
	}
	if ctx.Err() != nil {
		r, _ := NewResult(ResultFacts{Outcome: OutcomeCancelled, Publication: state, Artifact: identity})
		return r, ctx.Err()
	}
	r, _ := NewResult(ResultFacts{Outcome: OutcomeSucceeded, Publication: state, Artifact: identity, Verification: Verification{Manifest: CheckPassed, Hash: CheckPassed, IndependentRefs: CheckPassed, Head: CheckPassed, ObjectIntegrity: CheckPassed, OfflineRestore: CheckPassed}})
	return r, nil
}

func copyBounded(ctx context.Context, src, dst string, max int64) error {
	in, e := openRegularNoFollow(src)
	if e != nil {
		return e
	}
	defer in.Close()
	st, e := in.Stat()
	if e != nil || st.Size() > max {
		return ErrUnsafeRepository
	}
	out, e := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	n, e := io.Copy(out, &contextReader{ctx: ctx, r: io.LimitReader(in, max+1)})
	if e == nil && n > max {
		e = ErrGitOutputLimit
	}
	if e == nil {
		e = out.Sync()
	}
	ce := out.Close()
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	return nil
}
func syncTree(root string) error {
	return filepath.Walk(root, func(p string, i os.FileInfo, e error) error {
		if e != nil {
			return e
		}
		if i.Mode().IsRegular() {
			f, er := os.Open(p)
			if er != nil {
				return er
			}
			er = f.Sync()
			ce := f.Close()
			if er != nil {
				return er
			}
			if ce != nil {
				return ce
			}
		}
		if i.IsDir() {
			return syncDirectory(p)
		}
		return nil
	})
}
func fileDigest(path string) (string, error) {
	b, e := os.ReadFile(path)
	if e != nil {
		return "", e
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}
