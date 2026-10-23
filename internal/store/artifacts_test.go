package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSnapshotTaskPinLeaseRecovery(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(privateTempDir(t), "facts.sqlite3")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	root := bindTestRoot(t, s)
	r, err := s.BeginRun(ctx, RunIntent{ID: "run-1", Trigger: "manual", Kind: "local_backup", RootID: root.ID, RepositoryIDs: []string{"repo-1"}})
	if err != nil {
		t.Fatal(err)
	}
	task := Task{ID: "task-1", RunID: r.ID, RepositoryID: "repo-1", Provider: "local", OperationID: "op-1"}
	if err = s.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	snapshot := Snapshot{ID: "snapshot-1", RepositoryID: "repo-1", Provider: "local", RootID: root.ID, FormatVersion: 1, RelativePath: "snapshots/repo-1/snapshot-1", State: "ready", PublicationState: "published", SHA256: strings.Repeat("a", 64), SizeBytes: 1024, CreatedAt: time.Now().UTC(), Verification: Verification{Manifest: "passed", Hash: "passed", IndependentRefs: "passed", Head: "passed", ObjectIntegrity: "passed", OfflineRestore: "passed"}}
	if err = s.IndexSnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	update := TaskUpdate{Outcome: "pending", Attempt: 1, SnapshotID: snapshot.ID, ArtifactID: snapshot.ID, ArtifactPath: snapshot.RelativePath, PublicationState: "published", CloudOutcome: "failed", ErrorCode: "network"}
	if err = s.UpdateTask(ctx, task.ID, TaskUpdate{Stage: "fetching", Outcome: "succeeded", Attempt: 1, PublicationState: "not_published"}); err == nil {
		t.Fatalf("accepted contradictory stage/outcome: %v", err)
	}
	if err = s.UpdateTask(ctx, task.ID, TaskUpdate{Stage: "uploading", Outcome: "pending", Attempt: 1, SnapshotID: snapshot.ID, ArtifactID: snapshot.ID, ArtifactPath: snapshot.RelativePath, PublicationState: "published", CloudOutcome: "failed"}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("accepted skipped task stages: %v", err)
	}
	if err = s.UpdateTask(ctx, task.ID, TaskUpdate{Stage: "uploading", Outcome: "pending", Attempt: 1, SnapshotID: snapshot.ID, ArtifactID: snapshot.ID, ArtifactPath: snapshot.RelativePath, PublicationState: "published"}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("accepted skipped task stages: %v", err)
	}
	for _, stage := range []string{"fetching", "bundling", "verifying", "publishing", "local_ready", "uploading"} {
		update.Stage = stage
		if err = s.UpdateTask(ctx, task.ID, update); err != nil {
			t.Fatalf("advance task to %s: %v", stage, err)
		}
	}
	if err = s.UpdateTask(ctx, task.ID, TaskUpdate{Stage: "uploading", Outcome: "pending", Attempt: 1, ArtifactID: snapshot.ID, ArtifactPath: snapshot.RelativePath, PublicationState: "published", CloudOutcome: "failed"}); err == nil {
		t.Fatal("task update erased the known snapshot index link")
	}
	if err = s.SetPin(ctx, snapshot.ID, true); err != nil {
		t.Fatal(err)
	}
	if err = s.AcquireLease(ctx, Lease{ID: "lease-1", SnapshotID: snapshot.ID, OwnerID: r.ID, OperationID: "restore-1", AcquiredAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	pins, err := s.ListPins(ctx, 100)
	if err != nil || len(pins) != 1 {
		t.Fatalf("pins %+v %v", pins, err)
	}
	leases, err := s.ListLeases(ctx, 100)
	if err != nil || len(leases) != 1 {
		t.Fatalf("leases %+v %v", leases, err)
	}
	interrupted, err := s.ReconcileInterrupted(ctx)
	if err != nil || len(interrupted) != 1 || interrupted[0].State != "interrupted" {
		t.Fatalf("reconcile %+v %v", interrupted, err)
	}
	tasks, err := s.ListTasks(ctx, r.ID, 100)
	if err != nil || len(tasks) != 1 || tasks[0].Outcome != "interrupted" || tasks[0].PublicationState != "published" || tasks[0].CloudOutcome != "failed" || tasks[0].ArtifactID != snapshot.ID {
		t.Fatalf("lost independent facts: %+v %v", tasks, err)
	}
	snapshots, err := s.ListSnapshots(ctx, "repo-1", 100)
	if err != nil || len(snapshots) != 1 || snapshots[0].State != "ready" {
		t.Fatalf("local publication changed %+v %v", snapshots, err)
	}
	snapshot.SHA256 = strings.Repeat("b", 64)
	if err = s.IndexSnapshot(ctx, snapshot); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting bytes accepted: %v", err)
	}
	if err = s.MarkSnapshotState(ctx, snapshot.ID, "missing"); err != nil {
		t.Fatal(err)
	}
	if err = s.AcquireLease(ctx, Lease{ID: "lease-2", SnapshotID: snapshot.ID, OwnerID: r.ID, OperationID: "restore-2", AcquiredAt: time.Now()}); err == nil {
		t.Fatal("leased missing snapshot")
	}
	if err = s.ReleaseLease(ctx, "lease-1"); err != nil {
		t.Fatal(err)
	}
	if err = s.SetPin(ctx, snapshot.ID, false); err != nil {
		t.Fatal(err)
	}
}
