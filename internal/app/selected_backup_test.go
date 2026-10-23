//go:build linux

package app

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gitvault/internal/backup"
	"gitvault/internal/config"
	"gitvault/internal/github"
	"gitvault/internal/secrets"
	"gitvault/internal/store"
)

const selectedCredential = "synthetic-GV-selected-pipeline-token"

// TestGitHubCredentialHelperFixture is the private-helper entry point for real
// Git subprocesses launched by this test binary. It is inert in normal test runs.
func TestGitHubCredentialHelperFixture(t *testing.T) {
	index := -1
	for i, arg := range os.Args {
		if arg == "--credential-helper-fixture" {
			index = i
			break
		}
	}
	if index < 0 {
		return
	}
	args := os.Args[index+1:]
	if len(args) > 0 && args[0] == "--no-gate" {
		args = args[1:]
	}
	handled, code := backup.HelperMain(context.Background(), args, os.Stdin, os.Stdout, os.Stderr)
	if !handled {
		os.Exit(93)
	}
	os.Exit(code)
}

func TestSelectedGitHubSnapshotPipeline(t *testing.T) {
	f := newAppFixture(t)
	remote := createSyntheticGitRepository(t, f.dir, "https-source", "offline recovery proof")
	server := newSyntheticSmartGitServer(t, remote, selectedCredential, false)
	defer server.Close()
	scratch := filepath.Join(f.dir, "git-scratch")
	runner := newSyntheticHTTPSRunner(t, server, scratch)
	engine := selectedTestEngine(t, f, runner)
	metadata := discoveredRepo(201, 101, "alice", "User", "demo")
	metadata.CloneURL = "https://github.com/alice/demo.git"
	discovery := &discoveryScript{results: []github.Result{completeDiscovery(metadata)}}
	_, workflow, _, revision := configureSelectedWorkflow(t, f, []github.Repository{metadata}, []string{"github:201"}, engine, discovery, nil)
	if err := workflow.Submit(Command{Kind: BackupSelected, ExpectedRevision: revision}); err != nil {
		t.Fatal(err)
	}
	state := waitDiscoveryState(t, workflow, func(state ViewState) bool {
		return !state.Busy && len(state.Tasks) > 0 && state.Tasks[0].Stage != "pending"
	})
	run, err := f.store.GetRun(context.Background(), workflow.lastRunID)
	if err != nil || run.State != "succeeded" || run.Intent.AccountID != "101" || len(run.Intent.RepositoryIDs) != 1 || run.Intent.RepositoryIDs[0] != "github:201" {
		t.Fatalf("captured run intent/result=%+v err=%v tasks=%+v", run, err, state.Tasks)
	}
	tasks, err := f.store.ListTasks(context.Background(), run.ID, 10)
	if err != nil || len(tasks) != 1 || tasks[0].Outcome != "succeeded" || tasks[0].Provider != "github" || tasks[0].ArtifactID != tasks[0].OperationID {
		t.Fatalf("durable GitHub task=%+v err=%v", tasks, err)
	}
	snapshots, err := f.store.ListSnapshots(context.Background(), "201", 10)
	if err != nil || len(snapshots) != 1 || snapshots[0].Provider != "github" || snapshots[0].State != "ready" {
		t.Fatalf("indexed snapshot=%+v err=%v", snapshots, err)
	}
	if !server.hasCredential() || server.authorizedRequests() < 2 {
		t.Fatal("real Git did not authenticate and complete the synthetic HTTPS smart-Git exchange")
	}
	assertNoCredentialInManagedFiles(t, f.root, []byte(selectedCredential))

	// The source and HTTP service disappear before restore. Only the independently
	// verified full bundle may supply the restored refs and HEAD.
	server.Close()
	if err := os.RemoveAll(remote); err != nil {
		t.Fatal("synthetic source cleanup failed")
	}
	destinationParent := filepath.Join(f.dir, "offline-restore")
	if err := os.Mkdir(destinationParent, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(destinationParent, 0700); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(destinationParent, "restored.git")
	snapshotPath := filepath.Join(f.root, filepath.FromSlash(snapshots[0].RelativePath))
	result, err := engine.Restore(context.Background(), snapshotPath, destination)
	if err != nil || result.Outcome() != backup.OutcomeSucceeded {
		t.Fatalf("offline exact-ref restore result=%+v err=%v", result, err)
	}
	manifest, err := engine.Inspect(context.Background(), snapshotPath)
	if err != nil {
		t.Fatal(err)
	}
	refsOut := runTestGit(t, destination, "for-each-ref", "--format=%(objectname) %(refname)")
	actualRefs := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(refsOut)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, " ", 2)
		if len(parts) != 2 {
			t.Fatalf("invalid restored ref output %q", line)
		}
		actualRefs[parts[1]] = parts[0]
	}
	if !sameStringMap(actualRefs, manifest.Refs) {
		t.Fatalf("restored refs=%v manifest refs=%v", actualRefs, manifest.Refs)
	}
	head, err := exec.Command("git", "-C", destination, "symbolic-ref", "-q", "HEAD").Output()
	if err != nil || manifest.Head.Kind != "symbolic" || strings.TrimSpace(string(head)) != manifest.Head.Target {
		t.Fatalf("restored HEAD=%q manifest=%+v err=%v", head, manifest.Head, err)
	}
}

