//go:build linux

package app

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	"gitvault/internal/backup"
	"gitvault/internal/config"
	"gitvault/internal/github"
	"gitvault/internal/secrets"
	"gitvault/internal/store"
)

type discoveryScript struct {
	mu       sync.Mutex
	results  []github.Result
	errors   []error
	tokens   []string
	started  chan struct{}
	finished chan struct{}
	block    bool
}

func (d *discoveryScript) DiscoverBytes(ctx context.Context, token []byte) (github.Result, error) {
	d.mu.Lock()
	index := len(d.tokens)
	d.tokens = append(d.tokens, string(token))
	var result github.Result
	var err error
	if index < len(d.results) {
		result = d.results[index]
	}
	if index < len(d.errors) {
		err = d.errors[index]
	}
	started := d.started
	block := d.block
	d.mu.Unlock()
	if started != nil {
		select {
		case started <- struct{}{}:
		default:
		}
	}
	if block {
		<-ctx.Done()
		if d.finished != nil {
			close(d.finished)
		}
		return result, ctx.Err()
	}
	return result, err
}

func (d *discoveryScript) calls() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.tokens)
}

func (d *discoveryScript) tokenAt(i int) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.tokens[i]
}

func discoveredRepo(id int64, ownerID int64, ownerLogin, ownerType, name string) github.Repository {
	return github.Repository{
		ID: id, Name: name, FullName: ownerLogin + "/" + name,
		Owner:    github.User{ID: ownerID, Login: ownerLogin, Type: ownerType},
		CloneURL: "https://github.com/" + ownerLogin + "/" + name + ".git",
		Private:  true, Description: "synthetic discovery row",
	}
}

func completeDiscovery(repositories ...github.Repository) github.Result {
	return github.Result{User: github.User{ID: 101, Login: "alice", Type: "User"}, Repositories: repositories, Complete: true}
}

func configuredDiscoveryWorkflow(t *testing.T, script *discoveryScript) (*appFixture, *LocalWorkflow, *fakeCredentialStore, int64) {
	return configuredDiscoveryWorkflowWithCredentials(t, script, nil)
}

func configuredDiscoveryWorkflowWithCredentials(t *testing.T, script *discoveryScript, credentials *fakeCredentialStore) (*appFixture, *LocalWorkflow, *fakeCredentialStore, int64) {
	t.Helper()
	f := newAppFixture(t)
	if credentials == nil {
		credentials = newFakeCredentialStore()
	}
	handle := secrets.Handle{Reference: "synthetic-discovery-handle", Generation: 1}
	secret := []byte("synthetic-GV-discovery-token")
	if err := credentials.Store(context.Background(), handle, secret); err != nil {
		t.Fatal(err)
	}
	clear(secret)
	current, err := f.store.CurrentConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	values := current.Config()
	values.GitHub.Credential = &config.Credential{Reference: handle.Reference, Generation: int64(handle.Generation)}
	configured, err := f.store.SaveConfig(context.Background(), current.ID(), values)
	if err != nil {
		t.Fatal(err)
	}
	w, err := NewLocalWorkflowWithDiscovery(context.Background(), f.store, func(store.RootBinding, config.Revision, chan<- backup.ProgressEvent) (SnapshotEngine, error) {
		return credentialTestEngine{}, nil
	}, credentials, func(config.Revision) (GitHubDiscoverer, error) { return script, nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := w.Close(ctx); err != nil {
			t.Errorf("close GitHub discovery workflow: %v", err)
		}
	})
	return f, w, credentials, configured.ID()
}

