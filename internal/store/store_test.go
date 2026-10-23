package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestMigrationsAtomic(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(privateTempDir(t), "metadata.sqlite3")
	injected := errors.New("migration interrupted")
	_, err := open(ctx, path, "", func(stage string) error {
		if stage == "migration.before_commit" {
			return injected
		}
		return nil
	})
	if !errors.Is(err, injected) {
		t.Fatalf("wanted rollback injection, got %v", err)
	}
	raw, err := sql.Open("sqlite", sqliteURI(path))
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err = raw.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='config_revisions'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("failed migration left partial schema")
	}
	raw.Close()
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	identity := s.Identity()
	r, err := s.CurrentConfig(ctx)
	if err != nil || r.ID() != 1 {
		t.Fatalf("bootstrap: %v %v", r, err)
	}
	s.Close()
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Identity() != identity {
		t.Fatal("identity changed across reopen")
	}
}

func TestFutureSchemaRejected(t *testing.T) {
	ctx := context.Background()
	for _, scenario := range []string{"future_pragma", "future_ledger", "altered_checksum", "missing_history"} {
		t.Run(scenario, func(t *testing.T) {
			path := filepath.Join(privateTempDir(t), "authority.sqlite3")
			s, err := Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			identity := s.Identity()
			statement := map[string]string{"future_pragma": "PRAGMA user_version=2", "future_ledger": `INSERT INTO schema_migrations VALUES(2,'unknown','future')`, "altered_checksum": `UPDATE schema_migrations SET checksum='changed' WHERE version=1`, "missing_history": `DELETE FROM schema_migrations`}[scenario]
			if _, err = s.db.ExecContext(ctx, statement); err != nil {
				t.Fatal(err)
			}
			s.Close()
			if _, err = Open(ctx, path); err == nil {
				t.Fatal("unknown or altered history accepted")
			}
			raw, err := sql.Open("sqlite", sqliteURI(path))
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			var id string
			if err = raw.QueryRowContext(ctx, `SELECT store_id FROM store_identity`).Scan(&id); err != nil {
				t.Fatal(err)
			}
			if id != identity.StoreID {
				t.Fatal("failed open replaced original authority")
			}
			var count int
			if err = raw.QueryRowContext(ctx, `SELECT count(*) FROM config_revisions`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("original revisions changed: %d %v", count, err)
			}
		})
	}
}

func TestSQLiteConnectionPolicy(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(privateTempDir(t), "path with ?pragma&=literal#.sqlite3")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for attempt := 0; attempt < 2; attempt++ {
		if err = s.checkPolicy(ctx); err != nil {
			t.Fatal(err)
		}
		// Owned SQL seam verifies real FK enforcement, not a cached flag.
		if _, err = s.db.ExecContext(ctx, `INSERT INTO pins VALUES('nonexistent','now')`); err == nil {
			t.Fatal("SQLite accepted dangling foreign key")
		}
		if attempt == 0 {
			s.db.SetMaxIdleConns(0)
			s.db.SetMaxIdleConns(1)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("unsafe permissions: %v", info.Mode())
	}
	if s.db.Stats().MaxOpenConnections != 1 {
		t.Fatal("more than one connection allowed")
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if info, err := os.Stat(path + suffix); err == nil && info.Mode().Perm()&0077 != 0 {
			t.Fatalf("unsafe SQLite sidecar permissions: %s %v", suffix, info.Mode())
		}
	}
}

func TestOpenRejectsUnsafeOrCancelledPaths(t *testing.T) {
	ctx := context.Background()
	dir := privateTempDir(t)
	if _, err := Open(ctx, "relative.sqlite3"); err == nil {
		t.Fatal("accepted a working-directory fallback")
	}
	target := filepath.Join(dir, "target.sqlite3")
	if err := os.WriteFile(target, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.sqlite3")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ctx, link); err == nil {
		t.Fatal("accepted symlink database")
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "preserve" {
		t.Fatal("symlink target was modified")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	absent := filepath.Join(dir, "absent.sqlite3")
	if _, err = Open(cancelled, absent); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled open: %v", err)
	}
	if _, err = os.Stat(absent); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("cancelled open created database")
	}
}

func TestOpenRejectsSymlinkInstalledAtOpenBoundary(t *testing.T) {
	ctx := context.Background()
	dir := privateTempDir(t)
	path := filepath.Join(dir, "metadata.sqlite3")
	target := filepath.Join(dir, "unrelated.txt")
	if err := os.WriteFile(target, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := open(ctx, path, "", func(stage string) error {
		if stage == "database.before_open" {
			return os.Symlink(target, path)
		}
		return nil
	})
	if err == nil {
		_ = s.Close()
		t.Fatal("opened a symlink installed immediately before database open")
	}
	contents, readErr := os.ReadFile(target)
	if readErr != nil || string(contents) != "preserve" {
		t.Fatalf("symlink target changed: %q %v", contents, readErr)
	}
}
