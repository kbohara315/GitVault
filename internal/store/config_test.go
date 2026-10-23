package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"gitvault/internal/config"
)

func syntheticTarget() config.Target {
	return config.Target{ID: "target-1", Provider: "s3", Bucket: "synthetic-bucket", Region: "us-east-1", Prefix: "gitvault/", Credential: config.Credential{Reference: "synthetic-handle", Generation: 1}}
}
func TestConfigurationProjectionRollbackAndAdmission(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(privateTempDir(t), "atomic.sqlite3")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	root := bindTestRoot(t, s)
	head, err := s.CurrentConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	c := head.Config()
	c.Selection.RepositoryIDs = []string{"github:101"}
	c.Cloud.Targets = []config.Target{syntheticTarget()}
	c.Schedule.LocalTime = "03:30"
	injected := errors.New("disk failure after projections")
	s.hook = func(stage string) error {
		if stage == "config.after_projections" {
			return injected
		}
		return nil
	}
	if _, err = s.SaveConfig(ctx, head.ID(), c); !errors.Is(err, injected) {
		t.Fatalf("expected injected failure: %v", err)
	}
	s.hook = nil
	for table, want := range map[string]int{"config_revisions": 1, "selected_repositories": 0, "cloud_targets": 0, "schedule_config": 1} {
		var count int
		if err = s.db.QueryRowContext(ctx, `SELECT count(*) FROM `+table).Scan(&count); err != nil || count != want {
			t.Fatalf("partial %s: %d %v", table, count, err)
		}
	}
	head2, err := s.CurrentConfig(ctx)
	if err != nil || head2.ID() != head.ID() {
		t.Fatalf("rollback advanced head: %v", err)
	}
	// Block between projection writes and head CAS, then race admission through
	// a separate SQLite instance. It must capture the entire committed revision.
	other, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	reached := make(chan struct{})
	release := make(chan struct{})
	editDone := make(chan error, 1)
	s.hook = func(stage string) error {
		if stage == "config.after_projections" {
			close(reached)
			<-release
		}
		return nil
	}
	go func() { _, e := s.SaveConfig(ctx, head.ID(), c); editDone <- e }()
	<-reached
	type admission struct {
		r Run
		e error
	}
	admitted := make(chan admission, 1)
	go func() {
		r, e := other.BeginRun(ctx, RunIntent{ID: "atomic-run", Trigger: "manual", Kind: "github_backup", RootID: root.ID})
		admitted <- admission{r, e}
	}()
	close(release)
	if err = <-editDone; err != nil {
		t.Fatal(err)
	}
	result := <-admitted
	if result.e != nil {
		t.Fatal(result.e)
	}
	if result.r.Config.ID() != head.ID()+1 || result.r.Config.Config().Schedule.LocalTime != "03:30" || result.r.Config.Config().Cloud.Targets[0].ID != "target-1" || result.r.Intent.RepositoryIDs[0] != "github:101" {
		t.Fatalf("mixed revision at admission: %+v", result.r)
	}
	var selected, targets, schedules int64
	if err = s.db.QueryRowContext(ctx, `SELECT revision_id FROM selected_repositories`).Scan(&selected); err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRowContext(ctx, `SELECT revision_id FROM cloud_targets`).Scan(&targets); err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRowContext(ctx, `SELECT max(revision_id) FROM schedule_config`).Scan(&schedules); err != nil {
		t.Fatal(err)
	}
	if selected != result.r.Config.ID() || targets != selected || schedules != selected {
		t.Fatal("projections do not share admitted revision")
	}
}

func TestTargetHistoricalIdentityAndRotation(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(privateTempDir(t), "targets.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	root := bindTestRoot(t, s)
	head, _ := s.CurrentConfig(ctx)
	c := head.Config()
	c.Cloud.Targets = []config.Target{syntheticTarget()}
	c.Selection.RepositoryIDs = []string{"github:1"}
	first, err := s.SaveConfig(ctx, head.ID(), c)
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.BeginRun(ctx, RunIntent{ID: "rotation-run", Trigger: "manual", Kind: "github_backup", RootID: root.ID})
	if err != nil {
		t.Fatal(err)
	}
	c = first.Config()
	c.Cloud.Targets[0].Credential.Generation = 2
	rotated, err := s.SaveConfig(ctx, first.ID(), c)
	if err != nil {
		t.Fatalf("next-run rotation blocked: %v", err)
	}
	if err = s.FinishRun(ctx, run.ID, "cancelled", ""); err != nil {
		t.Fatal(err)
	}
	c = rotated.Config()
	c.Cloud.Targets[0].Bucket = "different-bucket"
	if _, err = s.SaveConfig(ctx, rotated.ID(), c); !errors.Is(err, ErrConflict) {
		t.Fatalf("reinterpreted historical target ID: %v", err)
	}
	c.Cloud.Targets[0].ID = "target-2"
	if _, err = s.SaveConfig(ctx, rotated.ID(), c); err != nil {
		t.Fatalf("new destination identity rejected: %v", err)
	}
}
