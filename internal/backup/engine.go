package backup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// EngineOptions captures all engine dependencies and finite operation budgets.
type EngineOptions struct {
	Root                                     string
	Runner                                   *GitRunner
	Limits                                   ManifestLimits
	VerificationScratchBytes                 int64
	InstallationID, StoreID, ApplicationData string
	Progress                                 chan<- ProgressEvent
	Filesystem                               filesystemOps // package-private deterministic fault seam
}

type Request struct {
	Source             string
	Repository         RepositoryIdentity
	ApplicationVersion string
	// ManagedGitHubMirror grants only the exact engine-owned stable-ID mirror
	// path as a snapshot source. The engine still validates and locks that mirror
	// under its managed root before reading it.
	ManagedGitHubMirror bool
	// SnapshotID is reserved by a durable coordinator before work starts. Empty
	// preserves the standalone engine's generated-ID behavior.
	SnapshotID string
}
type ProgressEvent struct {
	OperationID, RepositoryID, Stage string
	Sequence                         uint64
}

type Engine struct {
	root     string
	rootInfo os.FileInfo
	runner   *GitRunner
	options  EngineOptions
	fs       filesystemOps
}

var ErrEmptyRepository = fmt.Errorf("empty repository has no restorable snapshot")

func NewEngine(o EngineOptions) (*Engine, error) {
	root, err := canonicalDirectory(o.Root, false)
	if err != nil {
		return nil, fmt.Errorf("backup root: %w", err)
	}
	if err = validateDirectoryTrust(root); err != nil {
		return nil, fmt.Errorf("backup root trust boundary: %w", err)
	}
	if o.Runner == nil {
		return nil, fmt.Errorf("Git runner required")
	}
	rootInfo, err := os.Lstat(root)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return nil, ErrUnsafeRepository
	}
	if o.Limits == (ManifestLimits{}) {
		o.Limits = DefaultLimits()
	}
	if err := o.Limits.validate(); err != nil {
		return nil, err
	}
	if o.VerificationScratchBytes == 0 {
		o.VerificationScratchBytes = 250 << 30
	}
	if o.VerificationScratchBytes < o.Limits.BundleBytes || o.VerificationScratchBytes > 4<<40 {
		return nil, fmt.Errorf("invalid verification scratch budget")
	}
	fs := o.Filesystem
	if fs == nil {
		fs = osFilesystem{}
	}
	return &Engine{root: root, rootInfo: rootInfo, runner: o.Runner, options: o, fs: fs}, nil
}

func (e *Engine) validateRootIdentity() error {
	current, err := os.Lstat(e.root)
	if err != nil || !current.IsDir() || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(e.rootInfo, current) {
		return ErrUnsafeRepository
	}
	return validateDirectoryTrust(e.root)
}

