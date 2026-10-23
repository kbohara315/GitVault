package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"unicode/utf8"

	"gitvault/internal/config"
)

var ErrRootRebindUnknown = errors.New("root rebind commit outcome is unknown; keep the marker and reconcile")

// RootBinding is the exact identity from the marker validated by the caller
// under the root OS lock. FilesystemID is a qualified canonical filesystem
// identity, not merely the display path. No automatic adoption is supported.
type RootBinding struct {
	ID, Path, FilesystemID, StoreID, InstallationID, DataDir string
	Generation                                               int64
	Active                                                   bool
}

func readRoot(ctx context.Context, q queryer, identity Identity, id string, active bool) (RootBinding, error) {
	var r RootBinding
	err := q.QueryRowContext(ctx, `SELECT id,path,filesystem_id,store_id,installation_id,data_dir,generation,active FROM managed_roots WHERE id=?`, id).Scan(&r.ID, &r.Path, &r.FilesystemID, &r.StoreID, &r.InstallationID, &r.DataDir, &r.Generation, &r.Active)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrForeignRoot
	}
	if err != nil {
		return r, err
	}
	if r.StoreID != identity.StoreID || r.InstallationID != identity.InstallationID || r.DataDir != identity.DataDir || (active && !r.Active) {
		return r, ErrForeignRoot
	}
	return r, nil
}
func (s *Store) RootBinding(ctx context.Context, id string) (RootBinding, error) {
	return readRoot(ctx, s.db, s.identity, id, true)
}

// RegisteredRoot returns an identity owned by this store whether or not it is
// currently active. Callers may use it only to implement an explicit, idle
// rebind after validating the matching on-disk authority marker.
func (s *Store) RegisteredRoot(ctx context.Context, id string) (RootBinding, error) {
	return readRoot(ctx, s.db, s.identity, id, false)
}

