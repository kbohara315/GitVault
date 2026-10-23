package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"gitvault/internal/backup"
	"gitvault/internal/config"
	"gitvault/internal/secrets"
	"gitvault/internal/store"
)

var (
	ErrWorkflowClosed             = errors.New("local workflow is closed")
	ErrCommandQueueFull           = errors.New("application command queue is full")
	ErrUnsupportedCommand         = errors.New("this command is not available in the current build")
	ErrCredentialOperationPending = errors.New("native credential interaction is pending")
	ErrWorkflowBusy               = errors.New("application operation is busy")
)

// LocalEngineBuilder maps one immutable configuration revision to backup-owned
// options. The progress channel is bounded and the recovery engine publishes to
// it without waiting for the UI.
type LocalEngineBuilder func(store.RootBinding, config.Revision, chan<- backup.ProgressEvent) (SnapshotEngine, error)

type workflowCompletion struct {
	run store.Run
	err error
}

// LocalWorkflow is the asynchronous bridge between native commands, durable
// application coordination, and immutable UI snapshots. Its worker is the only
// writer of presentation state. Views only call Snapshot and nonblocking Submit.
type LocalWorkflow struct {
	store       *store.Store
	build       LocalEngineBuilder
	credentials CredentialStore
	discovery   GitHubDiscoveryFactory
	ctx         context.Context
	cancel      context.CancelFunc
	events      *Events
	commands    chan Command
	progress    chan backup.ProgressEvent
	finished    chan workflowCompletion
	discoveries chan discoveryCompletion
	done        chan struct{}
	discoveryWG sync.WaitGroup

	mu      sync.RWMutex
	state   ViewState
	logMu   sync.Mutex
	logPath string

	root                     store.RootBinding
	hasRoot                  bool
	coordinator              *Coordinator
	ready                    bool
	activeRunID              string
	activeRepoID             string
	lastRunID                string
	closed                   bool
	rootRebindBlocked        bool
	discoveryCancel          context.CancelFunc
	discoveryCancelRequested bool
}

// NewLocalWorkflow opens no paths and starts no work. It loads current durable
// state, validates any existing managed-root binding, reconciles interrupted
// work, and then starts one bounded command worker.
func NewLocalWorkflow(parent context.Context, s *store.Store, build LocalEngineBuilder) (*LocalWorkflow, error) {
	return NewLocalWorkflowWithCredentials(parent, s, build, nil)
}

// NewLocalWorkflowWithCredentials composes explicit native credential
// onboarding into the same bounded worker that owns durable settings edits.
func NewLocalWorkflowWithCredentials(parent context.Context, s *store.Store, build LocalEngineBuilder, credentials CredentialStore) (*LocalWorkflow, error) {
	return newLocalWorkflow(parent, s, build, credentials, nil)
}

func newLocalWorkflow(parent context.Context, s *store.Store, build LocalEngineBuilder, credentials CredentialStore, discovery GitHubDiscoveryFactory) (*LocalWorkflow, error) {
	if parent == nil || s == nil || build == nil {
		return nil, errors.New("application context, store, and engine builder are required")
	}
	revision, err := s.CurrentConfig(parent)
	if err != nil {
		return nil, fmt.Errorf("load current configuration: %w", err)
	}
	values := revision.Config()
	capacity := values.Limits.EventCapacity
	if capacity < 1 || capacity > 4096 {
		return nil, errors.New("invalid configured event capacity")
	}
	ctx, cancel := context.WithCancel(parent)
	queueCapacity := min(capacity, 256)
	w := &LocalWorkflow{
		store: s, build: build, credentials: credentials, discovery: discovery, ctx: ctx, cancel: cancel,
		events: NewEvents(capacity), commands: make(chan Command, queueCapacity),
		progress: make(chan backup.ProgressEvent, capacity), finished: make(chan workflowCompletion, 1), discoveries: make(chan discoveryCompletion, 1),
		done:    make(chan struct{}),
		logPath: filepath.Join(s.Identity().DataDir, "operational-logs.json"),
		state:   ViewState{Connected: true, Revision: revision.ID(), Settings: settingsDraft(values), CanManageCredential: credentials != nil, Status: "Choose an existing local folder and bind it before starting a backup."},
	}
	w.state.Logs = loadOperationalLogs(w.logPath)
	if runs, queryErr := s.QueryRuns(parent, 1); queryErr == nil && len(runs) > 0 {
		w.lastRunID = runs[0].ID
	}
	if err = w.initializeExistingRoot(parent, values); err != nil {
		cancel()
		if w.coordinator != nil {
			closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = w.coordinator.Close(closeCtx)
			closeCancel()
		}
		return nil, err
	}
	w.refreshDurableView(parent)
	w.events.Publish(w.Snapshot())
	go w.loop()
	return w, nil
}