func TestPipelinePartialResults(t *testing.T) {
	f := newAppFixture(t)
	firstSource := createSyntheticGitRepository(t, f.dir, "source-one", "first repository")
	secondSource := createSyntheticGitRepository(t, f.dir, "source-two", "second repository")
	engine := selectedTestEngine(t, f, nil)
	acquirer := &scriptedGitHubEngine{Engine: engine, mirrorRoot: filepath.Join(f.root, "mirrors", "github"), sources: map[string]string{"201": firstSource, "202": secondSource}, failures: map[string]error{"202": errors.New("synthetic transfer failure")}}
	repositories := []github.Repository{discoveredRepo(201, 101, "alice", "User", "one"), discoveredRepo(202, 101, "alice", "User", "two")}
	discovery := &discoveryScript{results: []github.Result{completeDiscovery(repositories...), completeDiscovery(repositories...)}}
	_, workflow, _, revision := configureSelectedWorkflow(t, f, repositories, []string{"github:201", "github:202"}, acquirer, discovery, nil)
	if err := workflow.Submit(Command{Kind: BackupSelected, ExpectedRevision: revision}); err != nil {
		t.Fatal(err)
	}
	state := waitDiscoveryState(t, workflow, func(state ViewState) bool {
		if state.Busy || !strings.Contains(state.Status, "partial results") || len(state.Tasks) != 2 {
			return false
		}
		for _, task := range state.Tasks {
			if task.Stage == "pending" {
				return false
			}
		}
		return true
	})
	partial, err := f.store.GetRun(context.Background(), workflow.lastRunID)
	if err != nil || partial.State != "partial" {
		t.Fatalf("run did not retain independent partial outcomes: run=%+v err=%v", partial, err)
	}
	tasks, err := f.store.ListTasks(context.Background(), partial.ID, 10)
	if err != nil || len(tasks) != 2 {
		t.Fatalf("partial tasks=%+v err=%v", tasks, err)
	}
	var retryTask store.Task
	for _, task := range tasks {
		if task.RepositoryID == "202" {
			retryTask = task
		} else if task.Outcome != "succeeded" {
			t.Fatalf("unrelated repository success was lost: %+v", task)
		}
	}
	canRetry := false
	for _, view := range state.Tasks {
		if view.RepositoryID == "github:202" && view.CanRetry {
			canRetry = true
		}
	}
	if !canRetry || retryTask.Outcome != "failed" || retryTask.PublicationState != string(backup.NotPublished) {
		t.Fatalf("retry eligibility did not match the durable no-artifact failure: state=%+v task=%+v", state, retryTask)
	}
	acquirer.mu.Lock()
	delete(acquirer.failures, "202")
	acquirer.mu.Unlock()
	if err = workflow.Submit(Command{Kind: RetryGitHubTask, ExpectedRevision: state.Revision, RepositoryID: "github:202", RunID: partial.ID}); err != nil {
		t.Fatal(err)
	}
	retryState := waitDiscoveryState(t, workflow, func(state ViewState) bool {
		return !state.Busy && len(state.Tasks) == 1 && state.Tasks[0].RunID != partial.ID && state.Tasks[0].Stage != "pending"
	})
	retried, err := f.store.GetRun(context.Background(), workflow.lastRunID)
	if err != nil || retried.State != "succeeded" || retried.Trigger != "retry" || len(retried.Intent.RepositoryIDs) != 1 || retried.Intent.RepositoryIDs[0] != "github:202" {
		retryTasks, tasksErr := f.store.ListTasks(context.Background(), workflow.lastRunID, 10)
		t.Fatalf("eligible retry was not a scoped durable run: state=%s trigger=%s intent=%+v err=%v tasks=%+v tasksErr=%v view=%+v status=%q", retried.State, retried.Trigger, retried.Intent, err, retryTasks, tasksErr, retryState.Tasks, retryState.Status)
	}
	snapshots, err := f.store.ListSnapshots(context.Background(), "", 10)
	if err != nil || len(snapshots) != 2 {
		t.Fatalf("independent/retried snapshots=%+v err=%v", snapshots, err)
	}
}

