package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"gitvault/internal/config"
)

// RunIntent contains non-secret explicit caller intent. Github admission always
// captures selection from the same settings revision, never a mutable catalog.
// RootID, when present, must refer to this database's active binding. The caller
// must also validate its filesystem marker while holding the OS root lock.
type RunIntent struct {
	ID              string   `json:"id"`
	Trigger         string   `json:"trigger"`
	Kind            string   `json:"kind"`
	RootID          string   `json:"root_id,omitempty"`
	RepositoryIDs   []string `json:"repository_ids"`
	SourcePath      string   `json:"source_path,omitempty"`
	DestinationPath string   `json:"destination_path,omitempty"`
	SnapshotID      string   `json:"snapshot_id,omitempty"`
	TargetID        string   `json:"target_id,omitempty"`
}
type Run struct {
	ID, Trigger, State string
	Intent             RunIntent
	Config             config.Revision
	StartedAt          time.Time
	FinishedAt         *time.Time
	ErrorCode          string
}

func (s *Store) BeginRun(ctx context.Context, intent RunIntent) (Run, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Run{}, err
	}
	defer tx.Rollback()
	revision, err := readRevision(ctx, tx, 0)
	if err != nil {
		return Run{}, err
	}
	if intent.ID == "" {
		intent.ID = newID()
	}
	if !validID(intent.ID) {
		return Run{}, errors.New("invalid run ID")
	}
	switch intent.Trigger {
	case "manual", "scheduled", "catch_up", "retry":
	default:
		return Run{}, errors.New("invalid run trigger")
	}
	switch intent.Kind {
	case "local_backup", "github_backup", "restore", "upload_retry":
	default:
		return Run{}, errors.New("invalid operation kind")
	}
	if intent.Kind == "github_backup" {
		intent.RepositoryIDs = revision.Config().Selection.RepositoryIDs
	}
	if len(intent.RepositoryIDs) > 10000 {
		return Run{}, errors.New("too many repository IDs")
	}
	seen := map[string]bool{}
	for _, id := range intent.RepositoryIDs {
		if !safeRepo(id) || seen[id] {
			return Run{}, errors.New("invalid explicit repository selection")
		}
		seen[id] = true
	}
	if (intent.Kind == "local_backup" || intent.Kind == "github_backup") && len(intent.RepositoryIDs) == 0 {
		return Run{}, errors.New("empty explicit selection")
	}
	if (intent.Kind == "local_backup" || intent.Kind == "github_backup") && intent.RootID == "" {
		return Run{}, errors.New("managed backup requires an active root binding")
	}
	for _, p := range []string{intent.SourcePath, intent.DestinationPath} {
		if p != "" && (!filepath.IsAbs(p) || filepath.Clean(p) != p || strings.ContainsRune(p, 0) || !utf8.ValidString(p)) {
			return Run{}, errors.New("intent paths must be clean absolute local paths")
		}
	}
	if intent.RootID != "" {
		if _, err = readRoot(ctx, tx, s.identity, intent.RootID, true); err != nil {
			return Run{}, err
		}
	}
	var active string
	err = tx.QueryRowContext(ctx, `SELECT id FROM backup_runs WHERE state IN('preparing','running','cancelling')`).Scan(&active)
	if err == nil {
		return Run{}, &BusyError{RunID: active}
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Run{}, err
	}
	b, err := json.Marshal(intent)
	if err != nil {
		return Run{}, err
	}
	if len(b) > config.MaxJSONBytes {
		return Run{}, errors.New("intent exceeds budget")
	}
	at := time.Now().UTC()
	if _, err = tx.ExecContext(ctx, `INSERT INTO backup_runs(id,trigger,state,config_revision_id,intent,started_at) VALUES(?,?,'preparing',?,?,?)`, intent.ID, intent.Trigger, revision.ID(), string(b), stamp(at)); err != nil {
		return Run{}, err
	}
	if err = s.checkpoint("run.before_commit"); err != nil {
		return Run{}, err
	}
	if err = tx.Commit(); err != nil {
		return Run{}, err
	}
	// Re-decode the serialized intent: callers retain ownership of input slices.
	var captured RunIntent
	if err = json.Unmarshal(b, &captured); err != nil {
		return Run{}, err
	}
	return Run{ID: intent.ID, Trigger: intent.Trigger, State: "preparing", Intent: captured, Config: revision, StartedAt: at}, nil
}
func terminal(state string) bool {
	switch state {
	case "succeeded", "partial", "failed", "cancelled", "interrupted":
		return true
	}
	return false
}