func waitDiscoveryState(t *testing.T, w *LocalWorkflow, predicate func(ViewState) bool) ViewState {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		state := w.Snapshot()
		if predicate(state) {
			return state
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("GitHub discovery state did not converge: %+v", w.Snapshot())
	return ViewState{}
}

func submitDiscovery(t *testing.T, w *LocalWorkflow, revision int64) {
	t.Helper()
	if err := w.Submit(Command{Kind: Discover, ExpectedRevision: revision}); err != nil {
		t.Fatal(err)
	}
}

func TestStableExplicitSelection(t *testing.T) {
	first := completeDiscovery(
		discoveredRepo(201, 101, "alice", "User", "before-rename"),
		discoveredRepo(202, 88, "team", "Organization", "organization-repo"),
		discoveredRepo(203, 77, "bob", "User", "collaboration-repo"),
	)
	second := completeDiscovery(
		discoveredRepo(201, 101, "alice", "User", "after-rename"),
		discoveredRepo(202, 88, "team", "Organization", "organization-repo"),
		discoveredRepo(203, 77, "bob", "User", "collaboration-repo"),
		discoveredRepo(204, 101, "alice", "User", "new-repository"),
	)
	script := &discoveryScript{results: []github.Result{first, second}}
	f, w, _, revision := configuredDiscoveryWorkflow(t, script)
	_ = f
	state := waitDiscoveryState(t, w, func(s ViewState) bool { return s.CanDiscover && s.Revision == revision })
	submitDiscovery(t, w, state.Revision)
	state = waitDiscoveryState(t, w, func(s ViewState) bool { return s.InventoryComplete && s.Account == "alice" && len(s.Repositories) == 3 })
	if script.calls() != 1 || script.tokenAt(0) != "synthetic-GV-discovery-token" {
		t.Fatal("discovery did not receive the configured synthetic token exactly once")
	}
	for _, repository := range state.Repositories {
		if repository.Selected {
			t.Fatalf("new inventory was implicitly selected: %+v", repository)
		}
	}
	selectID := "github:201"
	if err := w.Submit(Command{Kind: SelectRepository, ExpectedRevision: state.Revision, RepositoryID: selectID, Selected: true}); err != nil {
		t.Fatal(err)
	}
	state = waitDiscoveryState(t, w, func(s ViewState) bool {
		for _, repository := range s.Repositories {
			if repository.ID == selectID && repository.Selected {
				return true
			}
		}
		return false
	})
	selectedRevision := state.Revision
	filter := config.GitHubFilters{Organizations: true}
	if err := w.Submit(Command{Kind: SetRepositoryFilters, ExpectedRevision: selectedRevision, Filters: filter}); err != nil {
		t.Fatal(err)
	}
	state = waitDiscoveryState(t, w, func(s ViewState) bool {
		return s.Revision > selectedRevision && s.Filters == filter && len(s.Repositories) == 1
	})
	current, err := f.store.CurrentConfig(context.Background())
	if err != nil || !reflect.DeepEqual(current.Config().Selection.RepositoryIDs, []string{selectID}) {
		t.Fatalf("display filter changed explicit selection: %v %v", current.Config().Selection.RepositoryIDs, err)
	}
	if err = w.Submit(Command{Kind: SetRepositoryFilters, ExpectedRevision: state.Revision, Filters: config.GitHubFilters{Owned: true, Organizations: true, Collaborating: true}}); err != nil {
		t.Fatal(err)
	}
	state = waitDiscoveryState(t, w, func(s ViewState) bool {
		return s.Filters.Owned && s.Filters.Organizations && s.Filters.Collaborating && len(s.Repositories) == 3
	})
	submitDiscovery(t, w, state.Revision)
	state = waitDiscoveryState(t, w, func(s ViewState) bool { return s.InventoryComplete && len(s.Repositories) == 4 })
	var renamed, newlyDiscovered bool
	for _, repository := range state.Repositories {
		switch repository.ID {
		case "github:201":
			renamed = repository.Name == "alice/after-rename" && repository.Selected
		case "github:204":
			newlyDiscovered = !repository.Selected
		}
	}
	if !renamed || !newlyDiscovered {
		t.Fatalf("stable identity, rename, or new-unselected behavior failed: %+v", state.Repositories)
	}
	if err = w.Submit(Command{Kind: SelectRepository, ExpectedRevision: state.Revision, RepositoryID: selectID, Selected: false}); err != nil {
		t.Fatal(err)
	}
	state = waitDiscoveryState(t, w, func(s ViewState) bool { return s.Revision > state.Revision })
	current, err = f.store.CurrentConfig(context.Background())
	if err != nil || len(current.Config().Selection.RepositoryIDs) != 0 {
		t.Fatalf("explicit deselection did not persist an empty selection: %v %v", current.Config().Selection.RepositoryIDs, err)
	}
}

func TestPartialDiscoveryPreservesSelection(t *testing.T) {
	initial := completeDiscovery(discoveredRepo(201, 101, "alice", "User", "selected"), discoveredRepo(202, 88, "team", "Organization", "visible"))
	partial := github.Result{User: initial.User, Repositories: []github.Repository{discoveredRepo(202, 88, "team", "Organization", "visible-renamed")}}
	complete := completeDiscovery(discoveredRepo(202, 88, "team", "Organization", "visible-renamed"))
	script := &discoveryScript{results: []github.Result{initial, partial, complete}, errors: []error{nil, &github.Error{Kind: github.Transport}, nil}}
	f, w, _, revision := configuredDiscoveryWorkflow(t, script)
	state := waitDiscoveryState(t, w, func(s ViewState) bool { return s.CanDiscover && s.Revision == revision })
	submitDiscovery(t, w, state.Revision)
	state = waitDiscoveryState(t, w, func(s ViewState) bool { return s.InventoryComplete && len(s.Repositories) == 2 })
	if err := w.Submit(Command{Kind: SelectRepository, ExpectedRevision: state.Revision, RepositoryID: "github:201", Selected: true}); err != nil {
		t.Fatal(err)
	}
	state = waitDiscoveryState(t, w, func(s ViewState) bool {
		for _, repository := range s.Repositories {
			if repository.ID == "github:201" && repository.Selected {
				return true
			}
		}
		return false
	})
	submitDiscovery(t, w, state.Revision)
	state = waitDiscoveryState(t, w, func(s ViewState) bool {
		return !s.DiscoveryBusy && !s.InventoryComplete && s.Account == "alice" && s.Error != ""
	})
	if len(state.Repositories) != 2 {
		t.Fatalf("partial inventory replaced saved rows: %+v", state.Repositories)
	}
	for _, repository := range state.Repositories {
		if repository.ID == "github:201" && (!repository.Selected || !repository.Available || repository.Availability != "Available") {
			t.Fatalf("unseen row was marked inaccessible or deselected by partial result: %+v", repository)
		}
	}
	current, err := f.store.CurrentConfig(context.Background())
	if err != nil || !reflect.DeepEqual(current.Config().Selection.RepositoryIDs, []string{"github:201"}) {
		t.Fatalf("partial discovery erased explicit selection: %v %v", current.Config().Selection.RepositoryIDs, err)
	}
	submitDiscovery(t, w, state.Revision)
	state = waitDiscoveryState(t, w, func(s ViewState) bool { return s.InventoryComplete && len(s.Repositories) == 2 })
	for _, repository := range state.Repositories {
		if repository.ID == "github:201" && (repository.Available || !repository.Selected || repository.Availability != "Inaccessible or deleted") {
			t.Fatalf("complete disappearance was not visible without deleting selection: %+v", repository)
		}
	}
	current, err = f.store.CurrentConfig(context.Background())
	if err != nil || !reflect.DeepEqual(current.Config().Selection.RepositoryIDs, []string{"github:201"}) {
		t.Fatalf("complete reconciliation erased saved selection: %v %v", current.Config().Selection.RepositoryIDs, err)
	}
}

func TestCancelledDiscoveryPromptRemainsVisibleUntilRefresh(t *testing.T) {
	credentials := newFakeCredentialStore()
	credentials.read = func(context.Context, secrets.Handle) ([]byte, error) {
		credentials.setPending(true)
		return nil, context.Canceled
	}
	script := &discoveryScript{}
	_, w, _, revision := configuredDiscoveryWorkflowWithCredentials(t, script, credentials)
	state := waitDiscoveryState(t, w, func(s ViewState) bool { return s.CanDiscover && s.Revision == revision })
	submitDiscovery(t, w, state.Revision)
	state = waitDiscoveryState(t, w, func(s ViewState) bool { return !s.Busy && !s.DiscoveryBusy && s.CredentialBusy })
	if state.CanManageCredential || state.CanDiscover || script.calls() != 0 {
		t.Fatalf("pending native prompt was hidden or discovery proceeded: %+v calls=%d", state, script.calls())
	}
	credentials.setPending(false)
	if err := w.Submit(Command{Kind: Refresh}); err != nil {
		t.Fatal(err)
	}
	state = waitDiscoveryState(t, w, func(s ViewState) bool { return !s.CredentialBusy && s.CanManageCredential && s.CanDiscover })
	if script.calls() != 0 {
		t.Fatal("status refresh unexpectedly started another discovery request")
	}
}

func TestDiscoveryCancellationStopsWorkerAndShutdownWaits(t *testing.T) {
	script := &discoveryScript{results: []github.Result{completeDiscovery()}, started: make(chan struct{}, 1), finished: make(chan struct{}), block: true}
	_, w, _, revision := configuredDiscoveryWorkflow(t, script)
	state := waitDiscoveryState(t, w, func(s ViewState) bool { return s.CanDiscover && s.Revision == revision })
	submitDiscovery(t, w, state.Revision)
	select {
	case <-script.started:
	case <-time.After(5 * time.Second):
		t.Fatal("discovery client was not called")
	}
	if err := w.Submit(Command{Kind: Cancel}); err != nil {
		t.Fatal(err)
	}
	state = waitDiscoveryState(t, w, func(s ViewState) bool {
		return !s.Busy && !s.DiscoveryBusy && s.CanDiscover && s.Status == "Repository discovery cancelled. Saved selections and unseen inventory were preserved."
	})
	select {
	case <-script.finished:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled discovery client did not return")
	}
	if err := w.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if script.calls() != 1 {
		t.Fatal("duplicate discovery started while the worker was busy")
	}

	shutdownScript := &discoveryScript{results: []github.Result{completeDiscovery()}, started: make(chan struct{}, 1), finished: make(chan struct{}), block: true}
	_, shutdownWorkflow, _, shutdownRevision := configuredDiscoveryWorkflow(t, shutdownScript)
	state = waitDiscoveryState(t, shutdownWorkflow, func(s ViewState) bool { return s.CanDiscover && s.Revision == shutdownRevision })
	submitDiscovery(t, shutdownWorkflow, state.Revision)
	select {
	case <-shutdownScript.started:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown discovery client was not called")
	}
	closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer closeCancel()
	if err := shutdownWorkflow.Close(closeCtx); err != nil {
		t.Fatalf("shutdown did not wait for discovery worker: %v", err)
	}
	select {
	case <-shutdownScript.finished:
	case <-time.After(time.Second):
		t.Fatal("shutdown returned before discovery worker cleared its credential")
	}
}