func settingsDraft(c config.Config) SettingsDraft {
	root := ""
	if c.Backup.Root != nil {
		root = *c.Backup.Root
	}
	return SettingsDraft{Root: root, Concurrency: strconv.Itoa(c.Backup.RepositoryConcurrency)}
}

func (w *LocalWorkflow) initializeExistingRoot(ctx context.Context, values config.Config) error {
	root, err := w.store.ActiveRoot(ctx)
	if errors.Is(err, store.ErrNotFound) {
		return w.detectUnownedActiveRun(ctx)
	}
	if err != nil {
		w.mutate(func(s *ViewState) {
			s.Status = "Managed backup folder is unavailable."
			s.Error = "The saved root binding could not be loaded. Managed backup is disabled."
			s.CanBindRoot = true
		})
		return nil
	}
	w.root, w.hasRoot = root, true
	if err = w.installCoordinator(ctx, root); err != nil {
		w.mutate(func(s *ViewState) {
			s.Status = "Startup checks did not complete."
			s.Error = "The managed root could not be safely opened. No local backup can start."
			s.CanBindRoot = true
		})
		return nil
	}
	if _, err = w.coordinator.ReconcileStartup(ctx); err != nil {
		w.ready = false
		w.mutate(func(s *ViewState) {
			s.Status = "Startup reconciliation is required."
			s.Error = "Managed backup is disabled until the saved root can be reconciled."
			s.CanBindRoot = false
		})
		return nil
	}
	w.ready = true
	if values.Backup.Root == nil || *values.Backup.Root != root.Path {
		w.mutate(func(s *ViewState) {
			s.Status = "Confirm the configured folder before starting a backup."
			s.Error = "The saved folder and managed-root binding differ. Use Bind backup folder to rebind explicitly."
			s.CanBindRoot = true
		})
		return nil
	}
	w.mutate(func(s *ViewState) {
		s.Status = "Ready for a local backup."
		s.Error = ""
		s.CanBindRoot = true
	})
	return nil
}

func (w *LocalWorkflow) detectUnownedActiveRun(ctx context.Context) error {
	runs, err := w.store.QueryRuns(ctx, 1000)
	if err != nil {
		return fmt.Errorf("query durable run state: %w", err)
	}
	for _, run := range runs {
		if run.State == "preparing" || run.State == "running" || run.State == "cancelling" {
			w.activeRunID = run.ID
			w.lastRunID = run.ID
			w.mutate(func(s *ViewState) {
				s.Busy = true
				s.Status = "A prior operation needs root reconciliation."
				s.Error = "The saved database has active work but no usable root binding. No new work can start."
				s.CanStartLocal = false
				s.CanCancel = false
				s.CanBindRoot = false
			})
			return nil
		}
	}
	w.mutate(func(s *ViewState) {
		s.Status = "Choose an existing local folder and bind it before starting a backup."
		s.CanBindRoot = true
	})
	return nil
}

func (w *LocalWorkflow) installCoordinator(ctx context.Context, root store.RootBinding) error {
	factory := func(revision config.Revision) (SnapshotEngine, error) {
		return w.build(root, revision, w.progress)
	}
	c, err := NewCoordinatorWithFactory(ctx, w.store, root, factory)
	if err != nil {
		return err
	}
	w.coordinator = c
	return nil
}

// Snapshot returns a deep copy of the current in-memory presentation state.
// It performs no database, filesystem, Git, or network work.
func (w *LocalWorkflow) Snapshot() ViewState {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return CopyView(w.state)
}

func (w *LocalWorkflow) Updates() <-chan ViewState { return w.events.Updates() }

