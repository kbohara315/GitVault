package desktop

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gitvault/internal/app"
	"gitvault/internal/backup"
	"gitvault/internal/config"
	"gitvault/internal/store"
)

func workflowStore(t *testing.T) (*store.Store, string) {
	t.Helper()
	base := t.TempDir()
	if err := os.Chmod(base, 0700); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(base, "app-data")
	if err := os.Mkdir(data, 0700); err != nil {
		t.Fatal(err)
	}
	s, err := store.OpenWithInstallation(context.Background(), filepath.Join(data, "gitvault.sqlite3"), "desktop-workflow-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	return s, base
}

func newWorkflow(t *testing.T, s *store.Store, build app.LocalEngineBuilder) *app.LocalWorkflow {
	t.Helper()
	w, err := app.NewLocalWorkflow(context.Background(), s, build)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := w.Close(ctx); err != nil {
			t.Errorf("close local workflow: %v", err)
		}
	})
	return w
}

func localEngineBuilder(s *store.Store) app.LocalEngineBuilder {
	return func(root store.RootBinding, revision config.Revision, progress chan<- backup.ProgressEvent) (app.SnapshotEngine, error) {
		values := revision.Config()
		identity := s.Identity()
		scratch := filepath.Join(identity.DataDir, "git-scratch")
		if err := os.MkdirAll(scratch, 0700); err != nil {
			return nil, err
		}
		runnerOptions := backup.DefaultGitRunnerOptions()
		runnerOptions.CommandTimeout = values.Timeouts.Git.Value()
		runnerOptions.MachineOutputBytes = int(values.Limits.MachineOutputBytes)
		runnerOptions.DiagnosticBytes = int(values.Limits.DiagnosticBytes)
		runner, err := backup.NewGitRunner(scratch, runnerOptions)
		if err != nil {
			return nil, err
		}
		limits := backup.ManifestLimits{
			ManifestBytes:      values.Limits.ManifestBytes,
			ReferenceCount:     values.Limits.ReferenceCount,
			ReferenceNameBytes: values.Limits.ReferenceNameBytes,
			MetadataBytes:      values.Limits.ManifestBytes,
			BundleBytes:        values.Limits.BundleBytes,
		}
		return backup.NewEngine(backup.EngineOptions{
			Root:                     root.Path,
			Runner:                   runner,
			Limits:                   limits,
			VerificationScratchBytes: values.Limits.VerificationScratchBytes,
			InstallationID:           identity.InstallationID,
			StoreID:                  identity.StoreID,
			ApplicationData:          identity.DataDir,
			Progress:                 progress,
		})
	}
}