// Create makes and verifies a full portable snapshot from a local repository.
func (e *Engine) Create(ctx context.Context, req Request) (Result, error) {
	if err := ctx.Err(); err != nil {
		return resultFailure(err)
	}
	if !safeRepository(req.Repository) || !text(req.ApplicationVersion, 256) {
		return resultFailure(ErrUnsafeRepository)
	}
	if req.SnapshotID != "" && !validID(req.SnapshotID) {
		return resultFailure(ErrUnsafeRepository)
	}
	source, err := canonicalDirectory(req.Source, false)
	if err != nil {
		return resultFailure(ErrUnsafeRepository)
	}
	managedMirror := req.ManagedGitHubMirror && req.Repository.Provider == "github" && canonicalGitHubRepositoryID(req.Repository.ID) && source == filepath.Join(e.root, "mirrors", "github", req.Repository.ID+".git")
	if req.ManagedGitHubMirror && !managedMirror {
		return resultFailure(ErrUnsafeRepository)
	}
	if pathsOverlap(source, e.root) && !managedMirror {
		return resultFailure(ErrUnsafeRepository)
	}
	unlock, err := e.lock(ctx, true)
	if err != nil {
		return resultFailure(err)
	}
	defer unlock()
	if managedMirror {
		unlockMirror, lockErr := lockGitHubMirror(ctx, filepath.Join(e.root, "mirrors", "github"), req.Repository.ID)
		if lockErr != nil {
			return resultFailure(lockErr)
		}
		defer unlockMirror()
	}
	if err = e.validateRootIdentity(); err != nil {
		return resultFailure(err)
	}
	if err = e.authorizeManagedMutation(); err != nil {
		return resultFailure(err)
	}
	if repositoryHeadMissing(source) {
		return resultFailure(ErrGitUnsupported)
	}
	if err = e.ensureManagedDirectories(req.Repository.Provider); err != nil {
		return resultFailure(err)
	}
	if _, err = e.runner.ValidateGitRepository(ctx, source); err != nil {
		return resultFailure(err)
	}
	initial, err := e.capture(ctx, source, req)
	if errors.Is(err, ErrEmptyRepository) {
		r, _ := NewResult(ResultFacts{Outcome: OutcomeEmptyRepository, Publication: NotPublished})
		return r, ErrEmptyRepository
	}
	if err != nil {
		return resultFailure(err)
	}
	opID, idErr := NewID()
	if idErr != nil {
		return resultFailure(idErr)
	}
	e.progress(opID, req.Repository.ID, "mirror", 1)
	scratch, err := e.fs.makeTemp(filepath.Join(e.root, "staging"), "create-")
	if err != nil {
		return resultFailure(err)
	}
	defer e.cleanup(scratch)
	mirror := filepath.Join(scratch, "mirror.git")
	if _, err = e.runner.command(ctx, "", gitLocalSource, nil, "clone", "--mirror", "--no-local", "--", source, mirror); err != nil {
		return resultFailure(err)
	}
	manifest, err := e.capture(ctx, mirror, req)
	if err != nil {
		if errors.Is(err, ErrEmptyRepository) {
			r, _ := NewResult(ResultFacts{Outcome: OutcomeEmptyRepository, Publication: NotPublished})
			return r, ErrEmptyRepository
		}
		return resultFailure(err)
	}
	if initial.GitObjectFormat != manifest.GitObjectFormat || initial.Head != manifest.Head || !sameMap(initial.Refs, manifest.Refs) {
		return resultFailure(ErrGitUnsupported)
	}
	if len(manifest.Refs) == 0 && manifest.Head.Kind == "unborn" {
		r, _ := NewResult(ResultFacts{Outcome: OutcomeEmptyRepository, Publication: NotPublished})
		return r, ErrEmptyRepository
	}
	id := req.SnapshotID
	if id == "" {
		id, err = NewID()
		if err != nil {
			return resultFailure(err)
		}
	}
	manifest.SnapshotID = id
	finalDir := filepath.Join(e.root, "snapshots", req.Repository.Provider, req.Repository.ID, id)
	stage := filepath.Join(e.root, "staging", id+".partial")
	if err = e.fs.mkdir(stage, 0700); err != nil {
		return resultFailure(err)
	}
	committed := false
	defer func() {
		if !committed {
			e.cleanup(stage)
		}
	}()
	bundlePath := filepath.Join(stage, BundleFilename)
	e.progress(opID, req.Repository.ID, "bundle", 2)
	// Bundle from the validated source so Git's special HEAD advertisement keeps
	// detached-only history. The disposable no-local mirror remains acquisition state.
	if _, err = e.runner.command(ctx, mirror, gitOffline, nil, "bundle", "create", bundlePath, "--all"); err != nil {
		return resultFailure(err)
	}
	if err = e.verifyBundle(ctx, bundlePath, manifest); err != nil {
		return resultFailure(err)
	}
	e.progress(opID, req.Repository.ID, "verified", 3)
	info, err := os.Stat(bundlePath)
	if err != nil {
		return resultFailure(err)
	}
	if info.Size() > e.options.Limits.BundleBytes {
		return resultFailure(ErrGitOutputLimit)
	}
	hash, size, err := hashFile(ctx, bundlePath, e.options.Limits.BundleBytes)
	if err != nil {
		return resultFailure(err)
	}
	manifest.Bundle = Bundle{File: BundleFilename, Kind: "full", SHA256: hash, SizeBytes: size, Prerequisites: []string{}}
	manifestBytes, err := manifest.Marshal(e.options.Limits)
	if err != nil {
		return resultFailure(err)
	}
	if err = e.fs.writeSynced(filepath.Join(stage, "manifest.json"), manifestBytes, 0600); err != nil {
		return resultFailure(err)
	}
	if err = e.fs.syncDirectory(stage); err != nil {
		return resultFailure(err)
	}
	if err = e.ensureSnapshotParent(req.Repository.Provider, req.Repository.ID); err != nil {
		return resultFailure(err)
	}
	if err = e.validateRootIdentity(); err != nil {
		return resultFailure(err)
	}
	if err = e.fs.publish(stage, finalDir); err != nil {
		return resultFailure(err)
	}
	committed = true
	e.progress(opID, req.Repository.ID, "published", 4)
	identity := ArtifactIdentity{id, req.Repository.Provider, req.Repository.ID, finalDir}
	state := Published
	if err = e.fs.syncDirectory(filepath.Dir(finalDir)); err != nil {
		state = PublishedDurabilityUncertain
		r, _ := NewResult(ResultFacts{Outcome: OutcomeFailed, Publication: state, Artifact: identity, Verification: Verification{Manifest: CheckPassed, Hash: CheckPassed, IndependentRefs: CheckPassed, Head: CheckPassed, ObjectIntegrity: CheckPassed, OfflineRestore: CheckPassed}, Warnings: []Warning{{"directory_sync_uncertain", "Snapshot is visible; directory durability could not be confirmed."}}})
		return r, err
	}
	outcome := OutcomeSucceeded
	if ctx.Err() != nil {
		outcome = OutcomeCancelled
	}
	r, _ := NewResult(ResultFacts{Outcome: outcome, Publication: state, Artifact: identity, Verification: Verification{Manifest: CheckPassed, Hash: CheckPassed, IndependentRefs: CheckPassed, Head: CheckPassed, ObjectIntegrity: CheckPassed, OfflineRestore: CheckPassed}})
	return r, ctx.Err()
}