// Submit is deliberately nonblocking. Unsupported commands are rejected before
// queueing, so credentials or future-operation payloads are never retained.
func (w *LocalWorkflow) Submit(command Command) error {
	inputSecret := command.Secret
	defer clear(inputSecret)
	switch command.Kind {
	case Refresh, BindRoot, SaveSettings, BackupLocal, BackupSelected, RetryGitHubTask, Cancel, SaveToken, RemoveToken, Discover, SelectRepository, SetRepositoryFilters:
	default:
		return ErrUnsupportedCommand
	}
	if command.Kind == SaveToken {
		if w.credentials == nil {
			return ErrCredentialUnavailable
		}
		if len(inputSecret) == 0 || len(inputSecret) > secrets.MaxValueBytes {
			return errors.New("credential input must be between 1 and 2560 bytes")
		}
		command.Secret = append([]byte(nil), inputSecret...)
	} else if len(inputSecret) != 0 {
		return errors.New("credential input is not valid for this command")
	}
	if command.Kind == RemoveToken && w.credentials == nil {
		return ErrCredentialUnavailable
	}
	for _, value := range []string{command.Source, command.Root, command.RepositoryID, command.RunID, command.Settings.Root, command.Settings.Concurrency} {
		if len(value) > 32768 {
			clear(command.Secret)
			return errors.New("command input exceeds its bound")
		}
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	if w.closed || w.ctx.Err() != nil {
		clear(command.Secret)
		return ErrWorkflowClosed
	}
	credentialCommand := command.Kind == SaveToken || command.Kind == RemoveToken
	if command.CreateRoot && command.Kind != BindRoot {
		return errors.New("private folder creation is only valid when binding a backup folder")
	}
	if (w.state.CredentialBusy || w.credentials != nil && w.credentials.Pending() || credentialCommand && w.state.Busy) && command.Kind != Refresh && command.Kind != Cancel {
		clear(command.Secret)
		return ErrCredentialOperationPending
	}
	if w.state.Busy && (command.Kind == Discover || command.Kind == SelectRepository || command.Kind == SetRepositoryFilters || command.Kind == BackupSelected || command.Kind == RetryGitHubTask) {
		clear(command.Secret)
		return ErrWorkflowBusy
	}
	select {
	case w.commands <- command:
		return nil
	case <-w.ctx.Done():
		clear(command.Secret)
		return ErrWorkflowClosed
	default:
		clear(command.Secret)
		return ErrCommandQueueFull
	}
}

func (w *LocalWorkflow) loop() {
	defer func() {
		for {
			select {
			case command := <-w.commands:
				clear(command.Secret)
			default:
				close(w.done)
				return
			}
		}
	}()
	for {
		select {
		case <-w.ctx.Done():
			return
		case command := <-w.commands:
			w.handle(command)
		case progress := <-w.progress:
			w.handleProgress(progress)
		case result := <-w.discoveries:
			w.handleDiscoveryCompletion(result)
		case completion := <-w.finished:
			w.handleCompletion(completion)
		}
	}
}

func (w *LocalWorkflow) handle(command Command) {
	defer clear(command.Secret)
	switch command.Kind {
	case Refresh:
		w.refreshDurableView(w.ctx)
	case SaveSettings:
		w.saveSettings(command)
	case BindRoot:
		w.bindRoot(command)
	case BackupLocal:
		w.startLocal(command)
	case BackupSelected:
		w.startSelectedBackup(command)
	case RetryGitHubTask:
		w.retryGitHubTask(command)
	case SaveToken:
		w.saveGitHubToken(command)
	case RemoveToken:
		w.removeGitHubToken(command)
	case Discover:
		w.beginDiscovery(command)
	case SelectRepository:
		w.saveRepositorySelection(command)
	case SetRepositoryFilters:
		w.saveRepositoryFilters(command)
	case Cancel:
		if !w.cancelDiscovery() {
			w.cancelRun()
		}
	default:
		w.setError("This command is not available in the current build.")
	}
}

func (w *LocalWorkflow) saveSettings(command Command) {
	current, err := w.store.CurrentConfig(w.ctx)
	if err != nil {
		w.setError("Saved settings could not be loaded.")
		return
	}
	if current.ID() != command.ExpectedRevision {
		w.setError("Settings changed elsewhere. Reload saved settings before editing again.")
		w.refreshDurableView(w.ctx)
		return
	}
	values := current.Config()
	if command.Settings.Root != "" && (values.Backup.Root == nil || command.Settings.Root != *values.Backup.Root) {
		w.setError("Backup-folder changes require the explicit Bind backup folder action.")
		return
	}
	concurrency, err := strconv.Atoi(strings.TrimSpace(command.Settings.Concurrency))
	if err != nil {
		w.setError("Repository concurrency must be a whole number from 1 to 8.")
		return
	}
	values.Backup.RepositoryConcurrency = concurrency
	if _, err = config.Normalize(values); err != nil {
		w.setError(err.Error())
		return
	}
	revision, err := w.store.SaveConfig(w.ctx, command.ExpectedRevision, values)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			w.setError("Settings changed elsewhere. Reload saved settings before editing again.")
		} else {
			w.setError(err.Error())
		}
		return
	}
	w.mutate(func(s *ViewState) {
		s.Revision = revision.ID()
		s.Settings = settingsDraft(revision.Config())
		s.Status = "Local settings saved."
		s.Error = ""
	})
	w.refreshDurableView(w.ctx)
}

