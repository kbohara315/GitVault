//go:build linux

package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitvault/internal/store"
)

func TestCreatePrivateManagedRootCreatesOnlyMissingDirectory(t *testing.T) {
	parent := t.TempDir()
	if err := os.Chmod(parent, 0700); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parent, "GitVault-Backups")
	if err := createPrivateManagedRoot(root); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(root)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("created private root mode=%v err=%v", info, err)
	}
	if err = os.Chmod(root, 0770); err != nil {
		t.Fatal(err)
	}
	if err = createPrivateManagedRoot(root); err != nil {
		t.Fatal(err)
	}
	info, err = os.Stat(root)
	if err != nil || info.Mode().Perm() != 0770 {
		t.Fatalf("existing root permissions were changed: mode=%v err=%v", info, err)
	}
}

func TestBindManagedRootRejectsApplicationDataOverlapWithoutCreatingLocks(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	if err := os.Chmod(base, 0700); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(base, "app-data")
	if err := os.Mkdir(data, 0700); err != nil {
		t.Fatal(err)
	}
	s, err := store.OpenWithInstallation(ctx, filepath.Join(data, "gitvault.sqlite3"), "root-overlap-test")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	nested := filepath.Join(data, "backup")
	if err = os.Mkdir(nested, 0700); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []string{data, nested} {
		if _, err = BindManagedRoot(ctx, s, candidate); err == nil {
			t.Fatalf("application-data overlap accepted: %s", candidate)
		}
		entries, readErr := os.ReadDir(candidate)
		if readErr != nil {
			t.Fatal(readErr)
		}
		for _, entry := range entries {
			if entry.Name() == managedLockName || entry.Name() == managedRunLockName || entry.Name() == managedRootMarkerName || strings.HasPrefix(entry.Name(), ".gitvault-root-") {
				t.Fatalf("rejected root was modified before overlap validation: %s", entry.Name())
			}
		}
	}
	if _, err = s.ActiveRoot(ctx); err != store.ErrNotFound {
		t.Fatalf("rejected root created a database binding: %v", err)
	}
}

func TestManagedRootMarkerRollbackIsScoped(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	marker := managedRootMarker{Version: 1, RootID: "root-a", InstallationID: "installation-a", StoreID: "store-a", ApplicationData: "/tmp/app-a", Generation: 1}
	rollback, err := publishManagedRootMarkerForRollback(root, marker)
	if err != nil {
		t.Fatal(err)
	}
	if actual, err := readManagedRootMarker(filepath.Join(root, managedRootMarkerName)); err != nil || actual != marker {
		t.Fatalf("published marker=%+v err=%v", actual, err)
	}
	if err = rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Lstat(filepath.Join(root, managedRootMarkerName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("known failed transaction left its marker: %v", err)
	}

	rollback, err = publishManagedRootMarkerForRollback(root, marker)
	if err != nil {
		t.Fatal(err)
	}
	replacement := managedRootMarker{Version: 1, RootID: "root-b", InstallationID: "installation-b", StoreID: "store-b", ApplicationData: "/tmp/app-b", Generation: 1}
	data, err := json.Marshal(replacement)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, managedRootMarkerName), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err = rollback(); !errors.Is(err, store.ErrForeignRoot) {
		t.Fatalf("rollback removed a marker it did not publish: %v", err)
	}
	if actual, err := readManagedRootMarker(filepath.Join(root, managedRootMarkerName)); err != nil || actual != replacement {
		t.Fatalf("replacement marker was not preserved: %+v err=%v", actual, err)
	}
}
