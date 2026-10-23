//go:build linux

package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gitvault/internal/backup"
	"gitvault/internal/store"
)

const testInstallationID = "gitvault-test-installation"

type appFixture struct {
	dir, root, dbPath string
	store             *store.Store
	binding           store.RootBinding
}

func newAppFixture(t *testing.T) *appFixture {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dir, "vault")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(dir, "app-data")
	if err := os.Mkdir(data, 0700); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(data, "gitvault.sqlite3")
	s, err := store.OpenWithInstallation(context.Background(), dbPath, testInstallationID)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := BindManagedRoot(context.Background(), s, root)
	if err != nil {
		s.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return &appFixture{dir: dir, root: root, dbPath: dbPath, store: s, binding: binding}
}

func (f *appFixture) engine(t *testing.T, s *store.Store) *backup.Engine {
	t.Helper()
	scratch := filepath.Join(t.TempDir(), "git-scratch")
	if err := os.Mkdir(scratch, 0700); err != nil {
		t.Fatal(err)
	}
	runner, err := backup.NewGitRunner(scratch, backup.DefaultGitRunnerOptions())
	if err != nil {
		t.Fatal(err)
	}
	identity := s.Identity()
	e, err := backup.NewEngine(backup.EngineOptions{
		Root:            f.root,
		Runner:          runner,
		InstallationID:  identity.InstallationID,
		StoreID:         identity.StoreID,
		ApplicationData: identity.DataDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func (f *appFixture) coordinator(t *testing.T, engine SnapshotEngine) *Coordinator {
	t.Helper()
	c, err := NewCoordinator(context.Background(), f.store, f.binding, engine)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.ReconcileStartup(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = c.Close(ctx)
	})
	return c
}

type gatedSnapshotEngine struct {
	started       chan backup.Request
	release       chan struct{}
	cancelSeen    chan struct{}
	waitForCancel bool
	result        backup.Result
	err           error
	releaseOnce   sync.Once
}

func (e *gatedSnapshotEngine) Create(ctx context.Context, request backup.Request) (backup.Result, error) {
	e.started <- request
	if e.waitForCancel {
		<-ctx.Done()
		close(e.cancelSeen)
	}
	<-e.release
	return e.result, e.err
}

func (*gatedSnapshotEngine) Inspect(context.Context, string) (backup.Manifest, error) {
	return backup.Manifest{}, errors.New("unexpected Inspect call")
}

func (*gatedSnapshotEngine) Verify(context.Context, string) (backup.Verification, error) {
	return backup.Verification{}, errors.New("unexpected Verify call")
}

func (e *gatedSnapshotEngine) unblock() { e.releaseOnce.Do(func() { close(e.release) }) }

func resultFor(t *testing.T, outcome backup.OperationOutcome) backup.Result {
	t.Helper()
	r, err := backup.NewResult(backup.ResultFacts{Outcome: outcome, Publication: backup.NotPublished})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func newTestRepository(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "source")
	gitTestCommand(t, "init", "--initial-branch=main", path)
	if err := os.WriteFile(filepath.Join(path, "history.txt"), []byte("synthetic history\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTestCommand(t, "-C", path, "add", "history.txt")
	gitTestCommand(t, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-C", path, "commit", "-m", "initial")
	return path
}

func gitTestCommand(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git command failed: %v: %s", err, out)
	}
	return strings.TrimSpace(string(out))
}

func mutateSnapshotStateFixture(t *testing.T, databasePath, snapshotID, state string) {
	t.Helper()
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(databasePath)}
	query := url.Values{}
	query.Set("mode", "rw")
	db, err := sql.Open("sqlite", u.String()+"?"+query.Encode())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`UPDATE snapshots SET state=? WHERE id=?`, state, snapshotID); err != nil {
		t.Fatal(err)
	}
}

func waitForRun(t *testing.T, c *Coordinator, id string) store.Run {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	r, err := c.Wait(ctx, id)
	if err != nil {
		t.Fatalf("wait for run: %v", err)
	}
	return r
}

func TestAdmissionBeforeWork(t *testing.T) {
	f := newAppFixture(t)
	source := newTestRepository(t)
	fake := &gatedSnapshotEngine{started: make(chan backup.Request, 1), release: make(chan struct{}), result: resultFor(t, backup.OutcomeFailed), err: errors.New("synthetic engine failure")}
	c, err := NewCoordinator(context.Background(), f.store, f.binding, fake)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = c.Close(ctx)
	})
	t.Cleanup(fake.unblock)
	if _, err = c.StartLocal(context.Background(), LocalBackupRequest{SourcePath: source}); !errors.Is(err, ErrStartupReconciliationRequired) {
		t.Fatalf("admitted managed work before startup reconciliation: %v", err)
	}
	if _, err = c.ReconcileStartup(context.Background()); err != nil {
		t.Fatal(err)
	}
	repositoryID, err := backup.NewID()
	if err != nil {
		t.Fatal(err)
	}
	run, err := c.StartLocal(context.Background(), LocalBackupRequest{SourcePath: source, RepositoryID: repositoryID})
	if err != nil {
		t.Fatal(err)
	}
	request := <-fake.started
	if run.State != "running" || request.Repository.ID != repositoryID || request.SnapshotID == "" {
		t.Fatalf("work started without expected admitted identity: run=%+v request=%+v", run, request)
	}
	storedRun, err := f.store.GetRun(context.Background(), run.ID)
	if err != nil || storedRun.State != "running" || storedRun.Intent.RepositoryIDs[0] != repositoryID || storedRun.Intent.SourcePath != source {
		t.Fatalf("durable intent not committed before work: run=%+v err=%v", storedRun, err)
	}
	tasks, err := f.store.ListTasks(context.Background(), run.ID, 10)
	if err != nil || len(tasks) != 1 || tasks[0].Outcome != "pending" || tasks[0].OperationID != request.SnapshotID {
		t.Fatalf("durable task identity not committed before work: tasks=%+v err=%v", tasks, err)
	}
	if release, lockErr := lockCoordinatorRun(context.Background(), f.root); lockErr == nil {
		release()
		t.Fatal("second owner acquired the active run lock")
	} else {
		var busy *store.BusyError
		if !errors.As(lockErr, &busy) {
			t.Fatalf("active owner lock error=%v, want BusyError", lockErr)
		}
	}
	assertChildProcessLockBusy(t, f.root)
	second, err := NewCoordinator(context.Background(), f.store, f.binding, fake)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = second.ReconcileStartup(context.Background()); err == nil {
		t.Fatal("second coordinator reconciled while another owner was active")
	} else {
		var busy *store.BusyError
		if !errors.As(err, &busy) {
			t.Fatalf("second coordinator error=%v, want BusyError", err)
		}
	}

	fake.unblock()
	if terminal := waitForRun(t, c, run.ID); terminal.State != "failed" {
		t.Fatalf("run terminal state=%q, want failed", terminal.State)
	}
	protectedSnapshot := mustAppID(t)
	if err = f.store.IndexSnapshot(context.Background(), store.Snapshot{ID: protectedSnapshot, RepositoryID: repositoryID, Provider: "local", RootID: f.binding.ID, FormatVersion: 1, RelativePath: filepath.ToSlash(filepath.Join("snapshots", "local", repositoryID, protectedSnapshot)), State: "ready", PublicationState: "published", SHA256: strings.Repeat("a", 64), SizeBytes: 1, CreatedAt: time.Now().UTC(), Verification: store.Verification{Manifest: "passed", Hash: "passed", IndependentRefs: "passed", Head: "passed", ObjectIntegrity: "passed", OfflineRestore: "passed"}}); err != nil {
		t.Fatal(err)
	}

	for _, installationID := range []string{testInstallationID, testInstallationID + "-other"} {
		otherDir := t.TempDir()
		if err = os.Chmod(otherDir, 0700); err != nil {
			t.Fatal(err)
		}
		otherData := filepath.Join(otherDir, "other-app-data")
		if err = os.Mkdir(otherData, 0700); err != nil {
			t.Fatal(err)
		}
		foreign, openErr := store.OpenWithInstallation(context.Background(), filepath.Join(otherData, "gitvault.sqlite3"), installationID)
		if openErr != nil {
			t.Fatal(openErr)
		}
		if _, err = BindManagedRoot(context.Background(), foreign, f.root); !errors.Is(err, store.ErrForeignRoot) {
			foreign.Close()
			t.Fatalf("foreign identity %q bound the canonical root: %v", installationID, err)
		}
		if _, err = NewCoordinator(context.Background(), foreign, f.binding, fake); !errors.Is(err, store.ErrForeignRoot) {
			foreign.Close()
			t.Fatalf("foreign identity %q constructed an owner for the managed root: %v", installationID, err)
		}
		if _, err = foreign.BeginRun(context.Background(), store.RunIntent{ID: mustAppID(t), Trigger: "manual", Kind: "local_backup", RootID: f.binding.ID, RepositoryIDs: []string{repositoryID}, SourcePath: source}); !errors.Is(err, store.ErrForeignRoot) {
			foreign.Close()
			t.Fatalf("foreign store admitted a run against another database's root binding: %v", err)
		}
		if err = foreign.IndexSnapshot(context.Background(), store.Snapshot{ID: mustAppID(t), RepositoryID: repositoryID, Provider: "local", RootID: f.binding.ID, FormatVersion: 1, RelativePath: filepath.ToSlash(filepath.Join("snapshots", "local", repositoryID, mustAppID(t))), State: "ready", PublicationState: "published", SHA256: strings.Repeat("a", 64), SizeBytes: 1, CreatedAt: time.Now().UTC(), Verification: store.Verification{Manifest: "passed", Hash: "passed", IndependentRefs: "passed", Head: "passed", ObjectIntegrity: "passed", OfflineRestore: "passed"}}); !errors.Is(err, store.ErrForeignRoot) {
			foreign.Close()
			t.Fatalf("foreign store indexed a snapshot against another database's root: %v", err)
		}
		if err = foreign.SetPin(context.Background(), protectedSnapshot, true); !errors.Is(err, store.ErrNotFound) {
			foreign.Close()
			t.Fatalf("foreign store accessed a pin target owned by another database: %v", err)
		}
		if err = foreign.AcquireLease(context.Background(), store.Lease{ID: mustAppID(t), SnapshotID: protectedSnapshot, OwnerID: mustAppID(t), OperationID: mustAppID(t), AcquiredAt: time.Now().UTC()}); !errors.Is(err, store.ErrNotFound) {
			foreign.Close()
			t.Fatalf("foreign store accessed a lease target owned by another database: %v", err)
		}
		pins, pinErr := foreign.ListPins(context.Background(), 10)
		leases, leaseErr := foreign.ListLeases(context.Background(), 10)
		if pinErr != nil || leaseErr != nil || len(pins) != 0 || len(leases) != 0 {
			foreign.Close()
			t.Fatalf("foreign store acquired independent protection facts: pins=%+v leases=%+v errors=%v/%v", pins, leases, pinErr, leaseErr)
		}
		if err = foreign.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCancelRetainsOwnership(t *testing.T) {
	f := newAppFixture(t)
	fake := &gatedSnapshotEngine{started: make(chan backup.Request, 1), release: make(chan struct{}), cancelSeen: make(chan struct{}), waitForCancel: true, result: resultFor(t, backup.OutcomeCancelled), err: context.Canceled}
	c := f.coordinator(t, fake)
	t.Cleanup(fake.unblock)
	run, err := c.StartLocal(context.Background(), LocalBackupRequest{SourcePath: newTestRepository(t)})
	if err != nil {
		t.Fatal(err)
	}
	<-fake.started
	if err = c.Cancel(context.Background(), run.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-fake.cancelSeen:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not observe cancellation")
	}
	stored, err := f.store.GetRun(context.Background(), run.ID)
	if err != nil || stored.State != "cancelling" {
		t.Fatalf("cancel intent not durably recorded: %+v %v", stored, err)
	}
	if release, lockErr := lockCoordinatorRun(context.Background(), f.root); lockErr == nil {
		release()
		t.Fatal("ownership released before worker returned/reaped")
	} else {
		var busy *store.BusyError
		if !errors.As(lockErr, &busy) {
			t.Fatalf("lock while worker is reaping: %v", lockErr)
		}
	}
	assertChildProcessLockBusy(t, f.root)
	fake.unblock()
	if terminal := waitForRun(t, c, run.ID); terminal.State != "cancelled" {
		t.Fatalf("terminal state=%q, want cancelled", terminal.State)
	}
	if release, lockErr := lockCoordinatorRun(context.Background(), f.root); lockErr != nil {
		t.Fatalf("ownership not released after durable terminal result: %v", lockErr)
	} else {
		release()
	}
	tasks, err := f.store.ListTasks(context.Background(), run.ID, 10)
	if err != nil || len(tasks) != 1 || tasks[0].Outcome != "cancelled" || tasks[0].PublicationState != string(backup.NotPublished) {
		t.Fatalf("cancelled task facts=%+v err=%v", tasks, err)
	}
}

func TestRestartReconciliation(t *testing.T) {
	f := newAppFixture(t)
	ctx := context.Background()
	identity := f.store.Identity()
	engine := f.engine(t, f.store)
	repoReady := mustAppID(t)
	repoAmbiguous := mustAppID(t)
	repoMissing := mustAppID(t)
	repoIndexed := mustAppID(t)
	snapshotReady := mustAppID(t)
	snapshotAmbiguous := mustAppID(t)
	snapshotMissing := mustAppID(t)
	snapshotIndexed := mustAppID(t)
	runID := mustAppID(t)
	run, err := f.store.BeginRun(ctx, store.RunIntent{ID: runID, Trigger: "manual", Kind: "local_backup", RootID: f.binding.ID, RepositoryIDs: []string{repoReady, repoAmbiguous, repoMissing}, SourcePath: newTestRepository(t)})
	if err != nil {
		t.Fatal(err)
	}
	tasks := []store.Task{
		{ID: mustAppID(t), RunID: run.ID, RepositoryID: repoReady, Provider: "local", OperationID: snapshotReady},
		{ID: mustAppID(t), RunID: run.ID, RepositoryID: repoAmbiguous, Provider: "local", OperationID: snapshotAmbiguous},
		{ID: mustAppID(t), RunID: run.ID, RepositoryID: repoMissing, Provider: "local", OperationID: snapshotMissing},
	}
	for _, task := range tasks {
		if err = f.store.CreateTask(ctx, task); err != nil {
			t.Fatal(err)
		}
	}
	if err = f.store.SetRunState(ctx, run.ID, "running", ""); err != nil {
		t.Fatal(err)
	}
	created, err := engine.Create(ctx, backup.Request{Source: newTestRepository(t), Repository: backup.RepositoryIdentity{Provider: "local", ID: repoReady, DisplayName: "recoverable"}, ApplicationVersion: "test", SnapshotID: snapshotReady})
	if err != nil || created.Artifact().SnapshotID != snapshotReady {
		t.Fatalf("pre-crash exact artifact creation: %+v %v", created, err)
	}
	indexed, err := engine.Create(ctx, backup.Request{Source: newTestRepository(t), Repository: backup.RepositoryIdentity{Provider: "local", ID: repoIndexed, DisplayName: "indexed"}, ApplicationVersion: "test", SnapshotID: snapshotIndexed})
	if err != nil {
		t.Fatalf("indexed artifact fixture: %v", err)
	}
	indexedManifest, err := engine.Inspect(ctx, indexed.Artifact().Path)
	if err != nil {
		t.Fatal(err)
	}
	indexedVerification, err := engine.Verify(ctx, indexed.Artifact().Path)
	if err != nil {
		t.Fatal(err)
	}
	indexedRelative := mustRelative(f.root, indexed.Artifact().Path)
	if err = f.store.IndexSnapshot(ctx, snapshotRecord(f.binding.ID, indexedRelative, indexedManifest, string(backup.Published), indexedVerification)); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(indexed.Artifact().Path, backup.BundleFilename), []byte("corrupted bundle"), 0600); err != nil {
		t.Fatal(err)
	}
	ambiguousPath := filepath.Join(f.root, "snapshots", "local", repoAmbiguous, snapshotAmbiguous)
	if err = os.MkdirAll(filepath.Dir(ambiguousPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(ambiguousPath, []byte("not a published snapshot directory"), 0600); err != nil {
		t.Fatal(err)
	}
	unknownPath := filepath.Join(f.root, "snapshots", "local", mustAppID(t), "orphan")
	if err = os.MkdirAll(unknownPath, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(unknownPath, "keep.txt"), []byte("inspection only"), 0600); err != nil {
		t.Fatal(err)
	}
	stagingPath := filepath.Join(f.root, "staging", "unowned.partial")
	if err = os.WriteFile(stagingPath, []byte("do not delete"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = f.store.Close(); err != nil {
		t.Fatal(err)
	}
	f.store, err = store.OpenWithInstallation(ctx, f.dbPath, testInstallationID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.store.Close() })
	if f.store.Identity() != identity {
		t.Fatalf("store identity changed after restart: old=%+v new=%+v", identity, f.store.Identity())
	}
	f.binding, err = f.store.RootBinding(ctx, f.binding.ID)
	if err != nil {
		t.Fatal(err)
	}
	engine = f.engine(t, f.store)
	c, err := NewCoordinator(ctx, f.store, f.binding, engine)
	if err != nil {
		t.Fatal(err)
	}
	report, err := c.ReconcileStartup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = c.Close(closeCtx)
	})
	if len(report.Interrupted) != 1 || report.Interrupted[0].ID != run.ID || report.Interrupted[0].State != "interrupted" {
		t.Fatalf("interrupted run reconciliation=%+v", report.Interrupted)
	}
	ready, err := f.store.GetSnapshot(ctx, snapshotReady)
	if err != nil || ready.State != "ready" || ready.RelativePath != filepath.ToSlash(filepath.Join("snapshots", "local", repoReady, snapshotReady)) {
		t.Fatalf("exact crash artifact not indexed: %+v %v", ready, err)
	}
	gotTasks, err := f.store.ListTasks(ctx, run.ID, 10)
	if err != nil || len(gotTasks) != 3 {
		t.Fatalf("reconciled tasks=%+v err=%v", gotTasks, err)
	}
	byID := make(map[string]store.Task, len(gotTasks))
	for _, task := range gotTasks {
		byID[task.RepositoryID] = task
	}
	if byID[repoReady].Outcome != "succeeded" || byID[repoReady].SnapshotID != snapshotReady {
		t.Fatalf("reserved snapshot identity was not recovered exactly: %+v", byID[repoReady])
	}
	if byID[repoAmbiguous].Outcome != "interrupted" || byID[repoAmbiguous].PublicationState != string(backup.PublicationUnknown) {
		t.Fatalf("ambiguous output not retained for inspection: %+v", byID[repoAmbiguous])
	}
	if byID[repoMissing].Outcome != "interrupted" {
		t.Fatalf("missing reserved artifact did not remain interrupted: %+v", byID[repoMissing])
	}
	corruptSaved, err := f.store.GetSnapshot(ctx, snapshotIndexed)
	if err != nil || corruptSaved.State != "corrupt" {
		t.Fatalf("corrupt indexed artifact was not downgraded: %+v %v", corruptSaved, err)
	}
	unknownRelative := filepath.ToSlash(mustRelative(f.root, unknownPath))
	if !containsString(report.InspectionRequired, unknownRelative) || !containsString(report.UnownedStaging, "staging/unowned.partial") || !containsString(report.MissingSnapshots, filepath.ToSlash(mustRelative(f.root, filepath.Join(f.root, "snapshots", "local", repoMissing, snapshotMissing)))) || !containsString(report.CorruptSnapshots, filepath.ToSlash(indexedRelative)) || len(report.Conflicts) != 1 {
		t.Fatalf("reconciliation report omitted inspection facts: %+v", report)
	}
	for _, path := range []string{unknownPath, stagingPath, ambiguousPath} {
		if _, err = os.Lstat(path); err != nil {
			t.Fatalf("reconciliation deleted unowned/ambiguous data %q: %v", path, err)
		}
	}
	if err = os.Remove(filepath.Join(f.root, managedRootMarkerName)); err != nil {
		t.Fatal(err)
	}
	if _, err = BindManagedRoot(ctx, f.store, f.root); !errors.Is(err, store.ErrForeignRoot) {
		t.Fatalf("lost marker was silently adopted over managed data: %v", err)
	}
}

func TestTerminalPersistenceWithoutUI(t *testing.T) {
	f := newAppFixture(t)
	engine := f.engine(t, f.store)
	c := f.coordinator(t, engine)
	repositoryID, err := backup.NewID()
	if err != nil {
		t.Fatal(err)
	}
	run, err := c.StartLocal(context.Background(), LocalBackupRequest{SourcePath: newTestRepository(t), RepositoryID: repositoryID})
	if err != nil {
		t.Fatal(err)
	}
	terminal := waitForRun(t, c, run.ID)
	if terminal.State != "succeeded" {
		t.Fatalf("run result=%+v", terminal)
	}
	tasks, err := f.store.ListTasks(context.Background(), run.ID, 10)
	if err != nil || len(tasks) != 1 || tasks[0].Outcome != "succeeded" || tasks[0].SnapshotID != tasks[0].OperationID || tasks[0].PublicationState != string(backup.Published) {
		t.Fatalf("terminal task facts were not durable without a UI consumer: %+v %v", tasks, err)
	}
	snapshots, err := f.store.ListSnapshots(context.Background(), repositoryID, 10)
	if err != nil || len(snapshots) != 1 || snapshots[0].ID != tasks[0].OperationID || snapshots[0].State != "ready" {
		t.Fatalf("terminal snapshot facts were not durable: %+v %v", snapshots, err)
	}
}

func TestReconciliationReportsUnresolvedDeletionStates(t *testing.T) {
	f := newAppFixture(t)
	ctx := context.Background()
	engine := f.engine(t, f.store)
	states := []string{"deleting", "deleted"}
	paths := make(map[string]string, len(states))
	ids := make(map[string]string, len(states))
	for _, state := range states {
		repositoryID := mustAppID(t)
		snapshotID := mustAppID(t)
		result, err := engine.Create(ctx, backup.Request{Source: newTestRepository(t), Repository: backup.RepositoryIdentity{Provider: "local", ID: repositoryID, DisplayName: "deletion fixture"}, ApplicationVersion: "test", SnapshotID: snapshotID})
		if err != nil {
			t.Fatal(err)
		}
		artifact := result.Artifact()
		manifest, err := engine.Inspect(ctx, artifact.Path)
		if err != nil {
			t.Fatal(err)
		}
		verification, err := engine.Verify(ctx, artifact.Path)
		if err != nil {
			t.Fatal(err)
		}
		relative := mustRelative(f.root, artifact.Path)
		if err = f.store.IndexSnapshot(ctx, snapshotRecord(f.binding.ID, relative, manifest, string(backup.Published), verification)); err != nil {
			t.Fatal(err)
		}
		mutateSnapshotStateFixture(t, f.dbPath, snapshotID, state)
		paths[snapshotID] = artifact.Path
		ids[state] = snapshotID
	}
	c, err := NewCoordinator(ctx, f.store, f.binding, engine)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = c.Close(closeCtx)
	})
	if report, err := c.ReconcileStartup(ctx); !errors.Is(err, ErrDeletionReconciliationRequired) {
		t.Fatalf("unresolved deletion state error=%v report=%+v", err, report)
	} else if len(report.DeletionIssues) != 2 {
		t.Fatalf("deletion states were not reported: %+v", report.DeletionIssues)
	} else {
		byState := make(map[string]DeletionIssue, len(report.DeletionIssues))
		for _, issue := range report.DeletionIssues {
			byState[issue.SnapshotState] = issue
		}
		for _, state := range states {
			issue, ok := byState[state]
			if !ok || issue.SnapshotID != ids[state] || issue.Detail == "" {
				t.Fatalf("missing deletion-state report for %s: %+v", state, report.DeletionIssues)
			}
		}
	}
	for snapshotID, path := range paths {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("unresolved deletion reconciliation removed snapshot %s: %v", snapshotID, err)
		}
	}
	if _, err := c.StartLocal(ctx, LocalBackupRequest{SourcePath: newTestRepository(t)}); !errors.Is(err, ErrStartupReconciliationRequired) {
		t.Fatalf("managed admission bypassed unresolved deletion report: %v", err)
	}
}