func (w *LocalWorkflow) bindRoot(command Command) {
	current, err := w.store.CurrentConfig(w.ctx)
	if err != nil {
		w.setError("Saved settings could not be loaded.")
		return
	}
	if current.ID() != command.ExpectedRevision {
		w.setError("Settings changed elsewhere. Reload saved settings before rebinding the folder.")
		w.refreshDurableView(w.ctx)
		return
	}
	rootPath := command.Root
	if rootPath == "" || !filepath.IsAbs(rootPath) || filepath.Clean(rootPath) != rootPath || strings.ContainsAny(rootPath, "\x00\r\n") {
		w.setError("Backup folder must be an existing clean absolute local directory.")
		return
	}
	if command.CreateRoot {
		if err := createPrivateManagedRoot(rootPath); err != nil {
			w.setError("GitVault could not create the private backup folder. Check that its parent folder is available.")
			return
		}
	}
	values := current.Config()
	values.Backup.Root = &rootPath
	binding, revision, bindErr := BindManagedRootWithConfig(w.ctx, w.store, rootPath, current.ID(), values)
	if binding.ID == "" {
		if errors.Is(bindErr, store.ErrRootRebindUnknown) {
			w.dropCoordinator()
			w.rootRebindBlocked = true
			w.mutate(func(s *ViewState) {
				s.Status = "Root rebind outcome is uncertain."
				s.Error = "No backup can start. Restart the application to validate the saved root and marker before retrying."
				s.CanBindRoot = false
				s.CanStartLocal = false
			})
			return
		}
		w.setError(bindErr.Error())
		return
	}
	// A non-empty binding means the database commit succeeded. Preserve that
	// fact even if directory sync reports uncertainty after the commit point.
	if err = w.closeCoordinator(); err != nil {
		w.rootRebindBlocked = true
		w.ready = false
		w.mutate(func(s *ViewState) {
			s.Status = "The new folder is bound; the previous coordinator did not close cleanly."
			s.Error = "Restart before starting another backup."
			s.Revision = revision.ID()
			s.Settings = settingsDraft(revision.Config())
			s.CanStartLocal = false
			s.CanBindRoot = false
		})
		return
	}
	w.root, w.hasRoot = binding, true
	w.ready = false
	if err = w.installCoordinator(w.ctx, binding); err == nil {
		_, err = w.coordinator.ReconcileStartup(w.ctx)
	}
	if err == nil {
		w.ready = true
	}
	stateErr := ""
	status := "Backup folder bound and startup checks passed."
	if err != nil {
		status = "Backup folder is bound, but startup reconciliation did not pass."
		stateErr = "No local backup can start until the managed root is reconciled."
	}
	if bindErr != nil {
		w.ready = false
		w.rootRebindBlocked = true
		status = "Backup folder and settings are committed; directory durability is uncertain."
		stateErr = "The folder is visible, but its directory sync could not be confirmed. Do not move or remove it."
	}
	w.mutate(func(s *ViewState) {
		s.Revision = revision.ID()
		s.Settings = settingsDraft(revision.Config())
		s.Status = status
		s.Error = stateErr
		s.Notice = "A root rebind does not move or delete snapshots in the previous folder."
		s.CanBindRoot = err == nil && bindErr == nil
	})
	w.refreshDurableView(w.ctx)
}

