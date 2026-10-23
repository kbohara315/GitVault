package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"gitvault/internal/backup"
	"gitvault/internal/config"
	"gitvault/internal/store"
)

var ErrCoordinatorClosed = errors.New("run coordinator is closed")
var ErrStartupReconciliationRequired = errors.New("startup reconciliation must complete before managed admission")

// SnapshotEngine is the recovery capability consumed by managed local runs.
// Its filesystem and Git work always runs outside native views.
type SnapshotEngine interface {
	Create(context.Context, backup.Request) (backup.Result, error)
	Inspect(context.Context, string) (backup.Manifest, error)
	Verify(context.Context, string) (backup.Verification, error)
}

// SnapshotEngineFactory maps one admitted immutable configuration revision to
// module-owned recovery options. It runs after durable admission has captured
// the revision and before any source or backup-root mutation begins.
type SnapshotEngineFactory func(config.Revision) (SnapshotEngine, error)

// GitHubRunExecutor owns fresh metadata lookup and remote acquisition. It must
// finish all child workers before returning and call complete once per task;
// completed facts are persisted immediately, independent of later repositories.
type GitHubRunExecutor func(context.Context, store.Run, SnapshotEngine, []store.Task, func(store.Task, string) error, func(store.Task, backup.Result, error) error) error

type Coordinator struct {
	store   *store.Store
	root    store.RootBinding
	engine  SnapshotEngine
	factory SnapshotEngineFactory
	ctx     context.Context
	cancel  context.CancelFunc

	mu          sync.Mutex
	starting    bool
	reconciling bool
	ready       bool
	closed      bool
	active      *activeRun
}

type activeRun struct {
	run          store.Run
	task         store.Task
	engine       SnapshotEngine
	request      LocalBackupRequest
	tasks        []store.Task
	cancel       context.CancelFunc
	releaseRun   func()
	done         chan struct{}
	terminalErr  error
	cancelAsked  bool
	terminalDone bool
}

type GitHubBackupRequest struct {
	ID, Trigger, AccountID string
	ExpectedRevision       int64
	RepositoryIDs          []string
}

// NewCoordinatorWithFactory constructs a startup inspection engine from the
// current revision and captures a fresh engine for each admitted run revision.
func NewCoordinatorWithFactory(parent context.Context, s *store.Store, root store.RootBinding, factory SnapshotEngineFactory) (*Coordinator, error) {
	if parent == nil || s == nil || factory == nil {
		return nil, errors.New("application context, store, and snapshot engine factory are required")
	}
	revision, err := s.CurrentConfig(parent)
	if err != nil {
		return nil, err
	}
	engine, err := factory(revision)
	if err != nil {
		return nil, err
	}
	c, err := NewCoordinator(parent, s, root, engine)
	if err != nil {
		return nil, err
	}
	c.factory = factory
	return c, nil
}

func (c *Coordinator) Ready() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ready && !c.reconciling && !c.starting && c.active == nil && !c.closed
}

// LocalBackupRequest is an explicit local-source intent. RepositoryID may be
// empty to create a new local identity; paths are captured durably at admission.
type LocalBackupRequest struct {
	ID, Trigger, SourcePath, RepositoryID, DisplayName string
}

// NewCoordinator validates the root binding before accepting managed work.
// parent is the application lifetime context, not an individual view context.
func NewCoordinator(parent context.Context, s *store.Store, root store.RootBinding, engine SnapshotEngine) (*Coordinator, error) {
	if parent == nil || s == nil || engine == nil {
		return nil, errors.New("application context, store, and snapshot engine are required")
	}
	canonical, fsID, err := canonicalManagedRoot(root.Path)
	if err != nil {
		return nil, err
	}
	release, err := lockManagedRoot(parent, canonical)
	if err != nil {
		return nil, err
	}
	lockedRoot, lockedFSID, err := canonicalManagedRoot(canonical)
	if err != nil || lockedRoot != canonical || lockedFSID != fsID {
		release()
		return nil, fmt.Errorf("%w: managed root identity changed while locking", store.ErrForeignRoot)
	}
	marker, err := readManagedRootMarker(filepath.Join(canonical, managedRootMarkerName))
	if err != nil {
		release()
		return nil, err
	}
	checked, err := validateManagedRootLocked(parent, s, canonical, fsID, marker)
	release()
	if err != nil {
		return nil, err
	}
	if checked != root {
		return nil, store.ErrForeignRoot
	}
	ctx, cancel := context.WithCancel(parent)
	return &Coordinator{store: s, root: root, engine: engine, ctx: ctx, cancel: cancel}, nil
}