// ValidRunTransition is the single durable transition vocabulary. App policy
// may impose stronger rules (not releasing ownership before child reaping).
func ValidRunTransition(from, to string) bool {
	switch from {
	case "preparing":
		return to == "running" || to == "cancelling" || to == "failed" || to == "cancelled" || to == "interrupted"
	case "running":
		return to == "cancelling" || terminal(to)
	case "cancelling":
		return terminal(to)
	}
	return false
}
func (s *Store) SetRunState(ctx context.Context, id, state, errorCode string) error {
	if errorCode != "" && !validID(errorCode) {
		return errors.New("error code must be structured, not diagnostic text")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var from string
	if err = tx.QueryRowContext(ctx, `SELECT state FROM backup_runs WHERE id=?`, id).Scan(&from); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	if !ValidRunTransition(from, state) {
		return fmt.Errorf("%w: %s to %s", ErrInvalidTransition, from, state)
	}
	var finished any
	if terminal(state) {
		finished = stamp(time.Now())
	}
	if _, err = tx.ExecContext(ctx, `UPDATE backup_runs SET state=?,finished_at=?,error_code=? WHERE id=?`, state, finished, errorCode, id); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) FinishRun(ctx context.Context, id, outcome, errorCode string) error {
	if !terminal(outcome) {
		return ErrInvalidTransition
	}
	return s.SetRunState(ctx, id, outcome, errorCode)
}

func scanRun(row interface{ Scan(...any) error }) (Run, int64, error) {
	var r Run
	var revision int64
	var body, start string
	var end sql.NullString
	err := row.Scan(&r.ID, &r.Trigger, &r.State, &revision, &body, &start, &end, &r.ErrorCode)
	if errors.Is(err, sql.ErrNoRows) {
		return r, 0, ErrNotFound
	}
	if err != nil {
		return r, 0, err
	}
	if err = json.Unmarshal([]byte(body), &r.Intent); err != nil {
		return r, 0, err
	}
	r.StartedAt, err = parseStamp(start)
	if err != nil {
		return r, 0, err
	}
	if end.Valid {
		at, e := parseStamp(end.String)
		if e != nil {
			return r, 0, e
		}
		r.FinishedAt = &at
	}
	return r, revision, nil
}

const runColumns = `id,trigger,state,config_revision_id,intent,started_at,finished_at,error_code`

func (s *Store) GetRun(ctx context.Context, id string) (Run, error) {
	r, revision, err := scanRun(s.db.QueryRowContext(ctx, `SELECT `+runColumns+` FROM backup_runs WHERE id=?`, id))
	if err != nil {
		return Run{}, err
	}
	r.Config, err = s.ConfigRevision(ctx, revision)
	return r, err
}
func (s *Store) QueryRuns(ctx context.Context, limit int) ([]Run, error) {
	if limit < 1 || limit > 1000 {
		return nil, errors.New("run query limit must be 1..1000")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+runColumns+` FROM backup_runs ORDER BY started_at DESC,id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	out := []Run{}
	revisions := []int64{}
	for rows.Next() {
		r, v, e := scanRun(rows)
		if e != nil {
			rows.Close()
			return nil, e
		}
		out = append(out, r)
		revisions = append(revisions, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for i, v := range revisions {
		out[i].Config, err = s.ConfigRevision(ctx, v)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ReconcileInterrupted only records owner loss; it never removes staging,
// reimports snapshots, releases leases, or performs remote effects. Call only
// after establishing previous process/root ownership is gone under OS locks.
func (s *Store) ReconcileInterrupted(ctx context.Context) ([]Run, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id FROM backup_runs WHERE state IN('preparing','running','cancelling') LIMIT 1`)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		if _, err = tx.ExecContext(ctx, `UPDATE repository_tasks SET outcome='interrupted',error_code='owner_lost' WHERE run_id=? AND outcome='pending'`, id); err != nil {
			return nil, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE backup_runs SET state='interrupted',finished_at=?,error_code='owner_lost' WHERE id=?`, stamp(time.Now()), id); err != nil {
			return nil, err
		}
	}
	if err = s.checkpoint("reconcile.before_commit"); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	out := []Run{}
	for _, id := range ids {
		r, e := s.GetRun(ctx, id)
		if e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, nil
}
