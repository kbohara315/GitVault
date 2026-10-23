package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"gitvault/internal/config"
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

func TestVersionOneMigrationPreservesConfiguration(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(privateTempDir(t), "upgrade.sqlite3")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	identity := s.Identity()
	current, err := s.CurrentConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	values := current.Config()
	values.Selection.RepositoryIDs = []string{"github:201"}
	updated, err := s.SaveConfig(ctx, current.ID(), values)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(updated.Config())
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]json.RawMessage
	if err = json.Unmarshal(body, &document); err != nil {
		t.Fatal(err)
	}
	var githubBody map[string]json.RawMessage
	if err = json.Unmarshal(document["github"], &githubBody); err != nil {
		t.Fatal(err)
	}
	delete(githubBody, "filters") // The version-one configuration predates discovery filters.
	document["github"], err = json.Marshal(githubBody)
	if err != nil {
		t.Fatal(err)
	}
	body, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `DROP TRIGGER immutable_config_update`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `UPDATE config_revisions SET body=? WHERE id=?`, string(body), updated.ID()); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `CREATE TRIGGER immutable_config_update BEFORE UPDATE ON config_revisions BEGIN SELECT RAISE(ABORT,'immutable configuration'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `DROP TABLE github_repositories`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `DROP TABLE github_accounts`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version=2`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `PRAGMA user_version=1`); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	if upgraded.Identity() != identity {
		t.Fatal("v1 migration changed store authority")
	}
	after, err := upgraded.CurrentConfig(ctx)
	if err != nil || after.ID() != updated.ID() {
		t.Fatalf("v1 migration changed settings head: %v %v", after.ID(), err)
	}
	got := after.Config()
	if !reflect.DeepEqual(got.Selection.RepositoryIDs, []string{"github:201"}) || got.GitHub.Filters != (config.GitHubFilters{Owned: true, Organizations: true, Collaborating: true}) {
		t.Fatalf("v1 migration lost selection or defaulted filters incorrectly: %+v", got.GitHub)
	}
	var version, entries int
	if err = upgraded.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err = upgraded.db.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&entries); err != nil {
		t.Fatal(err)
	}
	if version != 2 || entries != 2 {
		t.Fatalf("unexpected migrated schema ledger: version=%d entries=%d", version, entries)
	}
}

func TestGitHubInventorySurvivesReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(privateTempDir(t), "inventory.sqlite3")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	account := GitHubAccount{ID: "101", Login: "alice", Type: "User"}
	repository := GitHubRepository{
		ID: "201", OwnerID: "101", OwnerLogin: "alice", OwnerType: "User", Name: "project",
		FullName: "alice/project", CloneURL: "https://github.com/alice/project.git", Private: true,
		Description: "synthetic inventory row",
	}
	if err = s.ReconcileGitHubInventory(ctx, account, []GitHubRepository{repository}, true); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	gotAccount, rows, err := s.LatestGitHubInventory(ctx)
	if err != nil || gotAccount.ID != account.ID || !gotAccount.Complete || len(rows) != 1 || rows[0].ID != repository.ID || !rows[0].Available || rows[0].FullName != repository.FullName {
		t.Fatalf("inventory did not survive reopen: account=%+v rows=%+v err=%v", gotAccount, rows, err)
	}
	if err = s.ReconcileGitHubInventory(ctx, account, nil, true); err != nil {
		t.Fatal(err)
	}
	_, rows, err = s.GitHubInventory(ctx, account.ID)
	if err != nil || len(rows) != 1 || rows[0].Available {
		t.Fatalf("complete inventory deleted instead of retaining inaccessible row: %+v err=%v", rows, err)
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
			statement := map[string]string{"future_pragma": "PRAGMA user_version=3", "future_ledger": `INSERT INTO schema_migrations VALUES(3,'unknown','future')`, "altered_checksum": `UPDATE schema_migrations SET checksum='changed' WHERE version=1`, "missing_history": `DELETE FROM schema_migrations`}[scenario]
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