// StartLocal commits the immutable intent and task identity before it launches
// the worker. The task OperationID is also the reserved snapshot ID for this
// one-artifact local operation, so restart reconciliation can inspect one exact
// path without guessing from timestamps or repository names.
func (c *Coordinator) StartLocal(ctx context.Context, request LocalBackupRequest) (store.Run, error) {
	if ctx == nil {
		return store.Run{}, errors.New("admission context required")
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return store.Run{}, ErrCoordinatorClosed
	}
	if c.starting || c.active != nil || c.reconciling {
		c.mu.Unlock()
		return store.Run{}, &store.BusyError{}
	}
	if !c.ready {
		c.mu.Unlock()
		return store.Run{}, ErrStartupReconciliationRequired
	}
	c.starting = true
	c.mu.Unlock()
	started := false
	defer func() {
		if !started {
			c.mu.Lock()
			c.starting = false
			c.mu.Unlock()
		}
	}()

	if request.Trigger == "" {
		request.Trigger = "manual"
	}
	if request.Trigger != "manual" && request.Trigger != "scheduled" && request.Trigger != "catch_up" && request.Trigger != "retry" {
		return store.Run{}, errors.New("invalid run trigger")
	}
	if !filepath.IsAbs(request.SourcePath) || filepath.Clean(request.SourcePath) != request.SourcePath || strings.ContainsAny(request.SourcePath, "\x00\r\n") || !utf8.ValidString(request.SourcePath) {
		return store.Run{}, errors.New("source path must be a clean absolute path")
	}
	if request.RepositoryID == "" {
		id, err := backup.NewID()
		if err != nil {
			return store.Run{}, err
		}
		request.RepositoryID = id
	}
	if !validUUIDv4(request.RepositoryID) {
		return store.Run{}, errors.New("local repository identity must be a UUID")
	}
	if request.DisplayName == "" {
		request.DisplayName = filepath.Base(request.SourcePath)
	}
	if !safeDisplayName(request.DisplayName) {
		return store.Run{}, errors.New("invalid local repository display name")
	}
	if request.ID == "" {
		id, err := backup.NewID()
		if err != nil {
			return store.Run{}, err
		}
		request.ID = id
	}
	taskID, err := backup.NewID()
	if err != nil {
		return store.Run{}, err
	}
	reservedSnapshotID, err := backup.NewID()
	if err != nil {
		return store.Run{}, err
	}

	// Keep the coordinator ownership lock until terminal facts are durable. The
	// engine's distinct .gitvault.lock is acquired only after this short root
	// identity/admission section has released it.
	releaseRun, err := lockCoordinatorRun(ctx, c.root.Path)
	if err != nil {
		return store.Run{}, err
	}
	releaseRoot, err := lockManagedRoot(ctx, c.root.Path)
	if err != nil {
		releaseRun()
		return store.Run{}, err
	}
	canonical, fsID, err := canonicalManagedRoot(c.root.Path)
	if err == nil {
		var marker managedRootMarker
		marker, err = readManagedRootMarker(filepath.Join(canonical, managedRootMarkerName))
		if err == nil {
			var checked store.RootBinding
			checked, err = validateManagedRootLocked(ctx, c.store, canonical, fsID, marker)
			if err == nil && checked != c.root {
				err = store.ErrForeignRoot
			}
		}
	}
	if err != nil {
		releaseRoot()
		releaseRun()
		return store.Run{}, err
	}

	run, err := c.store.BeginRun(ctx, store.RunIntent{ID: request.ID, Trigger: request.Trigger, Kind: "local_backup", RootID: c.root.ID, RepositoryIDs: []string{request.RepositoryID}, SourcePath: request.SourcePath})
	if err != nil {
		releaseRoot()
		releaseRun()
		return store.Run{}, err
	}
	engine := c.engine
	if c.factory != nil {
		engine, err = c.factory(run.Config)
		if err != nil {
			releaseRoot()
			c.failAdmission(run.ID, releaseRun, err)
			return store.Run{}, err
		}
	}
	task := store.Task{ID: taskID, RunID: run.ID, RepositoryID: request.RepositoryID, Provider: "local", OperationID: reservedSnapshotID, Stage: "pending", Outcome: "pending", Attempt: 1, PublicationState: string(backup.NotPublished), CloudOutcome: "not_requested"}
	if err = c.store.CreateTask(ctx, task); err != nil {
		releaseRoot()
		c.failAdmission(run.ID, releaseRun, err)
		return store.Run{}, err
	}
	if err = c.store.SetRunState(ctx, run.ID, "running", ""); err != nil {
		releaseRoot()
		c.failAdmission(run.ID, releaseRun, err)
		return store.Run{}, err
	}
	releaseRoot()
	run.State = "running"

	jobCtx, cancel := context.WithCancel(c.ctx)
	job := &activeRun{run: run, task: task, engine: engine, request: request, cancel: cancel, releaseRun: releaseRun, done: make(chan struct{})}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		cancel()
		c.failAdmission(run.ID, releaseRun, ErrCoordinatorClosed)
		return store.Run{}, ErrCoordinatorClosed
	}
	c.active = job
	c.starting = false
	c.mu.Unlock()
	started = true
	go c.execute(jobCtx, job)
	return run, nil
}

