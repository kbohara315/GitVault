package app

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"

	"gitvault/internal/backup"
	"gitvault/internal/github"
	"gitvault/internal/secrets"
	"gitvault/internal/store"
)

type GitHubAcquisitionEngine interface {
	AcquireGitHub(context.Context, string, string, []byte) (string, error)
}

func (w *LocalWorkflow) startSelectedBackup(command Command) {
	if w.coordinator == nil || !w.hasRoot || !w.ready || w.credentials == nil || w.discovery == nil {
		w.setError("GitHub backups require a ready managed folder, saved credential, and discovery service.")
		return
	}
	current, err := w.store.CurrentConfig(w.ctx)
	if err != nil {
		w.setError("Saved settings could not be loaded. No GitHub backup was admitted.")
		return
	}
	if current.ID() != command.ExpectedRevision {
		w.setError("Settings changed elsewhere. Reload the selected repositories before starting a backup.")
		w.refreshDurableView(w.ctx)
		return
	}
	values := current.Config()
	if values.GitHub.Credential == nil || len(values.Selection.RepositoryIDs) == 0 {
		w.setError("Save a GitHub credential and explicitly select at least one available repository.")
		return
	}
	account, _, err := w.store.LatestGitHubInventory(w.ctx)
	if err != nil {
		w.setError("A saved GitHub account inventory is required before a selected backup can start.")
		return
	}
	run, err := w.coordinator.StartGitHub(w.ctx, GitHubBackupRequest{Trigger: "manual", AccountID: account.ID, ExpectedRevision: current.ID(), RepositoryIDs: append([]string(nil), values.Selection.RepositoryIDs...)}, w.executeGitHubRun)
	if err != nil {
		switch {
		case errors.Is(err, ErrStartupReconciliationRequired):
			w.ready = false
			w.setError("Startup reconciliation must pass before a GitHub backup can start.")
		case errors.Is(err, store.ErrConflict):
			w.setError("The selected repositories or saved account changed. Reload the inventory before retrying.")
		case isStoreBusy(err):
			w.setError("Another operation is already active. No additional work was queued.")
		default:
			w.setError("The selected GitHub backup could not be durably admitted. No transfer started.")
		}
		w.refreshDurableView(w.ctx)
		return
	}
	w.launchGitHubRun(run)
}

func (w *LocalWorkflow) retryGitHubTask(command Command) {
	if w.coordinator == nil || !w.hasRoot || !w.ready || w.credentials == nil || w.discovery == nil || w.Snapshot().Busy {
		w.setError("GitHub task retry is unavailable while the workflow is busy or not ready.")
		return
	}
	if command.RunID == "" || command.RunID != w.lastRunID || !stableRepositoryID(command.RepositoryID) {
		w.setError("The selected task is no longer eligible for retry. Refresh the saved task result.")
		return
	}
	current, err := w.store.CurrentConfig(w.ctx)
	if err != nil || current.ID() != command.ExpectedRevision {
		w.setError("Settings changed elsewhere. Reload the selected repositories before retrying.")
		w.refreshDurableView(w.ctx)
		return
	}
	values := current.Config()
	if values.GitHub.Credential == nil || !containsSelectionID(values.Selection.RepositoryIDs, command.RepositoryID) {
		w.setError("The repository must remain explicitly selected and a GitHub credential must be configured before retry.")
		return
	}
	previous, err := w.store.GetRun(w.ctx, command.RunID)
	if err != nil {
		w.setError("The previous durable run could not be loaded. No retry started.")
		return
	}
	tasks, err := w.store.ListTasks(w.ctx, previous.ID, 1000)
	if err != nil {
		w.setError("The previous durable task results could not be loaded. No retry started.")
		return
	}
	repositoryID := strings.TrimPrefix(command.RepositoryID, "github:")
	eligible := false
	for _, task := range tasks {
		if task.RepositoryID == repositoryID && retryableGitHubTask(previous.State, task) {
			eligible = true
			break
		}
	}
	if !eligible {
		w.setError("This task has a known artifact, uncertain publication, or non-retryable outcome. No retry started.")
		return
	}
	account, _, err := w.store.LatestGitHubInventory(w.ctx)
	if err != nil || account.ID != previous.Intent.AccountID {
		w.setError("The saved GitHub account changed. Reconnect and rediscover before retrying this task.")
		return
	}
	run, err := w.coordinator.StartGitHub(w.ctx, GitHubBackupRequest{Trigger: "retry", AccountID: account.ID, ExpectedRevision: current.ID(), RepositoryIDs: []string{command.RepositoryID}}, w.executeGitHubRun)
	if err != nil {
		w.setError("The retry could not be durably admitted. No transfer started.")
		w.refreshDurableView(w.ctx)
		return
	}
	w.launchGitHubRun(run)
}