func TestPipelineCancellationAndRestart(t *testing.T) {
	f := newAppFixture(t)
	remote := createSyntheticGitRepository(t, f.dir, "cancel-source", "cancel while fetching")
	server := newSyntheticSmartGitServer(t, remote, selectedCredential, true)
	defer server.Close()
	runner := newSyntheticHTTPSRunner(t, server, filepath.Join(f.dir, "cancel-scratch"))
	engine := selectedTestEngine(t, f, runner)
	metadata := discoveredRepo(201, 101, "alice", "User", "demo")
	metadata.CloneURL = "https://github.com/alice/demo.git"
	discovery := &discoveryScript{results: []github.Result{completeDiscovery(metadata)}}
	_, workflow, credentials, revision := configureSelectedWorkflow(t, f, []github.Repository{metadata}, []string{"github:201"}, engine, discovery, nil)
	if err := workflow.Submit(Command{Kind: BackupSelected, ExpectedRevision: revision}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-server.requestStarted:
	case <-time.After(20 * time.Second):
		t.Fatal("Git did not reach the authenticated synthetic HTTPS server")
	}
	runs, err := f.store.QueryRuns(context.Background(), 10)
	if err != nil || len(runs) != 1 || runs[0].State != "running" || runs[0].Intent.AccountID != "101" {
		t.Fatalf("run intent was not durable before transfer: runs=%+v err=%v", runs, err)
	}
	reserved, err := f.store.ListTasks(context.Background(), runs[0].ID, 10)
	if err != nil || len(reserved) != 1 || reserved[0].OperationID == "" {
		t.Fatalf("snapshot identity was not reserved before transfer: tasks=%+v err=%v", reserved, err)
	}
	if err = workflow.Submit(Command{Kind: Cancel}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-server.requestCancelled:
	case <-time.After(10 * time.Second):
		t.Fatal("cancellation did not terminate the owned HTTPS Git process")
	}
	if _, waitErr := workflow.coordinator.Wait(context.Background(), runs[0].ID); waitErr != nil {
		t.Fatalf("coordinator could not reconcile cancellation: %v", waitErr)
	}
	waitDiscoveryState(t, workflow, func(state ViewState) bool { return !state.Busy && strings.Contains(state.Status, "cancelled") })
	terminal, err := f.store.GetRun(context.Background(), runs[0].ID)
	if err != nil || terminal.State != "cancelled" {
		t.Fatalf("cancelled terminal run=%+v err=%v", terminal, err)
	}
	terminalTasks, err := f.store.ListTasks(context.Background(), terminal.ID, 10)
	if err != nil || len(terminalTasks) != 1 || terminalTasks[0].Outcome != "cancelled" || terminalTasks[0].OperationID != reserved[0].OperationID {
		t.Fatalf("cancellation lost original task identity: tasks=%+v err=%v", terminalTasks, err)
	}
	if rows, listErr := f.store.ListSnapshots(context.Background(), "", 10); listErr != nil || len(rows) != 0 {
		t.Fatalf("fetch cancellation published a snapshot: rows=%+v err=%v", rows, listErr)
	}
	_ = os.RemoveAll(remote)
	if err = workflow.Close(context.Background()); err != nil {
		t.Fatalf("close cancelled workflow: %v", err)
	}
	newWorkflow, err := NewLocalWorkflowWithDiscovery(context.Background(), f.store, func(root store.RootBinding, _ config.Revision, _ chan<- backup.ProgressEvent) (SnapshotEngine, error) {
		return engine, nil
	}, credentials, func(config.Revision) (GitHubDiscoverer, error) { return &discoveryScript{}, nil })
	if err != nil {
		t.Fatalf("restart after terminal cancellation: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = newWorkflow.Close(ctx)
	}()
	if _, err = newWorkflow.coordinator.ReconcileStartup(context.Background()); err != nil {
		t.Fatalf("restart reconciliation: %v", err)
	}
	runs, err = f.store.QueryRuns(context.Background(), 10)
	if err != nil || len(runs) != 1 || runs[0].ID != terminal.ID || runs[0].State != "cancelled" {
		t.Fatalf("restart created or changed a run: %+v err=%v", runs, err)
	}
}

func TestPipelineSelectionAndCredentialRevision(t *testing.T) {
	f := newAppFixture(t)
	firstSource := createSyntheticGitRepository(t, f.dir, "captured-source-one", "captured one")
	secondSource := createSyntheticGitRepository(t, f.dir, "captured-source-two", "captured two")
	acquirer := &scriptedGitHubEngine{Engine: selectedTestEngine(t, f, nil), mirrorRoot: filepath.Join(f.root, "mirrors", "github"), sources: map[string]string{"201": firstSource, "202": secondSource}, failures: map[string]error{}}
	repositories := []github.Repository{discoveredRepo(201, 101, "alice", "User", "one"), discoveredRepo(202, 101, "alice", "User", "two")}
	discovery := &discoveryScript{results: []github.Result{completeDiscovery(repositories...)}}
	entered := make(chan secrets.Handle, 1)
	release := make(chan struct{})
	credentials := newFakeCredentialStore()
	credentials.read = func(ctx context.Context, handle secrets.Handle) ([]byte, error) {
		entered <- handle
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-release:
			return []byte(selectedCredential), nil
		}
	}
	_, workflow, _, revision := configureSelectedWorkflow(t, f, repositories, []string{"github:201", "github:202"}, acquirer, discovery, credentials)
	if err := workflow.Submit(Command{Kind: BackupSelected, ExpectedRevision: revision}); err != nil {
		t.Fatal(err)
	}
	var handle secrets.Handle
	select {
	case handle = <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("credential access did not begin after durable admission")
	}
	runs, err := f.store.QueryRuns(context.Background(), 10)
	if err != nil || len(runs) != 1 || runs[0].Config.ID() != revision || len(runs[0].Intent.RepositoryIDs) != 2 {
		t.Fatalf("captured selection/revision=%+v err=%v", runs, err)
	}
	reserved, err := f.store.ListTasks(context.Background(), runs[0].ID, 10)
	if err != nil || len(reserved) != 2 || reserved[0].OperationID == reserved[1].OperationID {
		t.Fatalf("per-repository snapshot IDs were not atomically reserved: %+v err=%v", reserved, err)
	}
	current, err := f.store.CurrentConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	rotated := current.Config()
	rotated.GitHub.Credential = &config.Credential{Reference: "replacement-handle", Generation: 2}
	rotated.Selection.RepositoryIDs = []string{"github:201"}
	newRevision, err := f.store.SaveConfig(context.Background(), current.ID(), rotated)
	if err != nil {
		t.Fatal(err)
	}
	close(release)
	waitDiscoveryState(t, workflow, func(state ViewState) bool { return !state.Busy && strings.Contains(state.Status, "completed") })
	finished, err := f.store.GetRun(context.Background(), runs[0].ID)
	if err != nil || finished.State != "succeeded" || finished.Config.ID() != revision || finished.Config.Config().GitHub.Credential == nil || finished.Config.Config().GitHub.Credential.Reference != "synthetic-selected-handle" {
		t.Fatalf("active run switched captured credential/config: %+v err=%v", finished, err)
	}
	if handle.Reference != "synthetic-selected-handle" || newRevision.ID() <= revision || !sameStrings(finished.Intent.RepositoryIDs, []string{"github:201", "github:202"}) {
		t.Fatalf("credential or selection revision changed the admitted work: handle=%+v revision=%d run=%+v", handle, newRevision.ID(), finished.Intent)
	}
	tasks, err := f.store.ListTasks(context.Background(), finished.ID, 10)
	if err != nil || len(tasks) != 2 || tasks[0].Outcome != "succeeded" || tasks[1].Outcome != "succeeded" {
		t.Fatalf("captured task set did not finish independently: %+v err=%v", tasks, err)
	}
}

