package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path"
	"strings"
	"time"
	"unicode/utf8"
)

// Verification contains independent check results; an absent check is never a
// passed check. The coordinator maps recovery-module evidence at its seam.
type Verification struct{ Manifest, Hash, IndependentRefs, Head, ObjectIntegrity, OfflineRestore string }
type Snapshot struct {
	ID, RepositoryID, Provider, RootID            string
	FormatVersion                                 int
	RelativePath, State, PublicationState, SHA256 string
	SizeBytes                                     int64
	CreatedAt                                     time.Time
	Verification                                  Verification
}
type Task struct {
	ID, RunID, RepositoryID, Provider, OperationID                                  string
	Stage, Outcome                                                                  string
	Attempt                                                                         int
	SnapshotID, ArtifactID, ArtifactPath, PublicationState, CloudOutcome, ErrorCode string
}
type TaskUpdate struct {
	Stage, Outcome                                                                  string
	Attempt                                                                         int
	SnapshotID, ArtifactID, ArtifactPath, PublicationState, CloudOutcome, ErrorCode string
}

func publication(s string) bool {
	switch s {
	case "not_published", "published", "published_durability_uncertain", "outcome_unknown_requires_reconciliation":
		return true
	}
	return false
}
func safeRelative(p string) bool {
	return p != "" && len(p) <= 32768 && utf8.ValidString(p) && !strings.ContainsAny(p, "\\\x00") && !strings.HasPrefix(p, "/") && path.Clean(p) == p && p != "." && p != ".." && !strings.HasPrefix(p, "../")
}
func safeRepo(id string) bool {
	return len(id) > 0 && len(id) <= 256 && utf8.ValidString(id) && !strings.ContainsRune(id, 0)
}
func validateSnapshot(v Snapshot) error {
	if !validID(v.ID) || !safeRepo(v.RepositoryID) || !validID(v.Provider) || !validID(v.RootID) || v.FormatVersion != 1 || !safeRelative(v.RelativePath) || v.SizeBytes < 0 || v.CreatedAt.IsZero() || !publication(v.PublicationState) {
		return errors.New("invalid snapshot identity or facts")
	}
	switch v.State {
	case "staging", "ready", "missing", "corrupt", "abandoned":
	default:
		return errors.New("deletion states require a scoped deletion-intent API")
	}
	hash, err := hex.DecodeString(v.SHA256)
	if err != nil || len(hash) != 32 || strings.ToLower(v.SHA256) != v.SHA256 {
		return errors.New("invalid SHA-256")
	}
	for _, check := range []string{v.Verification.Manifest, v.Verification.Hash, v.Verification.IndependentRefs, v.Verification.Head, v.Verification.ObjectIntegrity, v.Verification.OfflineRestore} {
		if check != "" && check != "passed" && check != "failed" {
			return errors.New("invalid check evidence")
		}
		if v.State == "ready" && check != "passed" {
			return errors.New("ready snapshot requires all local verification checks")
		}
	}
	if v.State == "ready" && v.PublicationState != "published" && v.PublicationState != "published_durability_uncertain" {
		return errors.New("ready requires known publication")
	}
	return nil
}

const snapshotColumns = `id,repository_id,provider,root_id,format_version,relative_path,state,publication_state,sha256,size_bytes,created_at,verification`

func scanSnapshot(row interface{ Scan(...any) error }) (Snapshot, error) {
	var v Snapshot
	var at, evidence string
	err := row.Scan(&v.ID, &v.RepositoryID, &v.Provider, &v.RootID, &v.FormatVersion, &v.RelativePath, &v.State, &v.PublicationState, &v.SHA256, &v.SizeBytes, &at, &evidence)
	if errors.Is(err, sql.ErrNoRows) {
		return v, ErrNotFound
	}
	if err != nil {
		return v, err
	}
	v.CreatedAt, err = parseStamp(at)
	if err != nil {
		return v, err
	}
	err = json.Unmarshal([]byte(evidence), &v.Verification)
	return v, err
}