func (w *LocalWorkflow) closeCoordinator() error {
	if w.coordinator == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := w.coordinator.Close(ctx)
	w.coordinator = nil
	return err
}

func (w *LocalWorkflow) dropCoordinator() {
	_ = w.closeCoordinator()
	w.root = store.RootBinding{}
	w.hasRoot = false
	w.ready = false
}

func (w *LocalWorkflow) startLocal(command Command) {
	if w.coordinator == nil || !w.hasRoot || !w.ready {
		w.setError("The managed folder is not ready. Bind it and complete startup checks before starting a backup.")
		return
	}
	if !filepath.IsAbs(command.Source) || filepath.Clean(command.Source) != command.Source || strings.ContainsAny(command.Source, "\x00\r\n") {
		w.setError("Local repository path must be a clean absolute directory path.")
		return
	}
	run, err := w.coordinator.StartLocal(w.ctx, LocalBackupRequest{Trigger: "manual", SourcePath: command.Source})
	if err != nil {
		if errors.Is(err, ErrStartupReconciliationRequired) {
			w.ready = false
			w.setError("Startup reconciliation must pass before a local backup can start.")
		} else if _, ok := err.(*store.BusyError); ok {
			w.setError("Another operation is already active. No additional work was queued.")
		} else {
			w.setError(err.Error())
		}
		w.refreshDurableView(w.ctx)
		return
	}
	w.activeRunID = run.ID
	w.lastRunID = run.ID
	w.activeRepoID = run.Intent.RepositoryIDs[0]
	w.addLog("info", "Local backup run started.")
	w.mutate(func(s *ViewState) {
		s.Busy = true
		s.CanCancel = true
		s.CanStartLocal = false
		s.CanStartSelected = false
		s.CanBindRoot = false
		s.Status = "Local backup is running."
		s.Error = ""
		s.Tasks = []TaskView{{Repository: "Local repository", Stage: "preparing", Detail: "Durable run intent recorded."}}
	})
	coordinator := w.coordinator
	go func(id string) {
		completion := workflowCompletion{}
		completion.run, completion.err = coordinator.Wait(context.Background(), id)
		select {
		case w.finished <- completion:
		case <-w.ctx.Done():
		}
	}(run.ID)
}

func (w *LocalWorkflow) cancelRun() {
	if w.coordinator == nil || w.activeRunID == "" {
		w.setError("There is no cancellable backup run owned by this window.")
		return
	}
	if err := w.coordinator.Cancel(w.ctx, w.activeRunID); err != nil {
		w.setError("Cancellation could not be recorded. The active operation remains visible.")
		return
	}
	w.mutate(func(s *ViewState) {
		s.Status = "Cancellation requested. Git processes will stop before ownership is released."
		s.Error = ""
	})
}

func (w *LocalWorkflow) handleProgress(progress backup.ProgressEvent) {
	if w.activeRunID == "" {
		return
	}
	w.mutate(func(s *ViewState) {
		if len(s.Logs) == 0 || s.Logs[len(s.Logs)-1].Message != "Backup stage: "+progress.Stage+"." {
			s.Logs = append(s.Logs, LogEntry{At: time.Now().UTC(), Level: "info", Message: "Backup stage: " + progress.Stage + "."})
			if len(s.Logs) > maxViewLogs {
				s.Logs = append([]LogEntry(nil), s.Logs[len(s.Logs)-maxViewLogs:]...)
			}
		}
		s.Status = "Backup: " + progress.Stage
		matched := false
		for i := range s.Tasks {
			if s.Tasks[i].RepositoryID == progress.RepositoryID || progress.RepositoryID == w.activeRepoID && s.Tasks[i].Repository == "Local repository" {
				s.Tasks[i].Stage = progress.Stage
				s.Tasks[i].Detail = "Working in the background."
				matched = true
			}
		}
		if len(s.Tasks) == 0 {
			s.Tasks = []TaskView{{Repository: "Local repository", Stage: progress.Stage, Detail: "Working in the background."}}
		}
		if !matched && progress.RepositoryID != "" {
			return
		}
	})
}

