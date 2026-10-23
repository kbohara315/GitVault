package app

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"

	"gitvault/internal/config"
	"gitvault/internal/github"
	"gitvault/internal/secrets"
	"gitvault/internal/store"
)

// GitHubDiscoverer accepts a short-lived byte credential and returns metadata
// only. It never authorizes a transfer or mutates selection.
type GitHubDiscoverer interface {
	DiscoverBytes(context.Context, []byte) (github.Result, error)
}

// GitHubDiscoveryFactory builds an HTTP client from one captured settings
// revision. The native UI and the worker never share a mutable client policy.
type GitHubDiscoveryFactory func(config.Revision) (GitHubDiscoverer, error)

type discoveryCompletion struct {
	result github.Result
	err    error
}

// NewLocalWorkflowWithDiscovery composes discovery without changing the
// existing no-network constructor used by local-only builds and tests.
func NewLocalWorkflowWithDiscovery(parent context.Context, s *store.Store, build LocalEngineBuilder, credentials CredentialStore, discovery GitHubDiscoveryFactory) (*LocalWorkflow, error) {
	return newLocalWorkflow(parent, s, build, credentials, discovery)
}

func (w *LocalWorkflow) beginDiscovery(command Command) {
	if w.discovery == nil || w.credentials == nil {
		w.setError("GitHub repository discovery is unavailable in this build.")
		return
	}
	if w.activeRunID != "" || w.state.Busy || w.credentials.Pending() {
		w.setError("Wait for the active operation or secure-store prompt to finish before discovering repositories.")
		return
	}
	current, err := w.store.CurrentConfig(w.ctx)
	if err != nil {
		w.setError("Saved settings could not be loaded. Repository discovery did not start.")
		return
	}
	if current.ID() != command.ExpectedRevision {
		w.setError("Settings changed elsewhere. Reload saved settings before discovering repositories.")
		w.refreshDurableView(w.ctx)
		return
	}
	values := current.Config()
	if values.GitHub.Credential == nil {
		w.setError("Save a GitHub credential in OS secure storage before discovering repositories.")
		return
	}
	client, err := w.discovery(current)
	if err != nil || client == nil {
		w.setError("A bounded GitHub discovery client could not be prepared. No request was sent.")
		return
	}
	configured := *values.GitHub.Credential
	handle := secrets.Handle{Reference: configured.Reference, Generation: uint32(configured.Generation)}
	ctx, cancel := context.WithCancel(w.ctx)
	w.discoveryCancel = cancel
	w.discoveryCancelRequested = false
	w.discoveryWG.Add(1)
	w.mutate(func(s *ViewState) {
		s.Busy = true
		s.DiscoveryBusy = true
		s.CanCancel = true
		s.CanDiscover = false
		s.CanStartLocal = false
		s.CanBindRoot = false
		s.CanManageCredential = false
		s.CredentialBusy = true
		s.Status = "Checking GitHub identity and discovering accessible repositories…"
		s.Error = ""
	})
	go func() {
		defer w.discoveryWG.Done()
		defer cancel()
		result := github.Result{}
		token, readErr := w.credentials.ReadInteractive(ctx, handle)
		if readErr != nil {
			select {
			case w.discoveries <- discoveryCompletion{err: readErr}:
			case <-w.ctx.Done():
			}
			return
		}
		defer clear(token)
		if ctx.Err() != nil {
			select {
			case w.discoveries <- discoveryCompletion{err: ctx.Err()}:
			case <-w.ctx.Done():
			}
			return
		}
		result, err = client.DiscoverBytes(ctx, token)
		select {
		case w.discoveries <- discoveryCompletion{result: result, err: err}:
		case <-w.ctx.Done():
		}
	}()
}

func (w *LocalWorkflow) cancelDiscovery() bool {
	if w.discoveryCancel == nil {
		return false
	}
	w.discoveryCancel()
	w.discoveryCancelRequested = true
	w.mutate(func(s *ViewState) {
		s.Status = "Repository discovery cancellation requested. The HTTP request or rate-limit wait will stop."
		s.Error = ""
	})
	return true
}