type scriptedGitHubEngine struct {
	*backup.Engine
	mu         sync.Mutex
	mirrorRoot string
	sources    map[string]string
	failures   map[string]error
}

func (e *scriptedGitHubEngine) AcquireGitHub(ctx context.Context, id, cloneURL string, secret []byte) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.failures[id]; err != nil {
		return "", err
	}
	source := e.sources[id]
	if source == "" || !strings.HasPrefix(cloneURL, "https://github.com/") || !bytes.Equal(secret, []byte(selectedCredential)) {
		return "", errors.New("synthetic acquisition mismatch")
	}
	if err := os.MkdirAll(e.mirrorRoot, 0700); err != nil {
		return "", errors.New("synthetic mirror root unavailable")
	}
	mirror := filepath.Join(e.mirrorRoot, id+".git")
	command := exec.CommandContext(ctx, "git", "clone", "--mirror", "--no-local", "--", source, mirror)
	command.Env = testGitEnvironment()
	if output, err := command.CombinedOutput(); err != nil {
		return "", fmt.Errorf("synthetic local mirror clone failed (%d output bytes)", len(output))
	}
	return mirror, nil
}

func configureSelectedWorkflow(t *testing.T, f *appFixture, repositories []github.Repository, selection []string, engine SnapshotEngine, discovery GitHubDiscoverer, credentials *fakeCredentialStore) (*appFixture, *LocalWorkflow, *fakeCredentialStore, int64) {
	t.Helper()
	if credentials == nil {
		credentials = newFakeCredentialStore()
	}
	handle := secrets.Handle{Reference: "synthetic-selected-handle", Generation: 1}
	if _, err := credentials.values[handle]; !err {
		if err := credentials.Store(context.Background(), handle, []byte(selectedCredential)); err != nil {
			t.Fatal(err)
		}
	}
	current, err := f.store.CurrentConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	values := current.Config()
	values.GitHub.Credential = &config.Credential{Reference: handle.Reference, Generation: int64(handle.Generation)}
	values.Selection.RepositoryIDs = append([]string(nil), selection...)
	revision, err := f.store.SaveConfig(context.Background(), current.ID(), values)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.store.ReconcileGitHubInventory(context.Background(), store.GitHubAccount{ID: "101", Login: "alice", Type: "User"}, inventoryRepositories(repositories), true); err != nil {
		t.Fatal(err)
	}
	workflow, err := NewLocalWorkflowWithDiscovery(context.Background(), f.store, func(store.RootBinding, config.Revision, chan<- backup.ProgressEvent) (SnapshotEngine, error) {
		return engine, nil
	}, credentials, func(config.Revision) (GitHubDiscoverer, error) { return discovery, nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := workflow.Close(ctx); err != nil {
			t.Errorf("close selected backup workflow: %v", err)
		}
	})
	waitDiscoveryState(t, workflow, func(state ViewState) bool {
		return state.CanStartSelected && state.SelectedRepositoryCount == len(selection)
	})
	return f, workflow, credentials, revision.ID()
}

