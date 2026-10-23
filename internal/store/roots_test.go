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
	if err = a.BindRoot(ctx, root); err != nil {
		t.Fatal(err)
	}
	if err = a.ValidateRoot(ctx, root); err != nil {
		t.Fatal(err)
	}
	if err = b.BindRoot(ctx, root); !errors.Is(err, ErrForeignRoot) {
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
	replacement.FilesystemID = "second"
	if err = a.BindRoot(ctx, replacement); err == nil {
		t.Fatal("rebound active root during accepted work")
	}
	interrupted, err := a.ReconcileInterrupted(ctx)
	if err != nil || len(interrupted) != 1 {
		t.Fatal(err)
	}
	if err = a.BindRoot(ctx, replacement); err != nil {
		t.Fatal(err)
	}
	if err = a.ValidateRoot(ctx, root); !errors.Is(err, ErrForeignRoot) {
		t.Fatalf("inactive root still grants authority: %v", err)
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
