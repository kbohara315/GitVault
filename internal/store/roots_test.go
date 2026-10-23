package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestRootAuthorityAndPersistentInstallation(t *testing.T) {
	ctx := context.Background()
	dir := privateTempDir(t)
	path := filepath.Join(dir, "a.sqlite3")
	a, err := OpenWithInstallation(ctx, path, "installation-a")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := OpenWithInstallation(ctx, filepath.Join(dir, "b.sqlite3"), "installation-b")
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	identity := a.Identity()
	root := RootBinding{ID: "root-1", Path: filepath.Join(privateTempDir(t), "managed"), FilesystemID: "synthetic-filesystem-identity", StoreID: identity.StoreID, InstallationID: identity.InstallationID, DataDir: identity.DataDir, Generation: 1, Active: true}
	if err = rebindTestRootConfig(t, a, root); err != nil {
		t.Fatal(err)
	}
	if err = a.ValidateRoot(ctx, root); err != nil {
		t.Fatal(err)
	}
	if err = rebindTestRootConfig(t, b, root); !errors.Is(err, ErrForeignRoot) {
		t.Fatalf("foreign root adopted: %v", err)
	}
	for _, mutate := range []func(*RootBinding){func(r *RootBinding) { r.Generation++ }, func(r *RootBinding) { r.Path += "-moved" }, func(r *RootBinding) { r.FilesystemID = "different" }, func(r *RootBinding) { r.StoreID = b.Identity().StoreID }, func(r *RootBinding) { r.InstallationID = "lost" }, func(r *RootBinding) { r.DataDir += "-other" }} {
		r := root
		mutate(&r)
		if err = a.ValidateRoot(ctx, r); !errors.Is(err, ErrForeignRoot) {
			t.Fatalf("ambiguous marker accepted: %+v %v", r, err)
		}
	}
	if _, err = b.BeginRun(ctx, RunIntent{ID: "foreign-run", Kind: "local_backup", Trigger: "manual", RootID: root.ID, RepositoryIDs: []string{"repo"}}); !errors.Is(err, ErrForeignRoot) {
		t.Fatalf("foreign authority admitted run: %v", err)
	}
	if _, err = OpenWithInstallation(ctx, path, "different-installation"); err == nil {
		t.Fatal("installation identity silently changed")
	}
	if _, err = a.BeginRun(ctx, RunIntent{ID: "bound-run", Kind: "local_backup", Trigger: "manual", RootID: root.ID, RepositoryIDs: []string{"repo"}}); err != nil {
		t.Fatal(err)
	}
	replacement := root
	replacement.ID = "root-2"
	replacement.Path = filepath.Join(privateTempDir(t), "managed-2")
	replacement.FilesystemID = "second"
	if err = rebindTestRootConfig(t, a, replacement); err == nil {
		t.Fatal("rebound active root during accepted work")
	}
	interrupted, err := a.ReconcileInterrupted(ctx)
	if err != nil || len(interrupted) != 1 {
		t.Fatal(err)
	}
	if err = rebindTestRootConfig(t, a, replacement); err != nil {
		t.Fatal(err)
	}
	if err = a.ValidateRoot(ctx, root); !errors.Is(err, ErrForeignRoot) {
		t.Fatalf("inactive root still grants authority: %v", err)
	}
}

