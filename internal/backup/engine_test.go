package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func testEngine(t *testing.T, root string, events chan<- ProgressEvent) *Engine {
	t.Helper()
	if err := os.Chmod(filepath.Dir(root), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	scratch := t.TempDir()
	if err := os.Chmod(scratch, 0700); err != nil {
		t.Fatal(err)
	}
	r, err := NewGitRunner(scratch, DefaultGitRunnerOptions())
	if err != nil {
		t.Fatal(err)
	}
	e, err := NewEngine(EngineOptions{Root: root, Runner: r, Progress: events})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func privateTempDir(t *testing.T) string {
	t.Helper()
	p := t.TempDir()
	if err := os.Chmod(p, 0700); err != nil {
		t.Fatal(err)
	}
	return p
}
func testRepo(t *testing.T) string {
	return testRepoFormat(t, "sha1")
}

func testRepoFormat(t *testing.T, format string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "source")
	runFixtureGit(t, "init", "--bare", "--initial-branch=main", "--object-format="+format, p)
	// Construct a regular bare repository with one real tree and commit.
	tb := runFixtureGit(t, "--git-dir="+p, "mktree")
	cb := runFixtureGit(t, "--git-dir="+p, "-c", "user.name=Fixture", "-c", "user.email=f@example.invalid", "commit-tree", stringTrim(tb), "-m", "first")
	runFixtureGit(t, "--git-dir="+p, "update-ref", "refs/heads/main", stringTrim(cb))
	return p
}

func runFixtureGit(t *testing.T, args ...string) []byte {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return out
}

func testDetachedRepo(t *testing.T, format string, tagOnly bool) (string, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "working")
	runFixtureGit(t, "init", "--initial-branch=main", "--object-format="+format, p)
	if err := os.WriteFile(filepath.Join(p, "history.txt"), []byte("history\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, "-C", p, "add", "history.txt")
	runFixtureGit(t, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-C", p, "commit", "-m", "first")
	oid := stringTrim(runFixtureGit(t, "-C", p, "rev-parse", "HEAD"))
	runFixtureGit(t, "-C", p, "checkout", "--detach", oid)
	if tagOnly {
		runFixtureGit(t, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-C", p, "tag", "-a", "v1", oid, "-m", "release")
	}
	runFixtureGit(t, "-C", p, "branch", "-D", "main")
	return p, oid
}

func stringTrim(b []byte) string { return strings.TrimSuffix(string(b), "\n") }

func TestOfflineRoundTrip(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	e := testEngine(t, root, nil)
	src := testRepo(t)
	r, err := e.Create(context.Background(), Request{Source: src, Repository: RepositoryIdentity{Provider: "local", ID: mustID(t), DisplayName: "fixture"}, ApplicationVersion: "test"})
	if err != nil {
		t.Fatal(err)
	}
	// Recovery must depend only on the published bundle, not the original source.
	if err = os.RemoveAll(src); err != nil {
		t.Fatal(err)
	}
	if r.Publication() != Published {
		t.Fatalf("publication=%s", r.Publication())
	}
	if err = os.RemoveAll(src); err != nil {
		t.Fatalf("make original source unavailable: %v", err)
	}
	dest := filepath.Join(privateTempDir(t), "restored.git")
	rr, err := e.Restore(context.Background(), r.Artifact().Path, dest)
	if err != nil {
		t.Fatal(err)
	}
	if rr.Publication() != Published {
		t.Fatalf("restore=%s", rr.Publication())
	}
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return stringTrim(out)
	}
	manifest, err := e.Inspect(context.Background(), r.Artifact().Path)
	if err != nil {
		t.Fatal(err)
	}
	refs := git("--git-dir="+dest, "for-each-ref", "--format=%(objectname) %(refname)")
	want := map[string]string{}
	for _, line := range splitLines(refs) {
		if line != "" {
			parts := strings.SplitN(line, " ", 2)
			if len(parts) == 2 {
				want[parts[1]] = parts[0]
			}
		}
	}
	if !sameMap(want, manifest.Refs) {
		t.Fatalf("restored refs differ: got %v want %v", want, manifest.Refs)
	}
	head := git("--git-dir="+dest, "symbolic-ref", "-q", "HEAD")
	if manifest.Head.Kind == "symbolic" && head != manifest.Head.Target {
		t.Fatalf("HEAD=%q want %q", head, manifest.Head.Target)
	}
	git("--git-dir="+dest, "fsck", "--full", "--no-reflogs")
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
func mustID(t *testing.T) string {
	id, e := NewID()
	if e != nil {
		t.Fatal(e)
	}
	return id
}

func TestSnapshotRefAndHeadCoverage(t *testing.T) {
	for _, tc := range []struct {
		name, format, head string
		tagOnly            bool
	}{{"sha1-symbolic", "sha1", "symbolic", false}, {"sha1-detached-tag-only", "sha1", "detached", true}, {"sha256-detached", "sha256", "detached", false}} {
		t.Run(tc.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "root")
			src, oid := testRepoFormat(t, tc.format), ""
			if tc.head == "detached" {
				src, oid = testDetachedRepo(t, tc.format, tc.tagOnly)
			}
			e := testEngine(t, root, nil)
			r, err := e.Create(context.Background(), Request{Source: src, Repository: RepositoryIdentity{Provider: "local", ID: mustID(t), DisplayName: "fixture"}, ApplicationVersion: "test"})
			if err != nil {
				t.Fatal(err)
			}
			m, err := e.Inspect(context.Background(), r.Artifact().Path)
			if err != nil {
				t.Fatal(err)
			}
			if m.GitObjectFormat != tc.format || m.Head.Kind != tc.head {
				t.Fatalf("format/head=%s/%#v", m.GitObjectFormat, m.Head)
			}
			if tc.tagOnly {
				if len(m.Refs) != 1 || m.Refs["refs/tags/v1"] == "" {
					t.Fatalf("tag-only refs=%v", m.Refs)
				}
			} else if tc.head == "symbolic" && m.Refs["refs/heads/main"] == "" {
				t.Fatalf("missing branch: %v", m.Refs)
			} else if tc.head == "detached" && len(m.Refs) != 0 {
				t.Fatalf("detached-only refs unexpectedly include refs: %v", m.Refs)
			}
			dest := filepath.Join(privateTempDir(t), "restored.git")
			if _, err = e.Restore(context.Background(), r.Artifact().Path, dest); err != nil {
				t.Fatal(err)
			}
			if tc.head == "detached" {
				got := stringTrim(runFixtureGit(t, "--git-dir="+dest, "rev-parse", "--verify", "HEAD"))
				if got != oid {
					t.Fatalf("detached HEAD=%s want %s", got, oid)
				}
			}
		})
	}
	t.Run("absent HEAD is explicit and publishes nothing", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "root")
		if err := os.Mkdir(root, 0700); err != nil {
			t.Fatal(err)
		}
		src := testRepoFormat(t, "sha1")
		if err := os.Remove(filepath.Join(src, "HEAD")); err != nil {
			t.Fatal(err)
		}
		e := testEngine(t, root, nil)
		r, err := e.Create(context.Background(), Request{Source: src, Repository: RepositoryIdentity{Provider: "local", ID: mustID(t), DisplayName: "absent-head"}, ApplicationVersion: "test"})
		if !errors.Is(err, ErrGitUnsupported) || r.Publication() != NotPublished || r.Artifact() != (ArtifactIdentity{}) {
			t.Fatalf("absent HEAD result=%+v err=%v", r, err)
		}
		entries, err := os.ReadDir(filepath.Join(root, "staging"))
		if err != nil && !os.IsNotExist(err) || len(entries) != 0 {
			t.Fatalf("absent HEAD left staging: %v %v", entries, err)
		}
	})
	t.Run("empty unborn repository is not a snapshot", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "root")
		if err := os.Mkdir(root, 0700); err != nil {
			t.Fatal(err)
		}
		src := filepath.Join(t.TempDir(), "empty.git")
		runFixtureGit(t, "init", "--bare", "--initial-branch=main", src)
		e := testEngine(t, root, nil)
		r, err := e.Create(context.Background(), Request{Source: src, Repository: RepositoryIdentity{Provider: "local", ID: mustID(t), DisplayName: "empty"}, ApplicationVersion: "test"})
		if !errors.Is(err, ErrEmptyRepository) || r.Outcome() != OutcomeEmptyRepository || r.Publication() != NotPublished {
			t.Fatalf("empty result=%+v err=%v", r, err)
		}
	})
	m := Manifest{FormatVersion: 1, SnapshotID: mustID(t), CreatedAt: time.Now().UTC(), ApplicationVersion: "test", Repository: RepositoryIdentity{Provider: "local", ID: mustID(t), DisplayName: "fixture"}, GitObjectFormat: "sha1", Bundle: Bundle{File: BundleFilename, Kind: "full", SHA256: strings.Repeat("a", 64), SizeBytes: 1, Prerequisites: []string{}}, Head: Head{Kind: "absent"}, Refs: map[string]string{}, Coverage: FullCoverage()}
	if err := m.Validate(DefaultLimits()); err != nil {
		t.Fatalf("absent HEAD is a valid representation: %v", err)
	}
}