// StartGitHub durably captures the selected stable IDs and reserves every task
// and snapshot identity before any credential prompt, REST request, or Git call.
func (c *Coordinator) StartGitHub(ctx context.Context, request GitHubBackupRequest, execute GitHubRunExecutor) (store.Run, error) {
	if ctx == nil || execute == nil || request.ExpectedRevision < 1 || !storeGitHubAccountID(request.AccountID) || len(request.RepositoryIDs) == 0 || len(request.RepositoryIDs) > 10000 {
		return store.Run{}, errors.New("valid selected GitHub repositories and executor are required")
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return store.Run{}, ErrCoordinatorClosed
	}
	if c.starting || c.active != nil || c.reconciling {
		c.mu.Unlock()
		return store.Run{}, &store.BusyError{}
	}
	if !c.ready {
		c.mu.Unlock()
		return store.Run{}, ErrStartupReconciliationRequired
	}
	c.starting = true
	c.mu.Unlock()
	started := false
	defer func() {
		if !started {
			c.mu.Lock()
			c.starting = false
			c.mu.Unlock()
		}
	}()

	if request.Trigger == "" {
		request.Trigger = "manual"
	}
	if request.Trigger != "manual" && request.Trigger != "retry" {
		return store.Run{}, errors.New("invalid GitHub run trigger")
	}
	seen := make(map[string]bool, len(request.RepositoryIDs))
	for _, id := range request.RepositoryIDs {
		if !stableRepositoryID(id) || seen[id] {
			return store.Run{}, errors.New("invalid or duplicate selected GitHub repository ID")
		}
		seen[id] = true
	}
	if request.ID == "" {
		id, err := backup.NewID()
		if err != nil {
			return store.Run{}, err
		}
		request.ID = id
	}
	reservations := make([]store.Task, 0, len(request.RepositoryIDs))
	for range request.RepositoryIDs {
		taskID, err := backup.NewID()
		if err != nil {
			return store.Run{}, err
		}
		snapshotID, err := backup.NewID()
		if err != nil {
			return store.Run{}, err
		}
		reservations = append(reservations, store.Task{ID: taskID, RunID: request.ID, Provider: "github", OperationID: snapshotID, Stage: "pending", Outcome: "pending", Attempt: 1, PublicationState: string(backup.NotPublished), CloudOutcome: "not_requested"})
	}

	releaseRun, err := lockCoordinatorRun(ctx, c.root.Path)
	if err != nil {
		return store.Run{}, err
	}
	releaseRoot, err := lockManagedRoot(ctx, c.root.Path)
	if err != nil {
		releaseRun()
		return store.Run{}, err
	}
	canonical, fsID, err := canonicalManagedRoot(c.root.Path)
	if err == nil {
		var marker managedRootMarker
		marker, err = readManagedRootMarker(filepath.Join(canonical, managedRootMarkerName))
		if err == nil {
			var checked store.RootBinding
			checked, err = validateManagedRootLocked(ctx, c.store, canonical, fsID, marker)
			if err == nil && checked != c.root {
				err = store.ErrForeignRoot
			}
		}
	}
	if err != nil {
		releaseRoot()
		releaseRun()
		return store.Run{}, err
	}

	run, tasks, err := c.store.BeginGitHubRun(ctx, store.RunIntent{ID: request.ID, Trigger: request.Trigger, Kind: "github_backup", RootID: c.root.ID, AccountID: request.AccountID, RepositoryIDs: append([]string(nil), request.RepositoryIDs...)}, request.ExpectedRevision, reservations)
	if err != nil {
		releaseRoot()
		releaseRun()
		return store.Run{}, err
	}
	engine := c.engine
	if c.factory != nil {
		engine, err = c.factory(run.Config)
		if err != nil {
			releaseRoot()
			c.failGitHubAdmission(run.ID, tasks, releaseRun, err)
			return store.Run{}, err
		}
	}
	if err = c.store.SetRunState(ctx, run.ID, "running", ""); err != nil {
		releaseRoot()
		c.failGitHubAdmission(run.ID, tasks, releaseRun, err)
		return store.Run{}, err
	}
	releaseRoot()
	run.State = "running"
	jobCtx, cancel := context.WithCancel(c.ctx)
	job := &activeRun{run: run, tasks: tasks, engine: engine, cancel: cancel, releaseRun: releaseRun, done: make(chan struct{})}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		cancel()
		c.failGitHubAdmission(run.ID, tasks, releaseRun, ErrCoordinatorClosed)
		return store.Run{}, ErrCoordinatorClosed
	}
	c.active = job
	c.starting = false
	c.mu.Unlock()
	started = true
	go c.executeGitHub(jobCtx, job, execute)
	return run, nil
}

