package store

import (
	"os"
	"path/filepath"
	"testing"
)

func privateTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "gitvault-store-test-")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func bindTestRoot(t *testing.T, s *Store) RootBinding {
	t.Helper()
	id := s.Identity()
	r := RootBinding{ID: "test-root", Path: filepath.Join(privateTempDir(t), "managed"), FilesystemID: "synthetic-filesystem", StoreID: id.StoreID, InstallationID: id.InstallationID, DataDir: id.DataDir, Generation: 1, Active: true}
	if err := rebindTestRootConfig(t, s, r); err != nil {
		t.Fatal(err)
	}
	return r
}

func rebindTestRootConfig(t *testing.T, s *Store, root RootBinding) error {
	t.Helper()
	current, err := s.CurrentConfig(t.Context())
	if err != nil {
		return err
	}
	values := current.Config()
	path := root.Path
	values.Backup.Root = &path
	_, err = s.RebindRootConfig(t.Context(), current.ID(), values, root, func() (func() error, error) {
		return func() error { return nil }, nil
	})
	return err
}
