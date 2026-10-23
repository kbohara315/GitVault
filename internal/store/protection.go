package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type Pin struct {
	SnapshotID string
	CreatedAt  time.Time
}

// Expiry is evidence for reconciliation, never automatic deletion permission.
// ReleaseLease requires the owner/coordinator to have established input liveness.
type Lease struct {
	ID, SnapshotID, OwnerID, OperationID string
	AcquiredAt                           time.Time
	ExpiresAt                            *time.Time
}

func (s *Store) protectable(ctx context.Context, tx *sql.Tx, id string, requireReady bool) error {
	var root, state string
	if err := tx.QueryRowContext(ctx, `SELECT root_id,state FROM snapshots WHERE id=?`, id).Scan(&root, &state); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	if _, err := readRoot(ctx, tx, s.identity, root, true); err != nil {
		return err
	}
	if requireReady && state != "ready" {
		return errors.New("snapshot is not ready")
	}
	if state == "deleting" || state == "deleted" {
		return errors.New("snapshot deletion has begun")
	}
	return nil
}
func (s *Store) SetPin(ctx context.Context, snapshotID string, pinned bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = s.protectable(ctx, tx, snapshotID, false); err != nil {
		return err
	}
	if pinned {
		_, err = tx.ExecContext(ctx, `INSERT INTO pins VALUES(?,?) ON CONFLICT(snapshot_id) DO NOTHING`, snapshotID, stamp(time.Now()))
	} else {
		_, err = tx.ExecContext(ctx, `DELETE FROM pins WHERE snapshot_id=?`, snapshotID)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) ListPins(ctx context.Context, limit int) ([]Pin, error) {
	if limit < 1 || limit > 10000 {
		return nil, errors.New("pin query limit must be 1..10000")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT snapshot_id,created_at FROM pins ORDER BY snapshot_id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Pin{}
	for rows.Next() {
		var p Pin
		var at string
		if err = rows.Scan(&p.SnapshotID, &at); err != nil {
			return nil, err
		}
		p.CreatedAt, err = parseStamp(at)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
func (s *Store) AcquireLease(ctx context.Context, l Lease) error {
	if !validID(l.ID) || !validID(l.SnapshotID) || !validID(l.OwnerID) || !validID(l.OperationID) || l.AcquiredAt.IsZero() || (l.ExpiresAt != nil && !l.ExpiresAt.After(l.AcquiredAt)) {
		return errors.New("invalid lease")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = s.protectable(ctx, tx, l.SnapshotID, true); err != nil {
		return err
	}
	var end any
	if l.ExpiresAt != nil {
		end = stamp(*l.ExpiresAt)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO leases VALUES(?,?,?,?,?,?)`, l.ID, l.SnapshotID, l.OwnerID, l.OperationID, stamp(l.AcquiredAt), end); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) ReleaseLease(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var snapshotID string
	if err = tx.QueryRowContext(ctx, `SELECT snapshot_id FROM leases WHERE id=?`, id).Scan(&snapshotID); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	if err = s.protectable(ctx, tx, snapshotID, false); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM leases WHERE id=?`, id); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) ListLeases(ctx context.Context, limit int) ([]Lease, error) {
	if limit < 1 || limit > 10000 {
		return nil, errors.New("lease query limit must be 1..10000")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,snapshot_id,owner_id,operation_id,acquired_at,expires_at FROM leases ORDER BY id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Lease{}
	for rows.Next() {
		var l Lease
		var at string
		var end sql.NullString
		if err = rows.Scan(&l.ID, &l.SnapshotID, &l.OwnerID, &l.OperationID, &at, &end); err != nil {
			return nil, err
		}
		l.AcquiredAt, err = parseStamp(at)
		if err != nil {
			return nil, err
		}
		if end.Valid {
			v, e := parseStamp(end.String)
			if e != nil {
				return nil, e
			}
			l.ExpiresAt = &v
		}
		out = append(out, l)
	}
	return out, rows.Err()
}