func (w *LocalWorkflow) handleCompletion(completion workflowCompletion) {
	w.activeRunID = ""
	w.activeRepoID = ""
	if completion.err != nil {
		w.addLog("error", "Run could not be finalized; reconciliation is required. No automatic retry was started.")
		w.ready = false
		w.mutate(func(s *ViewState) {
			s.Busy = true // terminal facts were not confirmed; fail admission closed.
			s.CanCancel = false
			s.CanStartLocal = false
			s.CanBindRoot = false
			s.Status = "The terminal result requires reconciliation."
			s.Error = "Do not retry yet. Restart the application to reconcile the original run identity."
		})
		return
	}
	w.lastRunID = completion.run.ID
	if completion.run.State == "failed" || completion.run.State == "partial" {
		w.addLog("error", "Backup run finished with failures. Open Backups for per-repository outcomes; no automatic retry was started.")
	} else {
		w.addLog("info", "Backup run finished: "+completion.run.State+".")
	}
	if completion.run.State == "interrupted" {
		w.ready = false
	}
	backupLabel := "Local backup"
	if completion.run.Intent.Kind == "github_backup" {
		backupLabel = "GitHub backup"
	}
	w.mutate(func(s *ViewState) {
		s.Busy = false
		s.CanCancel = false
		s.CanBindRoot = true
		s.Error = ""
		switch completion.run.State {
		case "succeeded":
			s.Status = backupLabel + " completed and its terminal result is durable."
		case "cancelled":
			s.Status = backupLabel + " cancelled; any published snapshot remains retained."
		case "interrupted":
			s.Status = backupLabel + " was interrupted and needs reconciliation."
			s.Error = "No retry was started. Inspect the original run during startup reconciliation."
		case "partial":
			s.Status = "The run completed with partial results."
		default:
			s.Status = backupLabel + " failed."
		}
	})
	w.refreshDurableView(w.ctx)
}

