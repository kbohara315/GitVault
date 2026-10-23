package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

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

// BindRoot persists an explicitly validated idle bind/rebind. A prior root ID
// can never be repointed; create a new root ID for a different filesystem.
func (s *Store) BindRoot(ctx context.Context, r RootBinding) error {
	if r.StoreID != s.identity.StoreID || r.InstallationID != s.identity.InstallationID || r.DataDir != s.identity.DataDir {
		return ErrForeignRoot
	}
	if !validID(r.ID) || !filepath.IsAbs(r.Path) || filepath.Clean(r.Path) != r.Path || strings.ContainsRune(r.Path, 0) || !utf8.ValidString(r.Path) || r.FilesystemID == "" || len(r.FilesystemID) > 1024 || !utf8.ValidString(r.FilesystemID) || strings.ContainsRune(r.FilesystemID, 0) || r.Generation < 1 || !r.Active {
		return errors.New("invalid root binding")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = ensureIdle(ctx, tx); err != nil {
		return err
	}
	old, err := readRoot(ctx, tx, s.identity, r.ID, false)
	if err == nil {
		if old.Path != r.Path || old.FilesystemID != r.FilesystemID || old.Generation != r.Generation {
			return ErrConflict
		}
	} else if !errors.Is(err, ErrForeignRoot) {
		return err
	} else {
		var exists int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM managed_roots WHERE id=?`, r.ID).Scan(&exists); err != nil {
			return err
		}
		if exists != 0 {
			return ErrForeignRoot
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE managed_roots SET active=0 WHERE active=1`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO managed_roots VALUES(?,?,?,?,?,?,?,1) ON CONFLICT(id) DO UPDATE SET active=1`, r.ID, r.Path, r.FilesystemID, r.StoreID, r.InstallationID, r.DataDir, r.Generation); err != nil {
		return err
	}
	return tx.Commit()
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