// ActiveRoot returns the single current managed-root binding, if one exists.
func (s *Store) ActiveRoot(ctx context.Context) (RootBinding, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM managed_roots WHERE active=1`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return RootBinding{}, ErrNotFound
	}
	if err != nil {
		return RootBinding{}, err
	}
	return readRoot(ctx, s.db, s.identity, id, true)
}

// RebindRootConfig atomically changes the active database binding, immutable
// configuration revision, revision-keyed projections, and settings head. The
// caller holds the selected root's OS ownership locks and supplies a narrowly
// scoped marker-publication callback. If the transaction is known to roll back,
// the returned rollback function removes only the marker it just published. If
// Commit itself fails, the outcome is uncertain and the marker is preserved for
// reconciliation rather than risking deletion of committed authority.
func (s *Store) RebindRootConfig(ctx context.Context, expectedRevision int64, values config.Config, root RootBinding, publishMarker func() (rollback func() error, err error)) (config.Revision, error) {
	values, err := config.Normalize(values)
	if err != nil {
		return config.Revision{}, err
	}
	if expectedRevision < 1 || root.StoreID != s.identity.StoreID || root.InstallationID != s.identity.InstallationID || root.DataDir != s.identity.DataDir {
		return config.Revision{}, ErrForeignRoot
	}
	if publishMarker == nil {
		return config.Revision{}, errors.New("managed-root marker publication is required")
	}
	if !validID(root.ID) || !filepath.IsAbs(root.Path) || filepath.Clean(root.Path) != root.Path || strings.ContainsRune(root.Path, 0) || !utf8.ValidString(root.Path) || root.FilesystemID == "" || len(root.FilesystemID) > 1024 || !utf8.ValidString(root.FilesystemID) || strings.ContainsRune(root.FilesystemID, 0) || root.Generation < 1 || !root.Active {
		return config.Revision{}, errors.New("invalid root binding")
	}
	if values.Backup.Root == nil || *values.Backup.Root != root.Path {
		return config.Revision{}, errors.New("configuration root must match the explicitly rebound root")
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
	currentValues := current.Config()
	requestedValues := values.Clone()
	currentValues.Backup.Root = nil
	requestedValues.Backup.Root = nil
	if !reflect.DeepEqual(currentValues, requestedValues) {
		return config.Revision{}, errors.New("root rebind cannot change unrelated settings")
	}
	if err = ensureIdle(ctx, tx); err != nil {
		return config.Revision{}, err
	}
	if rootPathsOverlap(root.Path, root.DataDir) {
		return config.Revision{}, ErrOverlappingRoots
	}
	if err = validateNoRootOverlap(ctx, tx, root); err != nil {
		return config.Revision{}, err
	}
	old, oldErr := readRoot(ctx, tx, s.identity, root.ID, false)
	if oldErr == nil {
		if old.Path != root.Path || old.FilesystemID != root.FilesystemID || old.Generation != root.Generation {
			return config.Revision{}, ErrConflict
		}
	} else if !errors.Is(oldErr, ErrForeignRoot) {
		return config.Revision{}, oldErr
	} else {
		var exists int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM managed_roots WHERE id=?`, root.ID).Scan(&exists); err != nil {
			return config.Revision{}, err
		}
		if exists != 0 {
			return config.Revision{}, ErrForeignRoot
		}
	}
	if err = validateTargetHistory(ctx, tx, values.Cloud.Targets); err != nil {
		return config.Revision{}, err
	}
	if !sameTargets(current.Config().Cloud.Targets, values.Cloud.Targets) {
		return config.Revision{}, errors.New("root rebind cannot also change cloud target identity")
	}
	if _, err = tx.ExecContext(ctx, `UPDATE managed_roots SET active=0 WHERE active=1`); err != nil {
		return config.Revision{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO managed_roots VALUES(?,?,?,?,?,?,?,1) ON CONFLICT(id) DO UPDATE SET active=1`, root.ID, root.Path, root.FilesystemID, root.StoreID, root.InstallationID, root.DataDir, root.Generation); err != nil {
		return config.Revision{}, err
	}
	revision, err := insertConfig(ctx, tx, values)
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
	changed, err := result.RowsAffected()
	if err != nil {
		return config.Revision{}, err
	}
	if changed != 1 {
		return config.Revision{}, ErrConflict
	}
	rollbackMarker := func() error { return nil }
	if publishMarker != nil {
		rollbackMarker, err = publishMarker()
		if err != nil {
			return config.Revision{}, err
		}
		if rollbackMarker == nil {
			rollbackMarker = func() error { return nil }
		}
	}
	if err = s.checkpoint("root_rebind.before_commit"); err != nil {
		return config.Revision{}, errors.Join(err, rollbackMarker())
	}
	if err = tx.Commit(); err != nil {
		return config.Revision{}, fmt.Errorf("%w: %w", ErrRootRebindUnknown, err)
	}
	return revision, nil
}

func validateNoRootOverlap(ctx context.Context, tx *sql.Tx, candidate RootBinding) error {
	rows, err := tx.QueryContext(ctx, `SELECT id,path FROM managed_roots`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, path string
		if err = rows.Scan(&id, &path); err != nil {
			return err
		}
		if id == candidate.ID && path == candidate.Path {
			continue
		}
		if rootPathsOverlap(path, candidate.Path) {
			return ErrOverlappingRoots
		}
	}
	return rows.Err()
}

func rootPathsOverlap(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	for _, pair := range [][2]string{{a, b}, {b, a}} {
		relative, err := filepath.Rel(pair[0], pair[1])
		if err != nil || relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// ValidateRoot checks the complete marker identity against the persisted active
// binding. It grants no filesystem lock and performs no marker IO.
func (s *Store) ValidateRoot(ctx context.Context, expected RootBinding) error {
	actual, err := s.RootBinding(ctx, expected.ID)
	if err != nil {
		return err
	}
	if actual != expected {
		return ErrForeignRoot
	}
	return nil
}

// compile-time assertion that transaction readers retain the same authority seam.
var _ queryer = (*sql.Tx)(nil)