func (w *LocalWorkflow) handleDiscoveryCompletion(completion discoveryCompletion) {
	w.discoveryCancel = nil
	cancelRequested := w.discoveryCancelRequested
	w.discoveryCancelRequested = false
	credentialPending := w.credentials != nil && w.credentials.Pending()
	user := completion.result.User
	if user.ID > 0 {
		account := store.GitHubAccount{ID: strconv.FormatInt(user.ID, 10), Login: user.Login, Type: user.Type}
		repositories := make([]store.GitHubRepository, 0, len(completion.result.Repositories))
		for _, repository := range completion.result.Repositories {
			repositories = append(repositories, store.GitHubRepository{
				ID: strconv.FormatInt(repository.ID, 10), OwnerID: strconv.FormatInt(repository.Owner.ID, 10),
				OwnerLogin: repository.Owner.Login, OwnerType: repository.Owner.Type,
				Name: repository.Name, FullName: repository.FullName, CloneURL: repository.CloneURL,
				Description: repository.Description, Private: repository.Private, Fork: repository.Fork,
				Archived: repository.Archived, Available: true,
			})
		}
		complete := completion.result.Complete && completion.err == nil
		if err := w.store.ReconcileGitHubInventory(w.ctx, account, repositories, complete); err != nil {
			w.finishDiscovery("Repository inventory could not be saved.", "The last saved inventory and all selected repository IDs were left unchanged.")
			return
		}
	}

	status, message := discoveryOutcome(completion.err, completion.result.Complete, cancelRequested)
	w.mutate(func(s *ViewState) {
		s.Busy = false
		s.DiscoveryBusy = false
		s.CredentialBusy = credentialPending
		s.CanCancel = false
		s.Status = status
		s.Error = message
		if credentialPending {
			s.Error = "The OS credential prompt may still be pending. GitVault will not use a late result; wait for the prompt to finish, then check its status."
		}
	})
	w.refreshDurableView(w.ctx)
}

func (w *LocalWorkflow) finishDiscovery(status, message string) {
	w.mutate(func(s *ViewState) {
		s.Busy = false
		s.DiscoveryBusy = false
		s.CredentialBusy = w.credentials != nil && w.credentials.Pending()
		s.CanCancel = false
		s.Status = status
		s.Error = message
	})
	w.recomputeControls()
}

func discoveryOutcome(err error, complete, cancelRequested bool) (string, string) {
	if err == nil && complete {
		return "Repository discovery completed. Newly found repositories remain unselected.", ""
	}
	if errors.Is(err, context.Canceled) && cancelRequested {
		return "Repository discovery cancelled. Saved selections and unseen inventory were preserved.", ""
	}
	if err == nil {
		return "Repository discovery is incomplete.", "GitHub did not confirm a complete inventory. Saved selections were preserved."
	}
	message := "GitHub discovery did not complete. Saved selections were preserved."
	var apiErr *github.Error
	if errors.As(err, &apiErr) {
		switch apiErr.Kind {
		case github.Authentication:
			message = "GitHub rejected the configured credential. Check token validity and retry."
		case github.Permission:
			message = "GitHub denied repository access. Organization policy or token permissions may restrict discovery."
		case github.NotFound:
			message = "GitHub could not find the requested account or repository inventory."
		case github.RateLimit:
			message = "GitHub rate-limit wait exceeded the configured budget. Retry later or adjust the wait limit."
		case github.Transport:
			message = "GitHub could not be reached within the bounded request and retry limits."
		case github.Bounds:
			message = "GitHub discovery exceeded a configured resource bound. No partial result was treated as complete."
		default:
			message = "GitHub returned invalid or unsupported discovery data. Saved selections were preserved."
		}
	} else if errors.Is(err, context.DeadlineExceeded) {
		message = "GitHub discovery exceeded its configured request timeout. Saved selections were preserved."
	}
	return "Repository discovery is partial or unavailable.", message
}

