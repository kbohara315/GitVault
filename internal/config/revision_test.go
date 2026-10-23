package config_test

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"gitvault/internal/config"
	"gitvault/internal/store"
)

func TestConfigImmutableRevision(t *testing.T) {
	ctx := context.Background()
	dir, err := os.MkdirTemp("/tmp", "gitvault-config-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	if err = os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "revisions.sqlite3")
	a, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	identity := a.Identity()
	root := store.RootBinding{ID: "test-root", Path: filepath.Join(dir, "managed"), FilesystemID: "synthetic-filesystem", StoreID: identity.StoreID, InstallationID: identity.InstallationID, DataDir: identity.DataDir, Generation: 1, Active: true}
	if err = a.BindRoot(ctx, root); err != nil {
		t.Fatal(err)
	}
	initial, err := a.CurrentConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	c := initial.Config()
	c.Selection.RepositoryIDs = []string{"github:101"}
	c.GitHub.Credential = &config.Credential{Reference: "synthetic-handle", Generation: 1}
	zone := "Asia/Kathmandu"
	c.Schedule.Timezone = &zone
	c.Cloud.Targets = []config.Target{{ID: "target-1", Provider: "s3", Bucket: "synthetic-bucket", Region: "us-east-1", Prefix: "gitvault/", Credential: config.Credential{Reference: "synthetic-cloud", Generation: 1}}}
	saved, err := a.SaveConfig(ctx, initial.ID(), c)
	if err != nil {
		t.Fatal(err)
	}
	c.Selection.RepositoryIDs[0] = "caller-mutated"
	*c.Schedule.Timezone = "UTC"
	c.GitHub.Credential.Generation = 55
	copy := saved.Config()
	copy.Selection.RepositoryIDs[0] = "query-mutated"
	copy.GitHub.Credential.Generation = 66
	run, err := a.BeginRun(ctx, store.RunIntent{ID: "immutable-run", Trigger: "manual", Kind: "github_backup", RootID: root.ID})
	if err != nil {
		t.Fatal(err)
	}
	edited := saved.Config()
	edited.Selection.RepositoryIDs = []string{"github:202"}
	edited.GitHub.Credential.Generation = 2
	edited.Cloud.Targets[0].Credential.Generation = 2
	edited.Schedule.LocalTime = "03:00"
	next, err := a.SaveConfig(ctx, saved.ID(), edited)
	if err != nil {
		t.Fatal(err)
	}
	previous, err := b.ConfigRevision(ctx, saved.ID())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []config.Revision{saved, previous, run.Config} {
		if got := r.Config(); got.Selection.RepositoryIDs[0] != "github:101" || got.GitHub.Credential.Generation != 1 || *got.Schedule.Timezone != "Asia/Kathmandu" || got.Schedule.LocalTime != "02:00" {
			t.Fatalf("old revision mutated: %+v", got)
		}
	}
	if run.Intent.RepositoryIDs[0] != "github:101" {
		t.Fatal("intent not captured from same revision")
	}
	// Private fault-injection seam in this temporary database only: fail the
	// settings-head write after revision/projection INSERTs have executed.
	uri := url.URL{Scheme: "file", Path: path}
	faultDB, err := sql.Open("sqlite", uri.String())
	if err != nil {
		t.Fatal(err)
	}
	defer faultDB.Close()
	if _, err = faultDB.ExecContext(ctx, `CREATE TRIGGER test_fail_head BEFORE UPDATE ON settings_head BEGIN SELECT RAISE(ABORT,'synthetic write failure'); END`); err != nil {
		t.Fatal(err)
	}
	failedEdit := next.Config()
	failedEdit.Selection.RepositoryIDs = []string{"github:rollback"}
	failedEdit.Schedule.LocalTime = "05:00"
	failedEdit.Cloud.Targets[0].Credential.Generation = 3
	if _, err = a.SaveConfig(ctx, next.ID(), failedEdit); err == nil {
		t.Fatal("injected head-write failure did not fail")
	}
	if _, err = faultDB.ExecContext(ctx, `DROP TRIGGER test_fail_head`); err != nil {
		t.Fatal(err)
	}
	afterFailure, err := b.CurrentConfig(ctx)
	if err != nil || afterFailure.ID() != next.ID() || afterFailure.Config().Selection.RepositoryIDs[0] != "github:202" || afterFailure.Config().Cloud.Targets[0].Credential.Generation != 2 || afterFailure.Config().Schedule.LocalTime != "03:00" {
		t.Fatalf("head failure partially published revision: %v", err)
	}
	if _, err = a.ConfigRevision(ctx, next.ID()+1); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("failed revision survived rollback: %v", err)
	}
	// Invalid complete edit cannot advance the head or partly publish selection.
	invalid := next.Config()
	invalid.Selection.RepositoryIDs = []string{"github:303"}
	invalid.Limits.UIBatchSize = 5000
	if _, err = a.SaveConfig(ctx, next.ID(), invalid); err == nil {
		t.Fatal("invalid edit accepted")
	}
	head, err := b.CurrentConfig(ctx)
	if err != nil || head.ID() != next.ID() || head.Config().Selection.RepositoryIDs[0] != "github:202" {
		t.Fatalf("non-atomic validation: %+v %v", head, err)
	}
	// Two actual SQLite instances compete on exactly the same expected head.
	var wg sync.WaitGroup
	results := make(chan error, 2)
	start := make(chan struct{})
	for i, s := range []*store.Store{a, b} {
		wg.Add(1)
		go func(i int, s *store.Store) {
			defer wg.Done()
			<-start
			c := next.Config()
			c.Backup.RepositoryConcurrency = 3 + i
			_, e := s.SaveConfig(ctx, next.ID(), c)
			results <- e
		}(i, s)
	}
	close(start)
	wg.Wait()
	close(results)
	wins, conflicts := 0, 0
	for e := range results {
		if e == nil {
			wins++
		} else if errors.Is(e, store.ErrConflict) {
			conflicts++
		} else {
			t.Fatal(e)
		}
	}
	if wins != 1 || conflicts != 1 {
		t.Fatalf("CAS winners=%d conflicts=%d", wins, conflicts)
	}
	persisted, err := b.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Config.ID() != saved.ID() || persisted.Config.Config().GitHub.Credential.Generation != 1 {
		t.Fatal("accepted run changed with settings")
	}
	if err = a.FinishRun(ctx, run.ID, "cancelled", ""); err != nil {
		t.Fatal(err)
	}
	head, err = a.CurrentConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	newValues := head.Config()
	newValues.Selection.RepositoryIDs = []string{"github:404"}
	newValues.Schedule.LocalTime = "04:00"
	newValues.Cloud.Targets[0].Credential.Generation = 4
	start = make(chan struct{})
	editResult := make(chan error, 1)
	admitResult := make(chan store.Run, 1)
	admitError := make(chan error, 1)
	go func() { <-start; _, e := a.SaveConfig(ctx, head.ID(), newValues); editResult <- e }()
	go func() {
		<-start
		r, e := b.BeginRun(ctx, store.RunIntent{ID: "race-run", Trigger: "manual", Kind: "github_backup", RootID: root.ID})
		admitResult <- r
		admitError <- e
	}()
	close(start)
	if err = <-editResult; err != nil {
		t.Fatal(err)
	}
	captured := <-admitResult
	if err = <-admitError; err != nil {
		t.Fatal(err)
	}
	values := captured.Config.Config()
	selected := captured.Intent.RepositoryIDs[0]
	switch captured.Config.ID() {
	case head.ID():
		if selected != "github:202" || values.Schedule.LocalTime != "03:00" || values.Cloud.Targets[0].Credential.Generation != 2 {
			t.Fatal("mixed old revision")
		}
	case head.ID() + 1:
		if selected != "github:404" || values.Schedule.LocalTime != "04:00" || values.Cloud.Targets[0].Credential.Generation != 4 {
			t.Fatal("mixed new revision")
		}
	default:
		t.Fatal("unrecognized admitted revision")
	}
}