func makeWorkflowRepository(t *testing.T, path string) {
	t.Helper()
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	gitCommand(t, "init", "--initial-branch=main", path)
	if err := os.WriteFile(filepath.Join(path, "history.txt"), []byte("synthetic history\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitCommand(t, "-C", path, "add", "history.txt")
	gitCommand(t, "-C", path, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "initial")
}

func gitCommand(t *testing.T, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v: %s", args, err, out)
	}
}

func submitWorkflow(t *testing.T, w *app.LocalWorkflow, command app.Command) {
	t.Helper()
	if err := w.Submit(command); err != nil {
		t.Fatal(err)
	}
}

func waitWorkflow(t *testing.T, w *app.LocalWorkflow, predicate func(app.ViewState) bool) app.ViewState {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		state := w.Snapshot()
		if predicate(state) {
			return state
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("workflow state did not reach expectation; last state: %+v", w.Snapshot())
	return app.ViewState{}
}

func TestLocalWorkflowCommands(t *testing.T) {
	s, base := workflowStore(t)
	rootA := filepath.Join(base, "vault-a")
	rootB := filepath.Join(base, "vault-b")
	for _, root := range []string{rootA, rootB} {
		if err := os.Mkdir(root, 0700); err != nil {
			t.Fatal(err)
		}
	}
	markerFile := filepath.Join(rootA, "user-data.txt")
	if err := os.WriteFile(markerFile, []byte("keep this file"), 0600); err != nil {
		t.Fatal(err)
	}
	w := newWorkflow(t, s, localEngineBuilder(s))
	initial := w.Snapshot()
	if initial.CanStartLocal || !initial.CanBindRoot {
		t.Fatalf("unbound startup controls are unsafe: %+v", initial)
	}
	submitWorkflow(t, w, app.Command{Kind: app.BindRoot, ExpectedRevision: initial.Revision, Root: rootA})
	bound := waitWorkflow(t, w, func(s app.ViewState) bool { return s.Status == "Backup folder bound and startup checks passed." })
	current, err := s.CurrentConfig(context.Background())
	if err != nil || current.ID() != bound.Revision || current.Config().Backup.Root == nil || *current.Config().Backup.Root != rootA {
		t.Fatalf("root/config not committed together: revision=%+v state=%+v err=%v", current, bound, err)
	}
	binding, err := s.ActiveRoot(context.Background())
	if err != nil || binding.Path != rootA {
		t.Fatalf("active binding=%+v err=%v", binding, err)
	}

	submitWorkflow(t, w, app.Command{Kind: app.BackupLocal, Source: "relative/repository"})
	invalidSource := waitWorkflow(t, w, func(s app.ViewState) bool {
		return !s.Busy && strings.Contains(s.Error, "clean absolute")
	})
	runs, err := s.QueryRuns(context.Background(), 10)
	if err != nil || len(runs) != 0 || !invalidSource.CanStartLocal {
		t.Fatalf("invalid source was admitted or poisoned readiness: state=%+v runs=%+v err=%v", invalidSource, runs, err)
	}

	submitWorkflow(t, w, app.Command{Kind: app.BackupLocal, Source: rootA})
	overlap := waitWorkflow(t, w, func(s app.ViewState) bool { return !s.Busy && s.Status == "Local backup failed." })
	runs, err = s.QueryRuns(context.Background(), 10)
	if err != nil || len(runs) != 1 || runs[0].State != "failed" || runs[0].Intent.SourcePath != rootA {
		t.Fatalf("overlapping source outcome was not durable: state=%+v runs=%+v err=%v", overlap, runs, err)
	}
	if snapshots, listErr := s.ListSnapshots(context.Background(), "", 10); listErr != nil || len(snapshots) != 0 {
		t.Fatalf("overlapping source produced a snapshot: %+v %v", snapshots, listErr)
	}
	if contents, readErr := os.ReadFile(markerFile); readErr != nil || string(contents) != "keep this file" {
		t.Fatalf("overlap failure changed existing root data: %q %v", contents, readErr)
	}

	source := filepath.Join(base, "source")
	makeWorkflowRepository(t, source)
	submitWorkflow(t, w, app.Command{Kind: app.BackupLocal, Source: source})
	completed := waitWorkflow(t, w, func(s app.ViewState) bool {
		return !s.Busy && (strings.Contains(s.Status, "completed") || s.Error != "") && (s.CanStartLocal || s.Error != "")
	})
	if !strings.Contains(completed.Status, "completed") {
		t.Fatalf("local backup did not complete: %+v", completed)
	}
	snapshots, err := s.ListSnapshots(context.Background(), "", 100)
	if err != nil || len(snapshots) != 1 || snapshots[0].RootID != binding.ID || snapshots[0].State != "ready" {
		t.Fatalf("durable snapshot facts=%+v err=%v", snapshots, err)
	}
	snapshotPath := filepath.Join(rootA, filepath.FromSlash(snapshots[0].RelativePath))
	if _, err = os.Stat(snapshotPath); err != nil {
		t.Fatalf("published snapshot missing: %v", err)
	}
	if !strings.Contains(snapshots[0].Verification.OfflineRestore, "passed") || !completed.CanStartLocal {
		t.Fatalf("snapshot verification or controls not refreshed: snapshot=%+v state=%+v", snapshots[0], completed)
	}

	submitWorkflow(t, w, app.Command{Kind: app.SaveSettings, ExpectedRevision: completed.Revision, Settings: app.SettingsDraft{Concurrency: "3"}})
	settings := waitWorkflow(t, w, func(s app.ViewState) bool { return s.Settings.Concurrency == "3" && s.Revision > completed.Revision })
	current, err = s.CurrentConfig(context.Background())
	if err != nil || current.Config().Backup.Root == nil || *current.Config().Backup.Root != rootA || current.Config().Backup.RepositoryConcurrency != 3 {
		t.Fatalf("non-root settings edit changed managed binding: config=%+v err=%v", current, err)
	}

	submitWorkflow(t, w, app.Command{Kind: app.BindRoot, ExpectedRevision: settings.Revision, Root: rootB})
	rebound := waitWorkflow(t, w, func(s app.ViewState) bool {
		return s.Status == "Backup folder bound and startup checks passed." && s.Settings.Root == rootB
	})
	newBinding, err := s.ActiveRoot(context.Background())
	if err != nil || newBinding.Path != rootB || rebound.Revision <= settings.Revision {
		t.Fatalf("explicit rebind did not publish new root: binding=%+v state=%+v err=%v", newBinding, rebound, err)
	}
	if _, err = os.Stat(snapshotPath); err != nil {
		t.Fatalf("rebind moved or removed the previous snapshot: %v", err)
	}
	if _, err = os.Stat(markerFile); err != nil {
		t.Fatalf("rebind removed unrelated root data: %v", err)
	}
	if got := w.Snapshot(); got.CanStartLocal != true {
		t.Fatalf("new root did not pass startup readiness: %+v", got)
	}
}

type cancellableEngine struct {
	started    chan struct{}
	cancelSeen chan struct{}
	release    chan struct{}
	startOnce  sync.Once
	cancelOnce sync.Once
	releaseOne sync.Once
}

func (e *cancellableEngine) Create(ctx context.Context, _ backup.Request) (backup.Result, error) {
	e.startOnce.Do(func() { close(e.started) })
	<-ctx.Done()
	e.cancelOnce.Do(func() { close(e.cancelSeen) })
	<-e.release
	result, _ := backup.NewResult(backup.ResultFacts{Outcome: backup.OutcomeCancelled, Publication: backup.NotPublished})
	return result, ctx.Err()
}

func (*cancellableEngine) Inspect(context.Context, string) (backup.Manifest, error) {
	return backup.Manifest{}, errors.New("unexpected Inspect")
}

func (*cancellableEngine) Verify(context.Context, string) (backup.Verification, error) {
	return backup.Verification{}, errors.New("unexpected Verify")
}

func (e *cancellableEngine) unblock() { e.releaseOne.Do(func() { close(e.release) }) }

func TestLocalWorkflowCancelAndBusy(t *testing.T) {
	s, base := workflowStore(t)
	root := filepath.Join(base, "vault")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	engine := &cancellableEngine{started: make(chan struct{}), cancelSeen: make(chan struct{}), release: make(chan struct{})}
	w := newWorkflow(t, s, func(store.RootBinding, config.Revision, chan<- backup.ProgressEvent) (app.SnapshotEngine, error) {
		return engine, nil
	})
	t.Cleanup(engine.unblock)
	initial := w.Snapshot()
	submitWorkflow(t, w, app.Command{Kind: app.BindRoot, ExpectedRevision: initial.Revision, Root: root})
	bound := waitWorkflow(t, w, func(s app.ViewState) bool { return s.CanStartLocal })
	source := filepath.Join(base, "synthetic-source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	submitWorkflow(t, w, app.Command{Kind: app.BackupLocal, Source: source})
	select {
	case <-engine.started:
	case <-time.After(10 * time.Second):
		t.Fatal("engine did not start")
	}
	state := waitWorkflow(t, w, func(s app.ViewState) bool { return s.Busy && s.CanCancel })
	submitWorkflow(t, w, app.Command{Kind: app.BackupLocal, Source: source})
	waitWorkflow(t, w, func(s app.ViewState) bool { return s.Busy && strings.Contains(s.Error, "already active") })
	runs, err := s.QueryRuns(context.Background(), 10)
	if err != nil || len(runs) != 1 {
		t.Fatalf("busy second request was admitted: runs=%+v err=%v", runs, err)
	}
	submitWorkflow(t, w, app.Command{Kind: app.Cancel})
	select {
	case <-engine.cancelSeen:
	case <-time.After(10 * time.Second):
		t.Fatal("engine did not observe cancellation")
	}
	stillOwned := w.Snapshot()
	if !stillOwned.Busy || !stillOwned.CanCancel {
		t.Fatalf("ownership released before engine completion: before=%+v after=%+v", state, stillOwned)
	}
	engine.unblock()
	cancelled := waitWorkflow(t, w, func(s app.ViewState) bool {
		return !s.Busy && s.CanStartLocal && strings.Contains(s.Status, "cancelled")
	})
	if cancelled.CanStartLocal != true {
		t.Fatalf("cancelled run did not retain a ready coordinator: %+v", cancelled)
	}
	runs, err = s.QueryRuns(context.Background(), 10)
	if err != nil || len(runs) != 1 || runs[0].State != "cancelled" || runs[0].Config.ID() != bound.Revision {
		t.Fatalf("durable cancellation/config facts=%+v err=%v", runs, err)
	}
	if got := fmt.Sprint(w.Snapshot().Tasks); !strings.Contains(got, "cancelled") {
		t.Fatalf("terminal task result not refreshed: %s", got)
	}
}