func (w *LocalWorkflow) saveRepositorySelection(command Command) {
	if w.state.Busy {
		w.setError("Wait for repository discovery or another operation to finish before changing selection.")
		return
	}
	if !stableRepositoryID(command.RepositoryID) {
		w.setError("Choose a repository using its stable GitHub ID. No selection was changed.")
		return
	}
	current, err := w.store.CurrentConfig(w.ctx)
	if err != nil {
		w.setError("Saved settings could not be loaded. Repository selection was not changed.")
		return
	}
	if current.ID() != command.ExpectedRevision {
		w.setError("Settings changed elsewhere. Reload repository selection before editing again.")
		w.refreshDurableView(w.ctx)
		return
	}
	known, err := w.store.HasGitHubRepository(w.ctx, command.RepositoryID)
	if err != nil || !known {
		w.setError("Choose a repository from the saved GitHub inventory. No selection was changed.")
		return
	}
	values := current.Config()
	ids := values.Selection.RepositoryIDs
	found := false
	updated := make([]string, 0, len(ids)+1)
	for _, id := range ids {
		if id == command.RepositoryID {
			found = true
			if !command.Selected {
				continue
			}
		}
		updated = append(updated, id)
	}
	if command.Selected && !found {
		updated = append(updated, command.RepositoryID)
	}
	sort.Strings(updated)
	if sameStrings(ids, updated) {
		return
	}
	values.Selection.RepositoryIDs = updated
	revision, err := w.store.SaveConfig(w.ctx, current.ID(), values)
	if err != nil {
		w.setError("Repository selection could not be saved at the displayed settings revision.")
		w.refreshDurableView(w.ctx)
		return
	}
	w.mutate(func(s *ViewState) {
		s.Revision = revision.ID()
		s.Status = "Explicit repository selection saved. Discovery filters do not change selection."
		s.Error = ""
	})
	w.refreshDurableView(w.ctx)
}

func (w *LocalWorkflow) saveRepositoryFilters(command Command) {
	if w.state.Busy {
		w.setError("Wait for repository discovery or another operation to finish before changing filters.")
		return
	}
	current, err := w.store.CurrentConfig(w.ctx)
	if err != nil {
		w.setError("Saved settings could not be loaded. Discovery filters were not changed.")
		return
	}
	if current.ID() != command.ExpectedRevision {
		w.setError("Settings changed elsewhere. Reload discovery filters before editing again.")
		w.refreshDurableView(w.ctx)
		return
	}
	values := current.Config()
	if values.GitHub.Filters == command.Filters {
		return
	}
	values.GitHub.Filters = command.Filters
	revision, err := w.store.SaveConfig(w.ctx, current.ID(), values)
	if err != nil {
		w.setError("Discovery filters could not be saved at the displayed settings revision.")
		w.refreshDurableView(w.ctx)
		return
	}
	w.mutate(func(s *ViewState) {
		s.Revision = revision.ID()
		s.Filters = command.Filters
		s.Status = "Display filters saved. Repository selection did not change."
		s.Error = ""
	})
	w.refreshDurableView(w.ctx)
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func repositoryView(account store.GitHubAccount, rows []store.GitHubRepository, filters config.GitHubFilters, selectedIDs []string) []RepositoryView {
	selected := make(map[string]bool, len(selectedIDs))
	for _, id := range selectedIDs {
		selected[id] = true
	}
	accountID, _ := strconv.ParseInt(account.ID, 10, 64)
	filter := github.Filter{Owned: filters.Owned, Organizations: filters.Organizations, Collaborating: filters.Collaborating, ExcludeForks: filters.ExcludeForks}
	views := make([]RepositoryView, 0, len(rows))
	for _, row := range rows {
		ownerID, _ := strconv.ParseInt(row.OwnerID, 10, 64)
		metadata := github.Repository{Fork: row.Fork, Owner: github.User{ID: ownerID, Login: row.OwnerLogin, Type: row.OwnerType}}
		if !filter.Match(metadata, github.User{ID: accountID}) {
			continue
		}
		id := "github:" + row.ID
		availability := "Available"
		if !row.Available {
			availability = "Inaccessible or deleted"
		}
		views = append(views, RepositoryView{ID: id, Name: row.FullName, Description: row.Description, Availability: availability, Private: row.Private, Fork: row.Fork, Selected: selected[id], Available: row.Available})
	}
	return views
}

func stableRepositoryID(id string) bool {
	if !strings.HasPrefix(id, "github:") {
		return false
	}
	n, err := strconv.ParseInt(strings.TrimPrefix(id, "github:"), 10, 64)
	return err == nil && n > 0 && "github:"+strconv.FormatInt(n, 10) == id
}