func selectedTestEngine(t *testing.T, f *appFixture, runner *backup.GitRunner) *backup.Engine {
	t.Helper()
	if runner == nil {
		scratch := filepath.Join(t.TempDir(), "git-scratch")
		if err := os.Mkdir(scratch, 0700); err != nil {
			t.Fatal(err)
		}
		var err error
		runner, err = backup.NewGitRunner(scratch, backup.DefaultGitRunnerOptions())
		if err != nil {
			t.Fatal(err)
		}
	}
	identity := f.store.Identity()
	engine, err := backup.NewEngine(backup.EngineOptions{Root: f.root, Runner: runner, InstallationID: identity.InstallationID, StoreID: identity.StoreID, ApplicationData: identity.DataDir})
	if err != nil {
		t.Fatal(err)
	}
	return engine
}

func newSyntheticHTTPSRunner(t *testing.T, server *syntheticSmartGitServer, scratch string) *backup.GitRunner {
	t.Helper()
	if err := os.Mkdir(scratch, 0700); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal("test executable path unavailable")
	}
	// The broker intentionally rejects writable executable images. Go's test
	// executable may inherit the developer umask, so qualify this isolated fixture.
	if err := os.Chmod(executable, 0700); err != nil {
		t.Fatal("test executable permissions unavailable")
	}
	certificate, err := x509.ParseCertificate(server.server.TLS.Certificates[0].Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	caPath := filepath.Join(filepath.Dir(scratch), "synthetic-ca.pem")
	if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	options := backup.DefaultGitRunnerOptions()
	options.CommandTimeout = 30 * time.Second
	prefix := []string{"-test.run=^TestGitHubCredentialHelperFixture$", "--", "--credential-helper-fixture", "--no-gate"}
	runner, err := backup.NewGitRunnerForTestingWithHelper(scratch, options, server.server.URL, caPath, prefix)
	if err != nil {
		t.Fatal("synthetic Git runner construction failed")
	}
	return runner
}

