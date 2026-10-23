package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestRunAdmissionAndReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(privateTempDir(t), "run?policy=#.sqlite3")
	a, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if _, err = a.BeginRun(ctx, RunIntent{ID: "missing-root", Trigger: "manual", Kind: "local_backup", RepositoryIDs: []string{"local-1"}}); err == nil {
		t.Fatal("admitted managed backup without a root binding")
	}
	if runs, queryErr := a.QueryRuns(ctx, 10); queryErr != nil || len(runs) != 0 {
		t.Fatalf("failed admission left a durable run: %+v %v", runs, queryErr)
	}
	b, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	root := bindTestRoot(t, a)
	r, err := a.BeginRun(ctx, RunIntent{ID: "run-1", Trigger: "manual", Kind: "local_backup", RootID: root.ID, RepositoryIDs: []string{"local-1"}, SourcePath: "/synthetic/source"})
	if err != nil {
		t.Fatal(err)
	}
	if r.State != "preparing" || r.Config.ID() != 1 {
		t.Fatalf("admission not captured: %+v", r)
	}
	_, err = b.BeginRun(ctx, RunIntent{ID: "run-2", Trigger: "manual", Kind: "local_backup", RootID: root.ID, RepositoryIDs: []string{"local-2"}, SourcePath: "/synthetic/source"})
	var busy *BusyError
	if !errors.As(err, &busy) || busy.RunID != "run-1" {
		t.Fatalf("wanted busy, got %v", err)
	}
	if err = a.SetRunState(ctx, "run-1", "succeeded", ""); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("preparing to succeeded accepted: %v", err)
	}
	if err = a.SetRunState(ctx, "run-1", "running", ""); err != nil {
		t.Fatal(err)
	}
	if err = a.FinishRun(ctx, "run-1", "cancelled", ""); err != nil {
		t.Fatal(err)
	}
	runs, err := b.QueryRuns(ctx, 10)
	if err != nil || len(runs) != 1 || runs[0].State != "cancelled" {
		t.Fatalf("durable terminal: %+v %v", runs, err)
	}
}