func containsSelectionID(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func (w *LocalWorkflow) launchGitHubRun(run store.Run) {
	w.activeRunID = run.ID
	w.lastRunID = run.ID
	w.mutate(func(s *ViewState) {
		s.Busy = true
		s.CanCancel = true
		s.CanStartLocal = false
		s.CanStartSelected = false
		s.CanBindRoot = false
		s.Status = "Selected GitHub backup is running. Its intent and snapshot identities are durable."
		s.Error = ""
		s.Tasks = selectedTaskViews(run.Intent.RepositoryIDs, "pending", "Durable intent recorded; waiting for credential and fresh inventory.")
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

func isStoreBusy(err error) bool {
	var busy *store.BusyError
	return errors.As(err, &busy)
}

func (w *LocalWorkflow) executeGitHubRun(ctx context.Context, run store.Run, snapshot SnapshotEngine, tasks []store.Task, setStage func(store.Task, string) error, complete func(store.Task, backup.Result, error) error) error {
	if w.credentials == nil || w.discovery == nil || run.Config.ID() < 1 || run.Intent.AccountID == "" {
		return ErrCredentialUnavailable
	}
	values := run.Config.Config()
	if values.GitHub.Credential == nil {
		return ErrCredentialUnavailable
	}
	credential := *values.GitHub.Credential
	handle := secrets.Handle{Reference: credential.Reference, Generation: uint32(credential.Generation)}
	token, err := w.credentials.ReadInteractive(ctx, handle)
	if err != nil {
		return err
	}
	defer clear(token)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	discoverer, err := w.discovery(run.Config)
	if err != nil || discoverer == nil {
		return errors.New("GitHub discovery client unavailable")
	}
	result, discoveryErr := discoverer.DiscoverBytes(ctx, token)
	if result.User.ID <= 0 || strconv.FormatInt(result.User.ID, 10) != run.Intent.AccountID {
		return errors.New("authenticated GitHub account does not match the admitted account")
	}
	completeInventory := result.Complete && discoveryErr == nil
	if err = w.store.ReconcileGitHubInventory(ctx, inventoryAccount(result.User), inventoryRepositories(result.Repositories), completeInventory); err != nil {
		return errors.New("fresh GitHub inventory could not be saved")
	}
	byID := make(map[string]github.Repository, len(result.Repositories))
	for _, repository := range result.Repositories {
		if repository.ID <= 0 {
			return errors.New("fresh GitHub inventory contains an invalid stable repository ID")
		}
		id := strconv.FormatInt(repository.ID, 10)
		if _, exists := byID[id]; exists {
			return errors.New("fresh GitHub inventory contains duplicate repository identities")
		}
		byID[id] = repository
	}

	acquirer, ok := snapshot.(GitHubAcquisitionEngine)
	if !ok {
		return errors.New("snapshot engine does not provide controlled GitHub acquisition")
	}
	concurrency := values.Backup.RepositoryConcurrency
	if concurrency < 1 || concurrency > 8 {
		return errors.New("captured repository concurrency is invalid")
	}
	jobs := make(chan store.Task, len(tasks))
	for _, task := range tasks {
		jobs <- task
	}
	close(jobs)
	var workerWG sync.WaitGroup
	var errMu sync.Mutex
	var taskErrors error
	workers := min(concurrency, len(tasks))
	for range workers {
		workerWG.Add(1)
		go func() {
			defer workerWG.Done()
			for task := range jobs {
				if ctx.Err() != nil {
					if recordErr := complete(task, failedBackupResult(ctx.Err()), ctx.Err()); recordErr != nil {
						errMu.Lock()
						taskErrors = errors.Join(taskErrors, recordErr)
						errMu.Unlock()
					}
					continue
				}
				repository, found := byID[task.RepositoryID]
				if !found {
					cause := errors.New("selected repository is absent from the fresh inventory")
					if !completeInventory {
						cause = errors.New("fresh inventory is incomplete for this selected repository")
					}
					if recordErr := complete(task, failedBackupResult(cause), cause); recordErr != nil {
						errMu.Lock()
						taskErrors = errors.Join(taskErrors, recordErr)
						errMu.Unlock()
					}
					continue
				}
				if stageErr := setStage(task, "fetching"); stageErr != nil {
					if recordErr := complete(task, failedBackupResult(stageErr), stageErr); recordErr != nil {
						errMu.Lock()
						taskErrors = errors.Join(taskErrors, recordErr)
						errMu.Unlock()
					}
					continue
				}
				mirror, acquireErr := acquirer.AcquireGitHub(ctx, task.RepositoryID, repository.CloneURL, token)
				if acquireErr != nil {
					if recordErr := complete(task, failedBackupResult(acquireErr), acquireErr); recordErr != nil {
						errMu.Lock()
						taskErrors = errors.Join(taskErrors, recordErr)
						errMu.Unlock()
					}
					continue
				}
				if stageErr := setStage(task, "bundling"); stageErr != nil {
					if recordErr := complete(task, failedBackupResult(stageErr), stageErr); recordErr != nil {
						errMu.Lock()
						taskErrors = errors.Join(taskErrors, recordErr)
						errMu.Unlock()
					}
					continue
				}
				snapshotResult, createErr := snapshot.Create(ctx, backup.Request{Source: mirror, Repository: backup.RepositoryIdentity{Provider: "github", ID: task.RepositoryID, DisplayName: repository.FullName}, ApplicationVersion: "0.1.0", ManagedGitHubMirror: true, SnapshotID: task.OperationID})
				if recordErr := complete(task, snapshotResult, createErr); recordErr != nil {
					errMu.Lock()
					taskErrors = errors.Join(taskErrors, recordErr)
					errMu.Unlock()
				}
			}
		}()
	}
	workerWG.Wait()
	errMu.Lock()
	defer errMu.Unlock()
	return taskErrors
}

func failedBackupResult(err error) backup.Result {
	outcome := backup.OutcomeFailed
	if errors.Is(err, context.Canceled) {
		outcome = backup.OutcomeCancelled
	}
	result, _ := backup.NewResult(backup.ResultFacts{Outcome: outcome, Publication: backup.NotPublished})
	return result
}

func inventoryAccount(user github.User) store.GitHubAccount {
	return store.GitHubAccount{ID: strconv.FormatInt(user.ID, 10), Login: user.Login, Type: user.Type}
}

func inventoryRepositories(repositories []github.Repository) []store.GitHubRepository {
	rows := make([]store.GitHubRepository, 0, len(repositories))
	for _, repository := range repositories {
		rows = append(rows, store.GitHubRepository{
			ID: strconv.FormatInt(repository.ID, 10), OwnerID: strconv.FormatInt(repository.Owner.ID, 10),
			OwnerLogin: repository.Owner.Login, OwnerType: repository.Owner.Type,
			Name: repository.Name, FullName: repository.FullName, CloneURL: repository.CloneURL,
			Description: repository.Description, Private: repository.Private, Fork: repository.Fork,
			Archived: repository.Archived, Available: true,
		})
	}
	return rows
}

func selectedTaskViews(repositoryIDs []string, stage, detail string) []TaskView {
	views := make([]TaskView, 0, len(repositoryIDs))
	for _, id := range repositoryIDs {
		views = append(views, TaskView{Repository: strings.TrimPrefix(id, "github:"), Stage: stage, Detail: detail})
	}
	return views
}