type syntheticSmartGitServer struct {
	server             *httptest.Server
	repositoryPath     string
	token              string
	block              bool
	requestStarted     chan struct{}
	requestCancelled   chan struct{}
	startOnce          sync.Once
	cancelOnce         sync.Once
	mu                 sync.Mutex
	authorized         int
	hasCredentialValue bool
}

func newSyntheticSmartGitServer(t *testing.T, repositoryPath, token string, block bool) *syntheticSmartGitServer {
	t.Helper()
	fixture := &syntheticSmartGitServer{repositoryPath: repositoryPath, token: token, block: block, requestStarted: make(chan struct{}), requestCancelled: make(chan struct{})}
	fixture.server = httptest.NewTLSServer(http.HandlerFunc(fixture.serveHTTP))
	t.Cleanup(fixture.server.Close)
	return fixture
}

func (s *syntheticSmartGitServer) Close() { s.server.Close() }

func (s *syntheticSmartGitServer) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/alice/demo.git/info/refs" && r.URL.Path != "/alice/demo.git/git-upload-pack" {
		http.NotFound(w, r)
		return
	}
	if !s.authorize(w, r) {
		return
	}
	if s.block {
		s.startOnce.Do(func() { close(s.requestStarted) })
		<-r.Context().Done()
		s.cancelOnce.Do(func() { close(s.requestCancelled) })
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/alice/demo.git/info/refs" && r.URL.Query().Get("service") == "git-upload-pack":
		advertisement, err := runUploadPack(r.Context(), s.repositoryPath, nil, true)
		if err != nil {
			http.Error(w, "synthetic Git advertisement failed", http.StatusInternalServerError)
			return
		}
		service := []byte("# service=git-upload-pack\n")
		body := append([]byte(fmt.Sprintf("%04x", len(service)+4)), service...)
		body = append(body, []byte("0000")...)
		body = append(body, advertisement...)
		w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(body)
	case r.Method == http.MethodPost && r.URL.Path == "/alice/demo.git/git-upload-pack":
		payload, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
		if err != nil || len(payload) >= 16<<20 {
			http.Error(w, "synthetic Git request exceeded bound", http.StatusRequestEntityTooLarge)
			return
		}
		result, err := runUploadPack(r.Context(), s.repositoryPath, payload, false)
		if err != nil {
			http.Error(w, "synthetic Git upload-pack failed", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/x-git-upload-pack-result")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(result)
	default:
		http.NotFound(w, r)
	}
}

func (s *syntheticSmartGitServer) authorize(w http.ResponseWriter, r *http.Request) bool {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(header, "Basic ") {
		data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(header, "Basic "))
		username, password, ok := strings.Cut(string(data), ":")
		if err == nil && ok && username == "x-access-token" && password == s.token {
			s.mu.Lock()
			s.authorized++
			s.hasCredentialValue = true
			s.mu.Unlock()
			return true
		}
	}
	w.Header().Set("WWW-Authenticate", `Basic realm="synthetic GitVault fixture"`)
	w.WriteHeader(http.StatusUnauthorized)
	return false
}