func TestReconciliationDirectoryScanIsBounded(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "snapshots")
	if err := os.Mkdir(base, 0700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if err := os.Mkdir(filepath.Join(base, fmt.Sprintf("provider-%d", i)), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := scanSnapshotLeaves(root, 3); err == nil {
		t.Fatal("snapshot scan accepted more directory entries than its total budget")
	}

	remaining := 2
	if _, err := readManagedDirectory(base, &remaining); err == nil {
		t.Fatal("incremental directory reader accepted an over-budget directory")
	}
}

func mustAppID(t *testing.T) string {
	t.Helper()
	id, err := backup.NewID()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// TestCoordinatorRunLockChild is launched by the parent tests in a separate
// process to exercise flock ownership beyond Go's in-process mutexes.
func TestCoordinatorRunLockChild(t *testing.T) {
	root := os.Getenv("GITVAULT_LOCK_PROBE_ROOT")
	if root == "" {
		return
	}
	release, err := lockCoordinatorRun(context.Background(), root)
	if err == nil {
		release()
		t.Fatal("child process acquired a lock held by another process")
	}
	var busy *store.BusyError
	if !errors.As(err, &busy) {
		t.Fatalf("child lock error=%v, want BusyError", err)
	}
}

func assertChildProcessLockBusy(t *testing.T, root string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestCoordinatorRunLockChild$")
	cmd.Env = append(os.Environ(), "GITVAULT_LOCK_PROBE_ROOT="+root)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("second process did not observe managed-run ownership: %v: %s", err, output)
	}
}