func TestDeletedRefDoesNotLeakIntoLaterSnapshot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	e := testEngine(t, root, nil)
	src := testRepo(t)
	mainOID := stringTrim(runFixtureGit(t, "--git-dir="+src, "rev-parse", "refs/heads/main"))
	runFixtureGit(t, "--git-dir="+src, "update-ref", "refs/tags/keep", mainOID)
	request := Request{Source: src, Repository: RepositoryIdentity{Provider: "local", ID: mustID(t), DisplayName: "fixture"}, ApplicationVersion: "test"}
	first, err := e.Create(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	firstPath := first.Artifact().Path
	if err = runFixtureGitError("--git-dir="+src, "update-ref", "-d", "refs/heads/main"); err != nil {
		t.Fatal(err)
	}
	second, err := e.Create(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	old, err := e.Inspect(context.Background(), firstPath)
	if err != nil {
		t.Fatal(err)
	}
	newer, err := e.Inspect(context.Background(), second.Artifact().Path)
	if err != nil {
		t.Fatal(err)
	}
	if old.Refs["refs/heads/main"] == "" {
		t.Fatalf("first immutable snapshot lost deleted ref: %v", old.Refs)
	}
	if _, exists := newer.Refs["refs/heads/main"]; exists {
		t.Fatalf("later snapshot resurrected deleted ref: %v", newer.Refs)
	}
	if newer.Refs["refs/tags/keep"] != mainOID {
		t.Fatalf("later snapshot lost surviving tag: %v", newer.Refs)
	}
	restored := filepath.Join(privateTempDir(t), "old.git")
	if _, err = e.Restore(context.Background(), firstPath, restored); err != nil {
		t.Fatalf("old snapshot no longer restores: %v", err)
	}
	if got := stringTrim(runFixtureGit(t, "--git-dir="+restored, "for-each-ref", "--format=%(refname)")); got != "refs/heads/main\nrefs/tags/keep" {
		t.Fatalf("restored old ref=%q", got)
	}
}

func runFixtureGitError(args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0")
	return cmd.Run()
}

func TestValidDanglingSourceObjectsDoNotBreakFullSnapshot(t *testing.T) {
	root := filepath.Join(privateTempDir(t), "root")
	src := testRepo(t)
	tree := stringTrim(runFixtureGit(t, "--git-dir="+src, "mktree"))
	dangling := stringTrim(runFixtureGit(t, "--git-dir="+src, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit-tree", tree, "-m", "unreferenced valid object"))
	e := testEngine(t, root, nil)
	r, err := e.Create(context.Background(), Request{Source: src, Repository: RepositoryIdentity{Provider: "local", ID: mustID(t), DisplayName: "dangling-object"}, ApplicationVersion: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Verify(context.Background(), r.Artifact().Path); err != nil {
		t.Fatalf("valid unreachable source object broke full snapshot: %v", err)
	}
	manifest, err := e.Inspect(context.Background(), r.Artifact().Path)
	if err != nil {
		t.Fatal(err)
	}
	mainOID := manifest.Refs["refs/heads/main"]
	cmd := exec.Command("git", "--git-dir="+src, "pack-objects", "--stdout", "--revs")
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0")
	cmd.Stdin = bytes.NewReader([]byte(mainOID + "\n" + dangling + "\n"))
	pack, err := cmd.Output()
	if err != nil {
		t.Fatalf("pack valid unreferenced object: %v", err)
	}
	header := []byte("# v2 git bundle\n" + mainOID + " refs/heads/main\n" + mainOID + " HEAD\n\n")
	bundle := append(header, pack...)
	bundlePath := filepath.Join(r.Artifact().Path, BundleFilename)
	if err = os.WriteFile(bundlePath, bundle, 0600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(bundle)
	manifest.Bundle.SHA256 = hex.EncodeToString(digest[:])
	manifest.Bundle.SizeBytes = int64(len(bundle))
	wire, err := manifest.Marshal(DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(r.Artifact().Path, "manifest.json"), wire, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = e.Verify(context.Background(), r.Artifact().Path); err != nil {
		t.Fatalf("valid packed object outside advertised refs rejected: %v", err)
	}
	dest := filepath.Join(privateTempDir(t), "restored.git")
	if _, err = e.Restore(context.Background(), r.Artifact().Path, dest); err != nil {
		t.Fatal(err)
	}
	if err = runFixtureGitError("--git-dir="+dest, "cat-file", "-e", dangling); err != nil {
		t.Fatalf("restore dropped a valid extra packed object: %v", err)
	}
}

func TestPublishNeverReplaces(t *testing.T) {
	for _, kind := range []string{"file", "directory", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			src := filepath.Join(t.TempDir(), "stage")
			dst := filepath.Join(t.TempDir(), "dest")
			if err := os.Mkdir(src, 0700); err != nil {
				t.Fatal(err)
			}
			var target string
			switch kind {
			case "file":
				if err := os.WriteFile(dst, []byte("competitor"), 0600); err != nil {
					t.Fatal(err)
				}
				target = dst
			case "directory":
				if err := os.Mkdir(dst, 0700); err != nil {
					t.Fatal(err)
				}
				target = filepath.Join(dst, "owned")
				if err := os.WriteFile(target, []byte("competitor"), 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				target = filepath.Join(t.TempDir(), "target")
				if err := os.WriteFile(target, []byte("competitor"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, dst); err != nil {
					t.Fatal(err)
				}
			}
			if err := (osFilesystem{}).publish(src, dst); err == nil {
				t.Fatal("replaced existing target")
			}
			b, err := os.ReadFile(target)
			if err != nil || string(b) != "competitor" {
				t.Fatalf("competitor changed: %q %v", b, err)
			}
			if _, err = os.Stat(src); err != nil {
				t.Fatalf("failed publication consumed staging: %v", err)
			}
		})
	}
}
func TestPublishRejectsUnsafePaths(t *testing.T) {
	root := t.TempDir()
	link := filepath.Join(root, "alias")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	if _, err := canonicalDirectory(link, false); err == nil {
		t.Fatal("accepted symlink root")
	}
}

func TestManagedRootMarkerRejectsSymlinkAndOversize(t *testing.T) {
	for _, kind := range []string{"symlink", "oversize"} {
		t.Run(kind, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "root")
			if err := os.Mkdir(root, 0700); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(root, ".gitvault-root.json")
			if kind == "symlink" {
				target := filepath.Join(t.TempDir(), "marker.json")
				if err := os.WriteFile(target, []byte(`{"version":1}`), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, marker); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.WriteFile(marker, make([]byte, (64<<10)+1), 0600); err != nil {
					t.Fatal(err)
				}
			}
			e := testEngine(t, root, nil)
			_, err := e.Create(context.Background(), Request{Source: testRepo(t), Repository: RepositoryIdentity{Provider: "local", ID: mustID(t), DisplayName: "fixture"}, ApplicationVersion: "test"})
			if err == nil {
				t.Fatal("accepted unsafe root marker")
			}
			if _, err = os.Stat(filepath.Join(root, "staging")); !os.IsNotExist(err) {
				t.Fatalf("created managed directories before rejecting marker: %v", err)
			}
		})
	}
}

func TestRootTrustBoundaryRejectsReplaceableParent(t *testing.T) {
	parent := t.TempDir()
	if err := os.Chmod(parent, 0777); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parent, "root")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	scratch := t.TempDir()
	if err := os.Chmod(scratch, 0700); err != nil {
		t.Fatal(err)
	}
	runner, err := NewGitRunner(scratch, DefaultGitRunnerOptions())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = NewEngine(EngineOptions{Root: root, Runner: runner}); err == nil {
		t.Fatal("accepted a root replaceable through a shared non-sticky parent")
	}
}

func TestRootTrustBoundaryRejectsWritableAncestor(t *testing.T) {
	outer := privateTempDir(t)
	if err := os.Chmod(outer, 0777); err != nil {
		t.Fatal(err)
	}
	inner := filepath.Join(outer, "private-parent")
	if err := os.Mkdir(inner, 0700); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(inner, "root")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	scratch := privateTempDir(t)
	runner, err := NewGitRunner(scratch, DefaultGitRunnerOptions())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = NewEngine(EngineOptions{Root: root, Runner: runner}); err == nil {
		t.Fatal("accepted root beneath a writable non-sticky ancestor")
	}
}
func TestConflictingRoots(t *testing.T) {
	if !pathsOverlap("/tmp/a", "/tmp/a/b") || pathsOverlap("/tmp/a", "/tmp/ab") {
		t.Fatal("root overlap policy incorrect")
	}
}
func TestConcurrentWriters(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	e := testEngine(t, root, nil)
	unlock, err := e.lock(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	if _, err = e.lock(ctx, true); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second writer did not wait for first lock: %v", err)
	}
	unlock()
	release, err := e.lock(context.Background(), true)
	if err != nil {
		t.Fatalf("first writer did not release lock: %v", err)
	}
	release()
}
func TestRestoreRejectsCorruption(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	e := testEngine(t, root, nil)
	src := testRepo(t)
	r, err := e.Create(context.Background(), Request{Source: src, Repository: RepositoryIdentity{Provider: "local", ID: mustID(t), DisplayName: "fixture"}, ApplicationVersion: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(r.Artifact().Path, BundleFilename), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = e.Verify(context.Background(), r.Artifact().Path); err == nil {
		t.Fatal("accepted corrupt payload")
	}
}

func TestBundleRejectsUnmanifestedHeads(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	e := testEngine(t, root, nil)
	r, err := e.Create(context.Background(), Request{Source: testRepo(t), Repository: RepositoryIdentity{Provider: "local", ID: mustID(t), DisplayName: "fixture"}, ApplicationVersion: "test"})
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := e.Inspect(context.Background(), r.Artifact().Path)
	if err != nil {
		t.Fatal(err)
	}
	bundlePath := filepath.Join(r.Artifact().Path, BundleFilename)
	payload, err := os.ReadFile(bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	separator := []byte("\n\n")
	at := bytes.Index(payload, separator)
	if at < 0 {
		t.Fatal("fixture bundle has no header terminator")
	}
	oid := ""
	for _, v := range manifest.Refs {
		oid = v
		break
	}
	if oid == "" {
		t.Fatal("fixture has no saved ref")
	}
	payload = append(append(append([]byte{}, payload[:at]...), []byte("\n"+oid+" refs/heads/unmanifested")...), payload[at:]...)
	if err = os.WriteFile(bundlePath, payload, 0600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	manifest.Bundle.SHA256 = hex.EncodeToString(digest[:])
	manifest.Bundle.SizeBytes = int64(len(payload))
	wire, err := manifest.Marshal(DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(r.Artifact().Path, "manifest.json"), wire, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = e.Verify(context.Background(), r.Artifact().Path); err == nil {
		t.Fatal("accepted bundle head absent from the manifest")
	}
}
func TestRestoreInputMutation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	e := testEngine(t, root, nil)
	src := testRepo(t)
	r, err := e.Create(context.Background(), Request{Source: src, Repository: RepositoryIdentity{Provider: "local", ID: mustID(t), DisplayName: "fixture"}, ApplicationVersion: "test"})
	if err != nil {
		t.Fatal(err)
	}
	input := r.Artifact().Path
	e.fs = &mutateAfterManifestFS{osFilesystem: osFilesystem{}, bundle: filepath.Join(input, BundleFilename)}
	dest := filepath.Join(privateTempDir(t), "mutated-target")
	if _, err = e.Restore(context.Background(), input, dest); err == nil {
		t.Fatal("accepted bytes changed after privately copying manifest")
	}
	if _, err = os.Lstat(dest); !os.IsNotExist(err) {
		t.Fatalf("published destination after mutation: %v", err)
	}
	e.fs = osFilesystem{}
	dest = filepath.Join(privateTempDir(t), "target")
	if _, err = e.Restore(context.Background(), input, dest); err == nil {
		t.Fatal("accepted persistently corrupted snapshot")
	}
}

type mutateAfterManifestFS struct {
	osFilesystem
	bundle string
}

func (f *mutateAfterManifestFS) copyBounded(ctx context.Context, src, dst string, max int64) error {
	if err := f.osFilesystem.copyBounded(ctx, src, dst, max); err != nil {
		return err
	}
	if filepath.Base(src) == "manifest.json" {
		b, err := os.ReadFile(f.bundle)
		if err != nil {
			return err
		}
		if len(b) == 0 {
			return errors.New("empty bundle fixture")
		}
		b[0] ^= 0xff
		return os.WriteFile(f.bundle, b, 0600)
	}
	return nil
}
func TestBackupCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e := testEngine(t, filepath.Join(t.TempDir(), "root"), nil)
	if _, err := e.Create(ctx, Request{}); err == nil {
		t.Fatal("ignored cancellation")
	}
}
func TestPublicationCommitBoundaries(t *testing.T) {
	d := t.TempDir()
	src := filepath.Join(d, "missing")
	e := testEngine(t, filepath.Join(d, "root"), nil)
	if _, err := e.Create(context.Background(), Request{Source: src}); err == nil {
		t.Fatal("missing source succeeded")
	}
}

type writeFailureFilesystem struct {
	osFilesystem
	failure error
}

func (f writeFailureFilesystem) writeSynced(string, []byte, os.FileMode) error { return f.failure }

type restoreCopyFailureFilesystem struct {
	osFilesystem
	mode    string
	failure error
}

func (f restoreCopyFailureFilesystem) copyBounded(ctx context.Context, src, dst string, max int64) error {
	if filepath.Base(src) != BundleFilename {
		return f.osFilesystem.copyBounded(ctx, src, dst, max)
	}
	switch f.mode {
	case "read":
		if err := os.WriteFile(dst, []byte("partial read"), 0600); err != nil {
			return err
		}
		return f.failure
	case "write":
		if err := os.WriteFile(dst, []byte("partial write"), 0600); err != nil {
			return err
		}
		return f.failure
	case "close":
		if err := f.osFilesystem.copyBounded(ctx, src, dst, max); err != nil {
			return err
		}
		return f.failure
	default:
		return errors.New("unknown copy fault")
	}
}

func TestWriteFailureDoesNotPublish(t *testing.T) {
	failure := errors.New("injected manifest write failure")
	root := filepath.Join(privateTempDir(t), "root")
	e := testEngineWithFS(t, root, writeFailureFilesystem{failure: failure})
	r, err := e.Create(context.Background(), Request{Source: testRepo(t), Repository: RepositoryIdentity{Provider: "local", ID: mustID(t), DisplayName: "write-failure"}, ApplicationVersion: "test"})
	if !errors.Is(err, failure) || r.Publication() != NotPublished || r.Artifact() != (ArtifactIdentity{}) {
		t.Fatalf("write failure result=%+v err=%v", r, err)
	}
	if entries, err := os.ReadDir(filepath.Join(root, "staging")); err != nil || len(entries) != 0 {
		t.Fatalf("write failure left staging: %v %v", entries, err)
	}
}

func TestRestoreCopyFailuresCleanStaging(t *testing.T) {
	root := filepath.Join(privateTempDir(t), "root")
	base := testEngine(t, root, nil)
	created, err := base.Create(context.Background(), Request{Source: testRepo(t), Repository: RepositoryIdentity{Provider: "local", ID: mustID(t), DisplayName: "copy-failure"}, ApplicationVersion: "test"})
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"read", "write", "close"} {
		t.Run(mode, func(t *testing.T) {
			failure := errors.New("injected " + mode + " failure")
			base.fs = restoreCopyFailureFilesystem{mode: mode, failure: failure}
			dest := filepath.Join(privateTempDir(t), "restore.git")
			if _, err := base.Restore(context.Background(), created.Artifact().Path, dest); !errors.Is(err, failure) {
				t.Fatalf("restore error=%v want %v", err, failure)
			}
			if _, err = os.Lstat(dest); !os.IsNotExist(err) {
				t.Fatalf("published after %s failure: %v", mode, err)
			}
			if entries, err := os.ReadDir(filepath.Join(root, "staging")); err != nil || len(entries) != 0 {
				t.Fatalf("%s failure left staging: %v %v", mode, entries, err)
			}
		})
	}
}

type enospcFilesystem struct{ osFilesystem }

func (enospcFilesystem) publish(string, string) error { return syscall.ENOSPC }
func TestDiskFull(t *testing.T) {
	d := t.TempDir()
	if err := os.Chmod(d, 0700); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(d, "root")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	scratch := t.TempDir()
	_ = os.Chmod(scratch, 0700)
	runner, err := NewGitRunner(scratch, DefaultGitRunnerOptions())
	if err != nil {
		t.Fatal(err)
	}
	e, err := NewEngine(EngineOptions{Root: root, Runner: runner, Filesystem: enospcFilesystem{}})
	if err != nil {
		t.Fatal(err)
	}
	repoID := mustID(t)
	_, err = e.Create(context.Background(), Request{Source: testRepo(t), Repository: RepositoryIdentity{Provider: "local", ID: repoID, DisplayName: "fixture"}, ApplicationVersion: "test"})
	if !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("expected injected ENOSPC at publication, got %v", err)
	}
	if entries, readErr := os.ReadDir(filepath.Join(root, "staging")); readErr != nil || len(entries) != 0 {
		t.Fatalf("uncommitted staging remains: %v %v", entries, readErr)
	}
	if entries, readErr := os.ReadDir(filepath.Join(root, "snapshots", "local", repoID)); readErr == nil && len(entries) != 0 {
		t.Fatalf("failed publication became visible: %v", entries)
	}
}

type syncFailFilesystem struct {
	osFilesystem
	failOn int
	calls  int
}

func (f *syncFailFilesystem) syncDirectory(path string) error {
	f.calls++
	if f.calls == f.failOn {
		return syscall.EIO
	}
	return f.osFilesystem.syncDirectory(path)
}

type cancelOnPublishFilesystem struct {
	osFilesystem
	cancel context.CancelFunc
}

func (f cancelOnPublishFilesystem) publish(src, dst string) error {
	if err := f.osFilesystem.publish(src, dst); err != nil {
		return err
	}
	f.cancel()
	return nil
}

func testEngineWithFS(t *testing.T, root string, fs filesystemOps) *Engine {
	t.Helper()
	if err := os.Chmod(filepath.Dir(root), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	scratch := t.TempDir()
	if err := os.Chmod(scratch, 0700); err != nil {
		t.Fatal(err)
	}
	runner, err := NewGitRunner(scratch, DefaultGitRunnerOptions())
	if err != nil {
		t.Fatal(err)
	}
	e, err := NewEngine(EngineOptions{Root: root, Runner: runner, Filesystem: fs})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestFailureBoundaries(t *testing.T) {
	t.Run("post-publication sync uncertainty", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "root")
		fs := &syncFailFilesystem{failOn: 2}
		e := testEngineWithFS(t, root, fs)
		r, err := e.Create(context.Background(), Request{Source: testRepo(t), Repository: RepositoryIdentity{Provider: "local", ID: mustID(t), DisplayName: "fixture"}, ApplicationVersion: "test"})
		if !errors.Is(err, syscall.EIO) || r.Publication() != PublishedDurabilityUncertain || r.Artifact().Path == "" {
			t.Fatalf("result=%+v err=%v", r, err)
		}
		if _, err = os.Stat(r.Artifact().Path); err != nil {
			t.Fatalf("published artifact identity lost after sync failure: %v", err)
		}
	})
	t.Run("cancel after visibility commit", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		root := filepath.Join(t.TempDir(), "root")
		e := testEngineWithFS(t, root, cancelOnPublishFilesystem{cancel: cancel})
		r, err := e.Create(ctx, Request{Source: testRepo(t), Repository: RepositoryIdentity{Provider: "local", ID: mustID(t), DisplayName: "fixture"}, ApplicationVersion: "test"})
		if !errors.Is(err, context.Canceled) || r.Outcome() != OutcomeCancelled || r.Publication() != Published || r.Artifact().Path == "" {
			t.Fatalf("result=%+v err=%v", r, err)
		}
		if _, err = os.Stat(r.Artifact().Path); err != nil {
			t.Fatalf("published artifact removed after cancellation: %v", err)
		}
	})
}
func TestProgressDoesNotBlock(t *testing.T) {
	events := make(chan ProgressEvent, 1)
	queued := ProgressEvent{OperationID: "queued", RepositoryID: "queued", Stage: "queued", Sequence: 1}
	events <- queued // Saturate the observer before any real engine event.
	e := testEngine(t, filepath.Join(privateTempDir(t), "root"), events)
	source := testRepo(t)
	request := Request{Source: source, Repository: RepositoryIdentity{Provider: "local", ID: mustID(t), DisplayName: "fixture"}, ApplicationVersion: "test"}
	done := make(chan struct {
		result Result
		err    error
	}, 1)
	go func() {
		r, err := e.Create(context.Background(), request)
		done <- struct {
			result Result
			err    error
		}{r, err}
	}()
	select {
	case got := <-done:
		if got.err != nil || got.result.Outcome() != OutcomeSucceeded || got.result.Publication() != Published {
			t.Fatalf("operation with saturated observer: result=%+v err=%v", got.result, got.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("real engine operation blocked on a saturated progress observer")
	}
	if len(events) != 1 {
		t.Fatalf("observer queue occupancy=%d", len(events))
	}
	if got := <-events; got != queued {
		t.Fatalf("saturated observer event was unexpectedly replaced: %+v", got)
	}
}