func (s *syntheticSmartGitServer) hasCredential() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hasCredentialValue
}

func (s *syntheticSmartGitServer) authorizedRequests() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.authorized
}

func runUploadPack(ctx context.Context, repositoryPath string, input []byte, advertise bool) ([]byte, error) {
	args := []string{"upload-pack", "--stateless-rpc"}
	if advertise {
		args = append(args, "--advertise-refs")
	}
	args = append(args, repositoryPath)
	command := exec.CommandContext(ctx, "git", args...)
	command.Env = testGitEnvironment()
	if input != nil {
		command.Stdin = bytes.NewReader(input)
	}
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("synthetic upload-pack failed (%d diagnostic bytes)", stderr.Len())
	}
	return stdout.Bytes(), nil
}

func createSyntheticGitRepository(t *testing.T, base, name, content string) string {
	t.Helper()
	bare := filepath.Join(base, name+".git")
	working := filepath.Join(base, name+"-work")
	runTestGit(t, base, "init", "--bare", "--initial-branch=main", bare)
	runTestGit(t, base, "init", "--initial-branch=main", working)
	runTestGit(t, working, "config", "user.name", "Synthetic GitVault")
	runTestGit(t, working, "config", "user.email", "synthetic@example.invalid")
	if err := os.WriteFile(filepath.Join(working, "README.md"), []byte(content+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, working, "add", "README.md")
	runTestGit(t, working, "commit", "-m", "synthetic history")
	runTestGit(t, working, "remote", "add", "fixture", bare)
	runTestGit(t, working, "push", "fixture", "HEAD:refs/heads/main")
	return bare
}

func runTestGit(t *testing.T, directory string, args ...string) []byte {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	command.Env = testGitEnvironment()
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("synthetic Git command failed: %v: %s", err, strings.TrimSpace(string(output)))
	}
	return output
}

func testGitEnvironment() []string {
	return []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.TempDir(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_PROTOCOL_FROM_USER=0", "GIT_ALLOW_PROTOCOL=file", "LC_ALL=C", "LANG=C"}
}

func assertNoCredentialInManagedFiles(t *testing.T, root string, sentinel []byte) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(data, sentinel) {
			return fmt.Errorf("synthetic credential appeared in managed file %s", filepath.Base(path))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func sameStringMap(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}