func (w *LocalWorkflow) refreshDurableView(ctx context.Context) {
	if ctx == nil || ctx.Err() != nil {
		return
	}
	current, err := w.store.CurrentConfig(ctx)
	if err != nil {
		w.mutate(func(s *ViewState) { s.Error = "Saved settings could not be queried." })
		return
	}
	values := current.Config()
	w.mutate(func(s *ViewState) {
		s.Revision = current.ID()
		s.Settings = settingsDraft(values)
		s.Filters = values.GitHub.Filters
		s.CredentialConfigured = values.GitHub.Credential != nil
		s.SelectedRepositoryCount = len(values.Selection.RepositoryIDs)
		s.CredentialBusy = w.credentials != nil && w.credentials.Pending()
		s.Connected = true
	})
	account, repositories, inventoryErr := w.store.LatestGitHubInventory(ctx)
	if inventoryErr == nil {
		selected := values.Selection.RepositoryIDs
		views := repositoryView(account, repositories, values.GitHub.Filters, selected)
		w.mutate(func(s *ViewState) {
			s.Account = account.Login
			s.Repositories = views
			s.InventoryComplete = account.Complete
		})
	} else if errors.Is(inventoryErr, store.ErrNotFound) {
		w.mutate(func(s *ViewState) {
			s.Account = ""
			s.Repositories = []RepositoryView{}
			s.InventoryComplete = false
		})
	} else {
		w.mutate(func(s *ViewState) {
			s.Error = "Saved GitHub inventory could not be loaded. Repository selection was preserved."
		})
	}

	snapshots := []SnapshotView{}
	if w.hasRoot {
		rows, listErr := w.store.ListSnapshots(ctx, "", 200)
		if listErr == nil {
			pins, _ := w.store.ListPins(ctx, 10000)
			pinned := make(map[string]bool, len(pins))
			for _, pin := range pins {
				pinned[pin.SnapshotID] = true
			}
			for _, row := range rows {
				if row.RootID != w.root.ID {
					continue
				}
				path := filepath.Join(w.root.Path, filepath.FromSlash(row.RelativePath))
				name := row.RepositoryID
				if row.Provider == "github" {
					for _, repository := range repositories {
						if repository.ID == row.RepositoryID {
							name = repository.FullName
							break
						}
					}
				}
				snapshots = append(snapshots, SnapshotView{ID: row.ID, Repository: name, Path: path, Verification: verificationLabel(row.Verification), CloudStatus: "not requested", CreatedAt: row.CreatedAt, LocalReady: row.State == "ready", Pinned: pinned[row.ID]})
			}
		}
	}
	w.mutate(func(s *ViewState) { s.Snapshots = snapshots })

	if w.lastRunID != "" {
		tasks, listErr := w.store.ListTasks(ctx, w.lastRunID, 100)
		if listErr == nil {
			run, runErr := w.store.GetRun(ctx, w.lastRunID)
			names := make(map[string]string, len(repositories))
			available := make(map[string]bool, len(repositories))
			for _, repository := range repositories {
				names[repository.ID] = repository.FullName
				available[repository.ID] = repository.Available
			}
			selected := make(map[string]bool, len(values.Selection.RepositoryIDs))
			for _, id := range values.Selection.RepositoryIDs {
				selected[id] = true
			}
			views := make([]TaskView, 0, len(tasks))
			for _, task := range tasks {
				detail := task.Outcome
				if task.ErrorCode != "" {
					detail = task.ErrorCode
				}
				repository, repositoryID := "Local repository", ""
				canRetry := false
				if task.Provider == "github" {
					repository, repositoryID = names[task.RepositoryID], "github:"+task.RepositoryID
					if repository == "" {
						repository = task.RepositoryID
					}
					canRetry = runErr == nil && !w.Snapshot().Busy && retryableGitHubTask(run.State, task) && selected[repositoryID] && available[task.RepositoryID]
				}
				viewDetail := detail
				if task.ErrorCode != "" {
					viewDetail = safeFailureDescription(detail)
				}
				views = append(views, TaskView{Repository: repository, RepositoryID: repositoryID, RunID: w.lastRunID, Stage: task.Stage, Detail: viewDetail, CanRetry: canRetry})
				if task.Outcome == "failed" {
					w.addLog("error", "Repository task "+repository+" failed at "+task.Stage+" ("+safeFailureDescription(detail)+").")
				}
			}
			if len(views) > 0 {
				w.mutate(func(s *ViewState) { s.Tasks = views })
			}
		}
	}
	w.recomputeControls()
}

func safeFailureDescription(code string) string {
	if description := classifiedFailureDescription(code); description != "" {
		return description
	}
	switch code {
	case "engine_failed_git_unavailable":
		return "Git could not be started or was unavailable"
	case "engine_failed_git_unsupported":
		return "Git process ownership or operation is unsupported"
	case "engine_failed_git_output_limit":
		return "Git output exceeded the configured safety limit"
	case "engine_failed_unsafe_repository":
		return "repository failed GitVault's safety checks"
	case "engine_failed_git_transport_denied":
		return "Git transport was denied by policy"
	case "operation_timeout":
		return "operation timed out"
	case "engine_failed":
		return "no specific diagnostic was retained; open Logs and explicitly retry to collect a new result"
	}
	if strings.HasPrefix(code, "engine_failed_git_exit_") {
		return "controlled Git command exited with " + strings.TrimPrefix(code, "engine_failed_git_exit_") + " (raw output suppressed)"
	}
	return code
}

func retryableGitHubTask(runState string, task store.Task) bool {
	if (runState != "failed" && runState != "partial" && runState != "cancelled") || task.Provider != "github" || task.Stage != "failed" || task.Outcome != "failed" || task.PublicationState != string(backup.NotPublished) || task.ArtifactID != "" || task.ArtifactPath != "" {
		return false
	}
	return strings.HasPrefix(task.ErrorCode, "engine_failed") || task.ErrorCode == "operation_timeout"
}

func verificationLabel(v store.Verification) string {
	checks := []string{v.Manifest, v.Hash, v.IndependentRefs, v.Head, v.ObjectIntegrity, v.OfflineRestore}
	for _, check := range checks {
		if check != "passed" {
			return "verification incomplete"
		}
	}
	return "manifest, hash, refs, HEAD, integrity, and offline restore passed"
}