func TestRootRebindCommitsConfigurationAndAuthorityTogether(t *testing.T) {
	ctx := context.Background()
	dir := privateTempDir(t)
	s, err := Open(ctx, filepath.Join(dir, "rebind.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rootBase := privateTempDir(t)
	current, err := s.CurrentConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	identity := s.Identity()
	root := RootBinding{ID: "rebind-root", Path: filepath.Join(rootBase, "managed"), FilesystemID: "synthetic-fs", StoreID: identity.StoreID, InstallationID: identity.InstallationID, DataDir: identity.DataDir, Generation: 1, Active: true}
	values := current.Config()
	rootPath := root.Path
	values.Backup.Root = &rootPath

	published := false
	rolledBack := false
	injected := errors.New("synthetic pre-commit failure")
	s.hook = func(stage string) error {
		if stage == "root_rebind.before_commit" {
			return injected
		}
		return nil
	}
	if _, err = s.RebindRootConfig(ctx, current.ID(), values, root, func() (func() error, error) {
		published = true
		return func() error { published = false; rolledBack = true; return nil }, nil
	}); !errors.Is(err, injected) {
		t.Fatalf("expected injected rollback: %v", err)
	}
	s.hook = nil
	if published || !rolledBack {
		t.Fatalf("marker rollback callback not observed: published=%v rolledBack=%v", published, rolledBack)
	}
	if got, err := s.CurrentConfig(ctx); err != nil || got.ID() != current.ID() || got.Config().Backup.Root != nil {
		t.Fatalf("failed root transaction changed config: %+v %v", got, err)
	}
	if _, err = s.ActiveRoot(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("failed root transaction changed binding: %v", err)
	}
	insideData := root
	insideData.ID = "application-data-root"
	insideData.Path = identity.DataDir
	insideConfig := current.Config()
	insideConfig.Backup.Root = &insideData.Path
	if _, err = s.RebindRootConfig(ctx, current.ID(), insideConfig, insideData, func() (func() error, error) {
		t.Fatal("application-data overlap reached marker publication")
		return nil, nil
	}); !errors.Is(err, ErrOverlappingRoots) {
		t.Fatalf("application data accepted as a managed root: %v", err)
	}

	revision, err := s.RebindRootConfig(ctx, current.ID(), values, root, func() (func() error, error) {
		return func() error { return nil }, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	bound, err := s.ActiveRoot(ctx)
	if err != nil || bound != root {
		t.Fatalf("root not committed: %+v %v", bound, err)
	}
	if revision.ID() != current.ID()+1 || revision.Config().Backup.Root == nil || *revision.Config().Backup.Root != root.Path {
		t.Fatalf("matching immutable config revision not committed: %+v", revision)
	}
	nested := root
	nested.ID = "nested-root"
	nested.Path = filepath.Join(root.Path, "nested")
	nestedConfig := revision.Config()
	nestedConfig.Backup.Root = &nested.Path
	markerPublished := false
	if _, err = s.RebindRootConfig(ctx, revision.ID(), nestedConfig, nested, func() (func() error, error) {
		markerPublished = true
		return func() error { return nil }, nil
	}); !errors.Is(err, ErrOverlappingRoots) {
		t.Fatalf("nested registered root accepted: %v", err)
	}
	if markerPublished {
		t.Fatal("overlapping root reached marker publication")
	}
	unrelated := revision.Config()
	unrelated.Backup.RepositoryConcurrency++
	markerPublished = false
	if _, err = s.RebindRootConfig(ctx, revision.ID(), unrelated, root, func() (func() error, error) {
		markerPublished = true
		return func() error { return nil }, nil
	}); err == nil {
		t.Fatal("root rebind changed unrelated settings")
	}
	if markerPublished {
		t.Fatal("unrelated settings edit reached marker publication")
	}

	changed := revision.Config()
	otherPath := filepath.Join(dir, "other-root")
	changed.Backup.Root = &otherPath
	if _, err = s.SaveConfig(ctx, revision.ID(), changed); !errors.Is(err, ErrRootRebindRequired) {
		t.Fatalf("ordinary config save bypassed root binding: %v", err)
	}
	if got, err := s.CurrentConfig(ctx); err != nil || got.ID() != revision.ID() || *got.Config().Backup.Root != root.Path {
		t.Fatalf("rejected root edit changed current revision: %+v %v", got, err)
	}
	if _, err = s.RebindRootConfig(ctx, current.ID(), values, root, func() (func() error, error) {
		t.Fatal("stale root edit reached marker publication")
		return nil, nil
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale root edit did not conflict: %v", err)
	}
}

func TestRunTransitionsExhaustive(t *testing.T) {
	states := []string{"preparing", "running", "cancelling", "succeeded", "partial", "failed", "cancelled", "interrupted", "idle", "queued"}
	allowed := map[string]map[string]bool{
		"preparing":  {"running": true, "cancelling": true, "failed": true, "cancelled": true, "interrupted": true},
		"running":    {"cancelling": true, "succeeded": true, "partial": true, "failed": true, "cancelled": true, "interrupted": true},
		"cancelling": {"succeeded": true, "partial": true, "failed": true, "cancelled": true, "interrupted": true},
	}
	for _, from := range states {
		for _, to := range states {
			if got := ValidRunTransition(from, to); got != allowed[from][to] {
				t.Errorf("transition %s -> %s = %v", from, to, got)
			}
		}
	}
}