// IndexSnapshot records validated observed facts without inspecting bytes.
// Duplicate identity is idempotent only for exactly matching captured facts.
func (s *Store) IndexSnapshot(ctx context.Context, v Snapshot) error {
	if err := validateSnapshot(v); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = readRoot(ctx, tx, s.identity, v.RootID, true); err != nil {
		return err
	}
	old, err := scanSnapshot(tx.QueryRowContext(ctx, `SELECT `+snapshotColumns+` FROM snapshots WHERE id=?`, v.ID))
	if err == nil {
		if !sameSnapshot(old, v) {
			return ErrConflict
		}
		return tx.Commit()
	}
	if !errors.Is(err, ErrNotFound) {
		return err
	}
	var collision int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM snapshots WHERE root_id=? AND relative_path=?`, v.RootID, v.RelativePath).Scan(&collision); err != nil {
		return err
	}
	if collision != 0 {
		return ErrConflict
	}
	evidence, err := json.Marshal(v.Verification)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO snapshots VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, v.ID, v.RepositoryID, v.Provider, v.RootID, v.FormatVersion, v.RelativePath, v.State, v.PublicationState, v.SHA256, v.SizeBytes, stamp(v.CreatedAt), string(evidence)); err != nil {
		return err
	}
	if err = s.checkpoint("snapshot.before_commit"); err != nil {
		return err
	}
	return tx.Commit()
}
func sameSnapshot(a, b Snapshot) bool {
	at, bt := a.CreatedAt, b.CreatedAt
	a.CreatedAt = time.Time{}
	b.CreatedAt = time.Time{}
	return a == b && at.Equal(bt)
}
func (s *Store) GetSnapshot(ctx context.Context, id string) (Snapshot, error) {
	return scanSnapshot(s.db.QueryRowContext(ctx, `SELECT `+snapshotColumns+` FROM snapshots WHERE id=?`, id))
}
func (s *Store) ListSnapshots(ctx context.Context, repositoryID string, limit int) ([]Snapshot, error) {
	if limit < 1 || limit > 10000 {
		return nil, errors.New("snapshot query limit must be 1..10000")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+snapshotColumns+` FROM snapshots WHERE (?='' OR repository_id=?) ORDER BY created_at DESC,id DESC LIMIT ?`, repositoryID, repositoryID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Snapshot{}
	for rows.Next() {
		v, e := scanSnapshot(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// MarkSnapshotState only reduces recoverability claims. It cannot grant ready
// status or deletion authority. Revalidation/reindex requires explicit recovery.
func (s *Store) MarkSnapshotState(ctx context.Context, id, state string) error {
	if state != "missing" && state != "corrupt" && state != "abandoned" {
		return ErrInvalidTransition
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	v, err := scanSnapshot(tx.QueryRowContext(ctx, `SELECT `+snapshotColumns+` FROM snapshots WHERE id=?`, id))
	if err != nil {
		return err
	}
	if _, err = readRoot(ctx, tx, s.identity, v.RootID, true); err != nil {
		return err
	}
	if v.State == "deleting" || v.State == "deleted" {
		return ErrInvalidTransition
	}
	if _, err = tx.ExecContext(ctx, `UPDATE snapshots SET state=? WHERE id=?`, state, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CreateTask(ctx context.Context, t Task) error {
	if !validID(t.ID) || !validID(t.RunID) || !safeRepo(t.RepositoryID) || !validID(t.Provider) || !validID(t.OperationID) {
		return errors.New("invalid task identity")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var state, intentJSON string
	if err = tx.QueryRowContext(ctx, `SELECT state,intent FROM backup_runs WHERE id=?`, t.RunID).Scan(&state, &intentJSON); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	if terminal(state) {
		return ErrInvalidTransition
	}
	var intent RunIntent
	if err = json.Unmarshal([]byte(intentJSON), &intent); err != nil {
		return err
	}
	selected := false
	for _, id := range intent.RepositoryIDs {
		if id == t.RepositoryID {
			selected = true
		}
	}
	if !selected {
		return errors.New("task repository was not admitted")
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO repository_tasks VALUES(?,?,?,?,?,'pending','pending',1,NULL,'','','not_published','not_requested','')`, t.ID, t.RunID, t.RepositoryID, t.Provider, t.OperationID); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) UpdateTask(ctx context.Context, id string, u TaskUpdate) error {
	switch u.Stage {
	case "pending", "fetching", "bundling", "verifying", "publishing", "local_ready", "uploading", "succeeded", "failed":
	default:
		return errors.New("invalid task stage")
	}
	switch u.Outcome {
	case "pending", "succeeded", "failed", "cancelled", "interrupted", "empty_repository":
	default:
		return errors.New("invalid task outcome")
	}
	switch u.CloudOutcome {
	case "", "not_requested", "pending", "uploading", "uploaded", "failed", "cancelled", "unknown":
	default:
		return errors.New("invalid cloud outcome")
	}
	if u.CloudOutcome == "" {
		u.CloudOutcome = "not_requested"
	}
	if !taskStageOutcomeConsistent(u.Stage, u.Outcome) {
		return errors.New("task stage and outcome are inconsistent")
	}
	if u.Attempt < 1 || u.Attempt > 8 || !publication(u.PublicationState) || (u.ErrorCode != "" && !validID(u.ErrorCode)) {
		return errors.New("invalid task result")
	}
	if u.PublicationState != "not_published" && (u.ArtifactID == "" || u.ArtifactPath == "") {
		return errors.New("publication requires retained artifact identity")
	}
	if (u.ArtifactID != "" && !validID(u.ArtifactID)) || len(u.ArtifactPath) > 32768 || strings.ContainsRune(u.ArtifactPath, 0) || !utf8.ValidString(u.ArtifactPath) {
		return errors.New("invalid artifact identity")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var repo, provider, oldStage, oldOutcome, oldArtifact, oldPath, oldPublication, oldSnapshot, runState string
	if err = tx.QueryRowContext(ctx, `SELECT t.repository_id,t.provider,t.stage,t.outcome,t.artifact_id,t.artifact_path,t.publication_state,coalesce(t.snapshot_id,''),r.state FROM repository_tasks t JOIN backup_runs r ON r.id=t.run_id WHERE t.id=?`, id).Scan(&repo, &provider, &oldStage, &oldOutcome, &oldArtifact, &oldPath, &oldPublication, &oldSnapshot, &runState); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	if oldOutcome != "pending" || terminal(runState) {
		return ErrInvalidTransition
	}
	if !taskStageTransitionAllowed(oldStage, u.Stage) {
		return ErrInvalidTransition
	}
	if oldPublication != "not_published" && (u.PublicationState == "not_published" || oldArtifact != u.ArtifactID || oldPath != u.ArtifactPath) {
		return errors.New("cannot erase or replace known publication facts")
	}
	if oldSnapshot != "" && u.SnapshotID != oldSnapshot {
		return ErrConflict
	}
	var snapshot any
	if u.SnapshotID != "" {
		var sr, sp string
		if err = tx.QueryRowContext(ctx, `SELECT repository_id,provider FROM snapshots WHERE id=?`, u.SnapshotID).Scan(&sr, &sp); err != nil {
			return err
		}
		if sr != repo || sp != provider || u.ArtifactID != u.SnapshotID {
			return ErrConflict
		}
		snapshot = u.SnapshotID
	}
	if _, err = tx.ExecContext(ctx, `UPDATE repository_tasks SET stage=?,outcome=?,attempt=?,snapshot_id=?,artifact_id=?,artifact_path=?,publication_state=?,cloud_outcome=?,error_code=? WHERE id=?`, u.Stage, u.Outcome, u.Attempt, snapshot, u.ArtifactID, u.ArtifactPath, u.PublicationState, u.CloudOutcome, u.ErrorCode, id); err != nil {
		return err
	}
	return tx.Commit()
}

func taskStageOutcomeConsistent(stage, outcome string) bool {
	switch outcome {
	case "pending":
		return stage != "succeeded" && stage != "failed"
	case "succeeded":
		return stage == "succeeded"
	case "failed":
		return stage == "failed"
	case "cancelled", "interrupted", "empty_repository":
		return stage != "succeeded" && stage != "failed"
	default:
		return false
	}
}

func taskStageTransitionAllowed(from, to string) bool {
	if from == to || to == "failed" {
		return true
	}
	if from == "local_ready" && to == "succeeded" {
		return true // local-only completion; an upload is optional.
	}
	allowed := map[string]string{"pending": "fetching", "fetching": "bundling", "bundling": "verifying", "verifying": "publishing", "publishing": "local_ready", "local_ready": "uploading", "uploading": "succeeded"}
	return allowed[from] == to
}
func (s *Store) ListTasks(ctx context.Context, runID string, limit int) ([]Task, error) {
	if limit < 1 || limit > 10000 {
		return nil, errors.New("task query limit must be 1..10000")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,run_id,repository_id,provider,operation_id,stage,outcome,attempt,coalesce(snapshot_id,''),artifact_id,artifact_path,publication_state,cloud_outcome,error_code FROM repository_tasks WHERE run_id=? ORDER BY id LIMIT ?`, runID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Task{}
	for rows.Next() {
		var t Task
		if err = rows.Scan(&t.ID, &t.RunID, &t.RepositoryID, &t.Provider, &t.OperationID, &t.Stage, &t.Outcome, &t.Attempt, &t.SnapshotID, &t.ArtifactID, &t.ArtifactPath, &t.PublicationState, &t.CloudOutcome, &t.ErrorCode); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