func storeGitHubAccountID(value string) bool {
	if value == "" || len(value) > 32 || value[0] == '0' {
		return false
	}
	for _, c := range value {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func (c *Coordinator) failGitHubAdmission(runID string, tasks []store.Task, release func(), cause error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var persistErr error
	for _, task := range tasks {
		persistErr = errors.Join(persistErr, c.store.UpdateTask(ctx, task.ID, store.TaskUpdate{Stage: "failed", Outcome: "failed", Attempt: task.Attempt, PublicationState: string(backup.NotPublished), CloudOutcome: "not_requested", ErrorCode: "admission_failed"}))
	}
	persistErr = errors.Join(persistErr, c.store.FinishRun(ctx, runID, "failed", "admission_failed"))
	if persistErr == nil {
		release()
		return
	}
	job := &activeRun{run: store.Run{ID: runID}, tasks: tasks, releaseRun: release, done: make(chan struct{}), terminalErr: errors.Join(cause, persistErr), terminalDone: true}
	close(job.done)
	c.mu.Lock()
	c.active = job
	c.starting = false
	c.mu.Unlock()
}

func (c *Coordinator) failAdmission(runID string, release func(), cause error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := c.store.FinishRun(ctx, runID, "failed", "admission_failed")
	if err == nil {
		release()
		return
	}
	// Keep root ownership if terminal intent could not be made durable. The
	// process can exit and startup reconciliation will inspect the original ID.
	job := &activeRun{run: store.Run{ID: runID}, releaseRun: release, done: make(chan struct{}), terminalErr: errors.Join(cause, err), terminalDone: true}
	close(job.done)
	c.mu.Lock()
	c.active = job
	c.starting = false
	c.mu.Unlock()
}

func (c *Coordinator) execute(ctx context.Context, job *activeRun) {
	result, engineErr := job.engine.Create(ctx, backup.Request{Source: job.request.SourcePath, Repository: backup.RepositoryIdentity{Provider: "local", ID: job.request.RepositoryID, DisplayName: job.request.DisplayName}, ApplicationVersion: "0.1.0", SnapshotID: job.task.OperationID})
	c.mu.Lock()
	defer c.mu.Unlock()
	if job.terminalDone {
		return
	}
	if job.cancelAsked && engineErr == nil {
		engineErr = context.Canceled
	}
	persistCtx, cancelPersist := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancelPersist()
	outcome, taskErrCode, persistErr := c.persistTaskResult(persistCtx, job, result, engineErr)
	if persistErr == nil {
		runOutcome := runOutcomeForTask(outcome)
		errorCode := ""
		if runOutcome == "failed" {
			errorCode = taskErrCode
		} else if runOutcome == "cancelled" {
			errorCode = "cancelled"
		}
		persistErr = c.store.FinishRun(persistCtx, job.run.ID, runOutcome, errorCode)
	}
	if persistErr != nil {
		job.terminalErr = fmt.Errorf("persist terminal run facts: %w", persistErr)
		job.terminalDone = true
		close(job.done)
		return // retain process/root ownership until restart reconciliation.
	}
	job.terminalDone = true
	job.releaseRun()
	if c.active == job {
		c.active = nil
	}
	close(job.done)
}

func (c *Coordinator) executeGitHub(ctx context.Context, job *activeRun, execute GitHubRunExecutor) {
	type taskRuntime struct {
		task      store.Task
		stage     string
		completed bool
	}
	runtimes := make(map[string]*taskRuntime, len(job.tasks))
	for _, task := range job.tasks {
		runtimes[task.ID] = &taskRuntime{task: task, stage: task.Stage}
	}
	var runtimeMu sync.Mutex
	var persistenceErr error
	setStage := func(task store.Task, stage string) error {
		runtimeMu.Lock()
		defer runtimeMu.Unlock()
		runtime := runtimes[task.ID]
		if runtime == nil || runtime.completed {
			return store.ErrInvalidTransition
		}
		update := store.TaskUpdate{Stage: stage, Outcome: "pending", Attempt: runtime.task.Attempt, PublicationState: string(backup.NotPublished), CloudOutcome: "not_requested"}
		if err := c.advanceTaskTo(ctx, runtime.task, update, runtime.stage, stage, "pending"); err != nil {
			return err
		}
		runtime.stage = stage
		runtime.task.Stage = stage
		return nil
	}
	complete := func(task store.Task, result backup.Result, engineErr error) error {
		runtimeMu.Lock()
		runtime := runtimes[task.ID]
		if runtime == nil || runtime.completed {
			runtimeMu.Unlock()
			return store.ErrInvalidTransition
		}
		runtime.completed = true
		captured := runtime.task
		runtimeMu.Unlock()
		perTask := *job
		perTask.task = captured
		_, _, err := c.persistTaskResult(context.Background(), &perTask, result, engineErr)
		if err != nil {
			runtimeMu.Lock()
			persistenceErr = errors.Join(persistenceErr, err)
			runtimeMu.Unlock()
			job.cancel()
			return err
		}
		return nil
	}

	executeErr := execute(ctx, job.run, job.engine, append([]store.Task(nil), job.tasks...), setStage, complete)
	for _, task := range job.tasks {
		runtimeMu.Lock()
		runtime := runtimes[task.ID]
		missing := runtime != nil && !runtime.completed
		captured := task
		if runtime != nil {
			captured = runtime.task
		}
		runtimeMu.Unlock()
		if !missing {
			continue
		}
		cause := executeErr
		if cause == nil {
			cause = errors.New("repository task did not produce a result")
		}
		outcome := backup.OutcomeFailed
		if errors.Is(cause, context.Canceled) {
			outcome = backup.OutcomeCancelled
		}
		result, _ := backup.NewResult(backup.ResultFacts{Outcome: outcome, Publication: backup.NotPublished})
		if err := complete(captured, result, cause); err != nil {
			executeErr = errors.Join(executeErr, err)
		}
	}

	runtimeMu.Lock()
	persistErr := persistenceErr
	runtimeMu.Unlock()
	if persistErr != nil {
		c.mu.Lock()
		job.terminalErr = fmt.Errorf("persist GitHub task facts: %w", persistErr)
		job.terminalDone = true
		close(job.done)
		c.mu.Unlock()
		return // keep managed-root ownership for startup reconciliation.
	}

	persistCtx, cancelPersist := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancelPersist()
	tasks, err := c.store.ListTasks(persistCtx, job.run.ID, 10000)
	if err != nil || len(tasks) != len(job.tasks) {
		if err == nil {
			err = errors.New("GitHub task inventory is incomplete")
		}
		c.mu.Lock()
		job.terminalErr = err
		job.terminalDone = true
		close(job.done)
		c.mu.Unlock()
		return
	}
	succeeded, failed, cancelled, interrupted := 0, 0, 0, 0
	for _, task := range tasks {
		switch task.Outcome {
		case "succeeded":
			succeeded++
		case "cancelled":
			cancelled++
		case "interrupted":
			interrupted++
		default:
			failed++
		}
	}
	c.mu.Lock()
	cancelAsked := job.cancelAsked
	if cancelAsked {
		err = c.store.FinishRun(persistCtx, job.run.ID, "cancelled", "cancelled")
	} else {
		outcome, code := "failed", "task_failures"
		switch {
		case succeeded == len(tasks):
			outcome, code = "succeeded", ""
		case succeeded > 0:
			outcome, code = "partial", "partial_results"
		case interrupted == len(tasks):
			outcome, code = "interrupted", "owner_lost"
		case cancelled == len(tasks):
			outcome, code = "cancelled", "cancelled"
		case failed == 0 && interrupted > 0:
			outcome, code = "interrupted", "owner_lost"
		}
		err = c.store.FinishRun(persistCtx, job.run.ID, outcome, code)
	}
	if err != nil {
		job.terminalErr = fmt.Errorf("persist terminal GitHub run facts: %w", err)
		job.terminalDone = true
		close(job.done)
		c.mu.Unlock()
		return
	}
	job.terminalDone = true
	job.releaseRun()
	if c.active == job {
		c.active = nil
	}
	close(job.done)
	c.mu.Unlock()
}

func (c *Coordinator) persistTaskResult(ctx context.Context, job *activeRun, result backup.Result, engineErr error) (string, string, error) {
	taskOutcome, errorCode := outcomeForResult(result, engineErr)
	artifact := result.Artifact()
	publication := string(result.Publication())
	artifactID, artifactPath := artifact.SnapshotID, artifact.Path
	indexedSnapshotID := ""
	stage := "pending"

	if artifact != (backup.ArtifactIdentity{}) {
		expectedID := job.task.OperationID
		provider, repositoryID := job.task.Provider, job.task.RepositoryID
		expectedPath := filepath.Join(c.root.Path, "snapshots", provider, repositoryID, expectedID)
		if artifact.SnapshotID != expectedID || artifact.Provider != provider || artifact.RepositoryID != repositoryID || filepath.Clean(artifact.Path) != expectedPath {
			taskOutcome, errorCode = "failed", "artifact_identity_conflict"
			publication = string(backup.PublicationUnknown)
		} else {
			manifest, inspectErr := job.engine.Inspect(ctx, expectedPath)
			verification, verifyErr := job.engine.Verify(ctx, expectedPath)
			if inspectErr != nil || verifyErr != nil || manifest.SnapshotID != expectedID || manifest.Repository.ID != repositoryID || manifest.Repository.Provider != provider {
				taskOutcome, errorCode = "failed", "artifact_verification_failed"
				publication = string(backup.PublicationUnknown)
			} else {
				relative, relErr := filepath.Rel(c.root.Path, expectedPath)
				if relErr != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
					return "failed", "artifact_identity_conflict", errors.New("published artifact escaped managed root")
				}
				if result.Publication() == backup.NotPublished || result.Publication() == backup.PublicationUnknown {
					publication = string(backup.PublishedDurabilityUncertain)
				}
				snapshot := snapshotRecord(c.root.ID, relative, manifest, publication, verification)
				if err := c.store.IndexSnapshot(ctx, snapshot); err != nil {
					taskOutcome, errorCode = "failed", "snapshot_index_failed"
				} else {
					indexedSnapshotID = expectedID
					stage = "local_ready"
				}
			}
		}
	}

	update := store.TaskUpdate{Stage: stage, Outcome: "pending", Attempt: job.task.Attempt, ArtifactID: artifactID, ArtifactPath: artifactPath, PublicationState: publication, CloudOutcome: "not_requested", ErrorCode: ""}
	if indexedSnapshotID != "" {
		update.SnapshotID = indexedSnapshotID
	}
	if taskOutcome == "succeeded" && indexedSnapshotID != "" {
		if err := c.advanceTaskTo(ctx, job.task, update, job.task.Stage, "succeeded", "succeeded"); err != nil {
			return "failed", "task_persistence_failed", err
		}
		return "succeeded", "", nil
	}
	if taskOutcome == "cancelled" && indexedSnapshotID != "" {
		if err := c.advanceTaskTo(ctx, job.task, update, job.task.Stage, "local_ready", "cancelled"); err != nil {
			return "failed", "task_persistence_failed", err
		}
		return "cancelled", "cancelled", nil
	}
	if taskOutcome == "succeeded" {
		taskOutcome, errorCode = "failed", "artifact_missing"
	}
	if taskOutcome == "empty_repository" {
		update.Stage, update.Outcome, update.ErrorCode = job.task.Stage, "empty_repository", errorCode
	} else if taskOutcome == "cancelled" || taskOutcome == "interrupted" {
		// A terminal outcome does not rewind the durable stage. For example,
		// cancellation during HTTPS acquisition remains at fetching.
		update.Stage, update.Outcome, update.ErrorCode = job.task.Stage, taskOutcome, errorCode
	} else {
		update.Stage, update.Outcome, update.ErrorCode = "failed", "failed", errorCode
	}
	if err := c.store.UpdateTask(ctx, job.task.ID, update); err != nil {
		return "failed", "task_persistence_failed", err
	}
	return taskOutcome, errorCode, nil
}

func (c *Coordinator) advanceTaskTo(ctx context.Context, task store.Task, update store.TaskUpdate, current, target, finalOutcome string) error {
	stages := []string{"fetching", "bundling", "verifying", "publishing", "local_ready", "succeeded"}
	currentIndex, targetIndex := -1, -1
	for i, stage := range stages {
		if stage == current {
			currentIndex = i
		}
		if stage == target {
			targetIndex = i
		}
	}
	if current != "pending" && currentIndex < 0 || targetIndex < 0 || currentIndex > targetIndex {
		return store.ErrInvalidTransition
	}
	if currentIndex == targetIndex {
		update.Stage, update.Outcome = target, finalOutcome
		return c.store.UpdateTask(ctx, task.ID, update)
	}
	start := currentIndex + 1
	for _, stage := range stages[start : targetIndex+1] {
		step := update
		step.Stage = stage
		step.Outcome = "pending"
		if stage != "local_ready" && stage != "succeeded" {
			step.SnapshotID = ""
			step.ArtifactID = ""
			step.ArtifactPath = ""
			step.PublicationState = string(backup.NotPublished)
		}
		if stage == target {
			step.Outcome = finalOutcome
		}
		if err := c.store.UpdateTask(ctx, task.ID, step); err != nil {
			return fmt.Errorf("persist task stage %s: %w", stage, err)
		}
	}
	return nil
}

func snapshotRecord(rootID, relative string, manifest backup.Manifest, publication string, verification backup.Verification) store.Snapshot {
	return store.Snapshot{ID: manifest.SnapshotID, RepositoryID: manifest.Repository.ID, Provider: manifest.Repository.Provider, RootID: rootID, FormatVersion: manifest.FormatVersion, RelativePath: filepath.ToSlash(relative), State: "ready", PublicationState: publication, SHA256: manifest.Bundle.SHA256, SizeBytes: manifest.Bundle.SizeBytes, CreatedAt: manifest.CreatedAt.UTC(), Verification: store.Verification{Manifest: string(verification.Manifest), Hash: string(verification.Hash), IndependentRefs: string(verification.IndependentRefs), Head: string(verification.Head), ObjectIntegrity: string(verification.ObjectIntegrity), OfflineRestore: string(verification.OfflineRestore)}}
}

func outcomeForResult(result backup.Result, err error) (outcome, code string) {
	if errors.Is(err, context.Canceled) || result.Outcome() == backup.OutcomeCancelled {
		return "cancelled", "cancelled"
	}
	if result.Outcome() == backup.OutcomeEmptyRepository || errors.Is(err, backup.ErrEmptyRepository) {
		return "empty_repository", "empty_repository"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "failed", "operation_timeout"
	}
	if err != nil || result.Outcome() == backup.OutcomeFailed {
		return "failed", "engine_failed"
	}
	if result.Outcome() == backup.OutcomeInterrupted {
		return "interrupted", "owner_lost"
	}
	return "succeeded", ""
}

func runOutcomeForTask(outcome string) string {
	switch outcome {
	case "succeeded":
		return "succeeded"
	case "cancelled":
		return "cancelled"
	case "interrupted":
		return "interrupted"
	default:
		return "failed"
	}
}

func (c *Coordinator) Cancel(ctx context.Context, runID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	job := c.active
	if job == nil || job.run.ID != runID || job.terminalDone {
		return store.ErrNotFound
	}
	run, err := c.store.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	switch run.State {
	case "running":
		if err = c.store.SetRunState(ctx, runID, "cancelling", ""); err != nil {
			return err
		}
	case "cancelling":
	default:
		return store.ErrInvalidTransition
	}
	job.cancelAsked = true
	job.cancel()
	return nil
}

// Wait returns only after task/run terminal facts are durable. A terminal write
// failure is returned while ownership remains held for restart reconciliation.
func (c *Coordinator) Wait(ctx context.Context, runID string) (store.Run, error) {
	c.mu.Lock()
	job := c.active
	if job == nil || job.run.ID != runID {
		c.mu.Unlock()
		return c.store.GetRun(ctx, runID)
	}
	done := job.done
	c.mu.Unlock()
	select {
	case <-ctx.Done():
		return store.Run{}, ctx.Err()
	case <-done:
	}
	if job.terminalErr != nil {
		return store.Run{}, job.terminalErr
	}
	return c.store.GetRun(ctx, runID)
}

// Close prevents new admission and requests cancellation of active work. It
// waits only for terminal persistence, not for any native view callback.
func (c *Coordinator) Close(ctx context.Context) error {
	c.mu.Lock()
	c.closed = true
	job := c.active
	c.cancel()
	if job == nil || job.terminalDone {
		c.mu.Unlock()
		return nil
	}
	run, err := c.store.GetRun(ctx, job.run.ID)
	if err == nil && run.State == "running" {
		err = c.store.SetRunState(ctx, run.ID, "cancelling", "")
	}
	job.cancelAsked = true
	job.cancel()
	done := job.done
	c.mu.Unlock()
	if err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return job.terminalErr
	}
}

func validUUIDv4(s string) bool {
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' || s[14] != '4' || !strings.ContainsRune("89ab", rune(s[19])) {
		return false
	}
	for i, r := range s {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			continue
		}
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

func safeDisplayName(s string) bool {
	if len(s) == 0 || len(s) > 256 || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r < 32 || r == 127 {
			return false
		}
	}
	return true
}