func repositoryHeadMissing(root string) bool {
	gitdir := root
	if st, err := os.Lstat(filepath.Join(root, ".git")); err == nil && st.IsDir() && st.Mode()&os.ModeSymlink == 0 {
		gitdir = filepath.Join(root, ".git")
	}
	if _, err := os.Lstat(filepath.Join(gitdir, "HEAD")); !os.IsNotExist(err) {
		return false
	}
	for _, name := range []string{"config", "objects", "refs"} {
		st, err := os.Lstat(filepath.Join(gitdir, name))
		if err != nil || st.Mode()&os.ModeSymlink != 0 {
			return false
		}
		if name == "config" && !st.Mode().IsRegular() || name != "config" && !st.IsDir() {
			return false
		}
	}
	return true
}

func resultFailure(err error) (Result, error) {
	r, _ := NewResult(ResultFacts{Outcome: OutcomeFailed, Publication: NotPublished})
	return r, err
}
func (e *Engine) progress(id, repo, stage string, seq uint64) {
	if e.options.Progress != nil {
		select {
		case e.options.Progress <- ProgressEvent{id, repo, stage, seq}:
		default:
		}
	}
}
func (e *Engine) cleanup(path string) {
	if path != "" && e.validateRootIdentity() == nil {
		_ = e.fs.removeAll(path)
	}
}
func pathsOverlap(a, b string) bool {
	r, err := filepath.Rel(a, b)
	if err == nil && (r == "." || r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator))) {
		return true
	}
	r, err = filepath.Rel(b, a)
	return err == nil && (r == "." || r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)))
}