func (w *LocalWorkflow) recomputeControls() {
	current, err := w.store.CurrentConfig(w.ctx)
	if err != nil {
		return
	}
	w.mu.Lock()
	state := CopyView(w.state)
	busy := state.Busy
	bound := w.hasRoot && w.coordinator != nil && w.ready && w.coordinator.Ready()
	configured := current.Config().Backup.Root != nil && w.hasRoot && *current.Config().Backup.Root == w.root.Path
	state.CanStartLocal = !busy && bound && configured
	values := current.Config()
	state.CanStartSelected = !busy && bound && configured && w.discovery != nil && w.credentials != nil && values.GitHub.Credential != nil && len(values.Selection.RepositoryIDs) > 0
	state.CanBindRoot = !busy && !w.closed && !w.rootRebindBlocked
	state.CanCancel = w.activeRunID != "" || w.discoveryCancel != nil
	state.CredentialBusy = w.credentials != nil && w.credentials.Pending()
	state.CanManageCredential = w.credentials != nil && !busy && !w.closed && !state.CredentialBusy && !w.credentials.Pending()
	state.CanDiscover = w.discovery != nil && w.credentials != nil && current.Config().GitHub.Credential != nil && !busy && !w.closed && !state.CredentialBusy && !w.credentials.Pending()
	w.state = CopyView(state)
	w.mu.Unlock()
	w.events.Publish(state)
}

func (w *LocalWorkflow) setError(message string) {
	w.mutate(func(s *ViewState) {
		s.Error = message
		if s.Status == "" || strings.HasPrefix(s.Status, "Ready") {
			s.Status = "Action requires attention."
		}
	})
}

func (w *LocalWorkflow) mutate(change func(*ViewState)) {
	w.mu.Lock()
	state := CopyView(w.state)
	change(&state)
	w.state = CopyView(state)
	w.mu.Unlock()
	w.events.Publish(state)
}

const maxViewLogs = 500

func (w *LocalWorkflow) addLog(level, message string) {
	w.logMu.Lock()
	defer w.logMu.Unlock()
	var added LogEntry
	w.mutate(func(s *ViewState) {
		if len(s.Logs) > 0 && s.Logs[len(s.Logs)-1].Level == level && s.Logs[len(s.Logs)-1].Message == message {
			return
		}
		added = LogEntry{At: time.Now().UTC(), Level: level, Message: message}
		s.Logs = append(s.Logs, added)
		if len(s.Logs) > maxViewLogs {
			s.Logs = append([]LogEntry(nil), s.Logs[len(s.Logs)-maxViewLogs:]...)
		}
	})
	if added.Message == "" {
		return
	}
	state := w.Snapshot()
	body, err := json.Marshal(state.Logs)
	if err != nil {
		return
	}
	tmp := w.logPath + ".tmp"
	if os.WriteFile(tmp, body, 0600) == nil {
		_ = os.Rename(tmp, w.logPath)
		_ = os.Chmod(w.logPath, 0600)
	}
}

func loadOperationalLogs(path string) []LogEntry {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var logs []LogEntry
	if json.Unmarshal(body, &logs) != nil {
		return nil
	}
	if len(logs) > maxViewLogs {
		logs = logs[len(logs)-maxViewLogs:]
	}
	return logs
}

// Close stops command admission, cancels active work, waits for the command
// worker to stop, then waits for coordinator-owned subprocesses and terminal
// persistence. It never waits on a UI callback.
func (w *LocalWorkflow) Close(ctx context.Context) error {
	w.mu.Lock()
	if !w.closed {
		w.closed = true
		w.cancel()
	}
	w.mu.Unlock()
	// Revoke volatile credential leases as soon as admission stops. A timeout
	// while waiting for the worker/coordinator must not keep unattended access.
	if w.credentials != nil {
		w.credentials.CloseLeases()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-w.done:
	}
	workersDone := make(chan struct{})
	go func() {
		w.discoveryWG.Wait()
		close(workersDone)
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-workersDone:
	}
	var closeErr error
	if w.coordinator != nil {
		closeErr = w.coordinator.Close(ctx)
	}
	if w.credentials != nil {
		if w.credentials.Pending() {
			closeErr = errors.Join(closeErr, ErrCredentialOperationPending)
		}
	}
	return closeErr
}
