// Package store persists configuration, intent, and observed facts. Filesystem
// validation and ownership locks belong to the coordinator, not SQLite.
package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gitvault/internal/config"
	_ "modernc.org/sqlite"
)

var (
	ErrConflict           = errors.New("stale revision or conflicting identity")
	ErrNotFound           = errors.New("record not found")
	ErrFutureSchema       = errors.New("unsupported future database schema")
	ErrMigrationHistory   = errors.New("missing or altered migration history")
	ErrForeignRoot        = errors.New("root binding does not belong to this database installation")
	ErrOverlappingRoots   = errors.New("managed root overlaps another registered root")
	ErrRootRebindRequired = errors.New("backup root changes require explicit validated rebind")
	ErrInvalidTransition  = errors.New("invalid state transition")
)

type BusyError struct{ RunID string }

func (e *BusyError) Error() string { return "managed work is busy" }

type Identity struct {
	StoreID        string
	InstallationID string
	DataDir        string
}
type Store struct {
	db       *sql.DB
	identity Identity
	hook     func(string) error
}

//go:embed migrations/001.sql
var migrationOne string

//go:embed migrations/002_github_inventory.sql
var migrationTwo string

func sqliteURI(path string) string {
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	q := url.Values{}
	q.Set("mode", "rw")
	q.Set("_txlock", "immediate")
	for _, p := range []string{"foreign_keys(1)", "busy_timeout(5000)", "journal_mode(WAL)", "synchronous(FULL)", "trusted_schema(0)"} {
		q.Add("_pragma", p)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// Open creates a private local database. It never falls back to another path.
// The caller must choose a local, non-synchronized application-data directory.
func Open(ctx context.Context, path string) (*Store, error) { return open(ctx, path, "", nil) }

// OpenWithInstallation verifies or establishes an externally supplied opaque
// installation identity. An empty ID has the same behavior as Open.
func OpenWithInstallation(ctx context.Context, path, installationID string) (*Store, error) {
	return open(ctx, path, installationID, nil)
}
func open(ctx context.Context, path, installationID string, hook func(string) error) (*Store, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsRune(path, 0) {
		return nil, errors.New("database path must be a clean absolute file path")
	}
	if installationID != "" && !validID(installationID) {
		return nil, errors.New("invalid installation ID")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("create application data directory: %w", err)
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	path = filepath.Join(dir, filepath.Base(path))
	if err = validateDatabaseDirectory(dir); err != nil {
		return nil, err
	}
	if hook != nil {
		if err = hook("database.before_open"); err != nil {
			return nil, err
		}
	}
	f, err := openDatabaseFile(path)
	if err != nil {
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", sqliteURI(path))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &Store{db: db, hook: hook}
	if err = db.PingContext(ctx); err == nil {
		err = s.checkPolicy(ctx)
	}
	if err == nil {
		err = s.migrate(ctx, dir, installationID)
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}
func (s *Store) Close() error       { return s.db.Close() }
func (s *Store) Identity() Identity { return s.identity }
func (s *Store) checkpoint(stage string) error {
	if s.hook != nil {
		return s.hook(stage)
	}
	return nil
}
func (s *Store) checkPolicy(ctx context.Context) error {
	for _, p := range []struct {
		name string
		want int
	}{{"foreign_keys", 1}, {"busy_timeout", 5000}, {"synchronous", 2}, {"trusted_schema", 0}} {
		var got int
		if err := s.db.QueryRowContext(ctx, "PRAGMA "+p.name).Scan(&got); err != nil {
			return err
		}
		if got != p.want {
			return fmt.Errorf("SQLite connection policy %s not enforced", p.name)
		}
	}
	var mode string
	if err := s.db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
		return err
	}
	if mode != "wal" {
		return errors.New("SQLite WAL unavailable")
	}
	return nil
}
func (s *Store) migrate(ctx context.Context, dir, installation string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version int
	if err = tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	migrations := []string{migrationOne, migrationTwo}
	if version > len(migrations) {
		return ErrFutureSchema
	}
	if _, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations(version INTEGER PRIMARY KEY,checksum TEXT NOT NULL,applied_at TEXT NOT NULL)`); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT version,checksum FROM schema_migrations ORDER BY version`)
	if err != nil {
		return err
	}
	var versions []int
	var sums []string
	for rows.Next() {
		var n int
		var sum string
		if err = rows.Scan(&n, &sum); err != nil {
			rows.Close()
			return err
		}
		versions = append(versions, n)
		sums = append(sums, sum)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if version != len(versions) {
		return ErrMigrationHistory
	}
	for i, v := range versions {
		if v > len(migrations) {
			return ErrFutureSchema
		}
		if v != i+1 {
			return ErrMigrationHistory
		}
		sum := sha256.Sum256([]byte(migrations[i]))
		if sums[i] != hex.EncodeToString(sum[:]) {
			return ErrMigrationHistory
		}
	}
	if len(versions) == 0 {
		var tables int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name NOT IN('schema_migrations','sqlite_sequence')`).Scan(&tables); err != nil {
			return err
		}
		if tables != 0 {
			return ErrMigrationHistory
		}
	} else {
		if err = tx.QueryRowContext(ctx, `SELECT store_id,installation_id,data_dir FROM store_identity WHERE singleton=1`).Scan(&s.identity.StoreID, &s.identity.InstallationID, &s.identity.DataDir); err != nil {
			return err
		}
		if s.identity.DataDir != dir || (installation != "" && s.identity.InstallationID != installation) {
			return errors.New("database authority differs from canonical application data location or installation")
		}
		if _, err = readRevision(ctx, tx, 0); err != nil {
			return fmt.Errorf("current configuration: %w", err)
		}
	}
	for next := len(versions) + 1; next <= len(migrations); next++ {
		if _, err = tx.ExecContext(ctx, migrations[next-1]); err != nil {
			return fmt.Errorf("schema migration %d: %w", next, err)
		}
		if next == 1 {
			if installation == "" {
				installation = newID()
			}
			s.identity = Identity{newID(), installation, dir}
			if _, err = tx.ExecContext(ctx, `INSERT INTO store_identity VALUES(1,?,?,?)`, s.identity.StoreID, installation, dir); err != nil {
				return err
			}
			if _, err = insertConfig(ctx, tx, config.Default()); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO settings_head VALUES(1,1)`); err != nil {
				return err
			}
		}
		sum := sha256.Sum256([]byte(migrations[next-1]))
		if _, err = tx.ExecContext(ctx, `INSERT INTO schema_migrations VALUES(?,?,?)`, next, hex.EncodeToString(sum[:]), stamp(time.Now())); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version=%d", next)); err != nil {
			return err
		}
	}
	if err = s.checkpoint("migration.before_commit"); err != nil {
		return err
	}
	return tx.Commit()
}
func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("system random source unavailable")
	}
	return hex.EncodeToString(b[:])
}
func validID(s string) bool {
	if len(s) < 1 || len(s) > 256 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == ':' || r == '.') {
			return false
		}
	}
	return true
}
func stamp(t time.Time) string               { return t.UTC().Format(time.RFC3339Nano) }
func parseStamp(s string) (time.Time, error) { return time.Parse(time.RFC3339Nano, s) }

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readRevision(ctx context.Context, q queryer, id int64) (config.Revision, error) {
	var body, created string
	var schema int
	var err error
	if id == 0 {
		err = q.QueryRowContext(ctx, `SELECT c.id,c.schema_version,c.body,c.created_at FROM config_revisions c JOIN settings_head h ON h.revision_id=c.id WHERE h.singleton=1`).Scan(&id, &schema, &body, &created)
	} else {
		err = q.QueryRowContext(ctx, `SELECT schema_version,body,created_at FROM config_revisions WHERE id=?`, id).Scan(&schema, &body, &created)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return config.Revision{}, ErrNotFound
	}
	if err != nil {
		return config.Revision{}, err
	}
	if schema != 1 {
		return config.Revision{}, errors.New("unsupported stored configuration schema")
	}
	c, err := config.Decode(strings.NewReader(body))
	if err != nil {
		return config.Revision{}, err
	}
	at, err := parseStamp(created)
	if err != nil {
		return config.Revision{}, err
	}
	return config.NewRevision(id, at, c)
}
func (s *Store) CurrentConfig(ctx context.Context) (config.Revision, error) {
	return readRevision(ctx, s.db, 0)
}
func (s *Store) ConfigRevision(ctx context.Context, id int64) (config.Revision, error) {
	if id < 1 {
		return config.Revision{}, ErrNotFound
	}
	return readRevision(ctx, s.db, id)
}
func insertConfig(ctx context.Context, tx *sql.Tx, c config.Config) (config.Revision, error) {
	body, err := json.Marshal(c)
	if err != nil {
		return config.Revision{}, err
	}
	at := time.Now().UTC()
	result, err := tx.ExecContext(ctx, `INSERT INTO config_revisions(schema_version,body,created_at) VALUES(?,?,?)`, c.SchemaVersion, string(body), stamp(at))
	if err != nil {
		return config.Revision{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return config.Revision{}, err
	}
	for _, repo := range c.Selection.RepositoryIDs {
		if _, err = tx.ExecContext(ctx, `INSERT INTO selected_repositories VALUES(?,?)`, id, repo); err != nil {
			return config.Revision{}, err
		}
	}
	for _, target := range c.Cloud.Targets {
		b, e := json.Marshal(target)
		if e != nil {
			return config.Revision{}, e
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO cloud_targets VALUES(?,?,?)`, id, target.ID, string(b)); err != nil {
			return config.Revision{}, err
		}
	}
	b, err := json.Marshal(c.Schedule)
	if err != nil {
		return config.Revision{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO schedule_config VALUES(?,?)`, id, string(b)); err != nil {
		return config.Revision{}, err
	}
	return config.NewRevision(id, at, c)
}

// SaveConfig validates a complete edit, writes immutable revision/projections,
// and changes the sole settings head in one compare-and-swap transaction.
func (s *Store) SaveConfig(ctx context.Context, expectedRevision int64, c config.Config) (config.Revision, error) {
	c, err := config.Normalize(c)
	if err != nil {
		return config.Revision{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return config.Revision{}, err
	}
	defer tx.Rollback()
	current, err := readRevision(ctx, tx, 0)
	if err != nil {
		return config.Revision{}, err
	}
	if current.ID() != expectedRevision {
		return config.Revision{}, ErrConflict
	}
	old := current.Config()
	if err = validateTargetHistory(ctx, tx, c.Cloud.Targets); err != nil {
		return config.Revision{}, err
	}
	if !sameString(old.Backup.Root, c.Backup.Root) {
		return config.Revision{}, ErrRootRebindRequired
	}
	if !sameTargets(old.Cloud.Targets, c.Cloud.Targets) {
		if err = ensureIdle(ctx, tx); err != nil {
			return config.Revision{}, err
		}
	}
	revision, err := insertConfig(ctx, tx, c)
	if err != nil {
		return config.Revision{}, err
	}
	if err = s.checkpoint("config.after_projections"); err != nil {
		return config.Revision{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE settings_head SET revision_id=? WHERE singleton=1 AND revision_id=?`, revision.ID(), expectedRevision)
	if err != nil {
		return config.Revision{}, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return config.Revision{}, err
	}
	if n != 1 {
		return config.Revision{}, ErrConflict
	}
	if err = s.checkpoint("config.before_commit"); err != nil {
		return config.Revision{}, err
	}
	if err = tx.Commit(); err != nil {
		return config.Revision{}, err
	}
	return revision, nil
}
func sameString(a, b *string) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }

// Credential rotation and display-independent option edits are next-run edits;
// reinterpreting an existing target destination is not permitted, even idle.
func targetIdentity(t config.Target) string {
	t.Credential = config.Credential{}
	b, _ := json.Marshal(t)
	return string(b)
}
func sameTargets(a, b []config.Target) bool {
	if len(a) != len(b) {
		return false
	}
	ids := map[string]string{}
	for _, t := range a {
		ids[t.ID] = targetIdentity(t)
	}
	for _, t := range b {
		if ids[t.ID] != targetIdentity(t) {
			return false
		}
	}
	return true
}
func validateTargetHistory(ctx context.Context, tx *sql.Tx, targets []config.Target) error {
	for _, target := range targets {
		rows, err := tx.QueryContext(ctx, `SELECT body FROM cloud_targets WHERE target_id=?`, target.ID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var body string
			if err = rows.Scan(&body); err != nil {
				rows.Close()
				return err
			}
			var prior config.Target
			if err = json.Unmarshal([]byte(body), &prior); err != nil {
				rows.Close()
				return err
			}
			if targetIdentity(prior) != targetIdentity(target) {
				rows.Close()
				return fmt.Errorf("%w: target destination requires a new ID", ErrConflict)
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}
	return nil
}
func ensureIdle(ctx context.Context, tx *sql.Tx) error {
	var id string
	err := tx.QueryRowContext(ctx, `SELECT id FROM backup_runs WHERE state IN('preparing','running','cancelling') LIMIT 1`).Scan(&id)
	if err == nil {
		return &BusyError{RunID: id}
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var n int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM leases`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return &BusyError{}
	}
	return nil
}
