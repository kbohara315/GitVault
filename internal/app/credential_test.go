//go:build linux

package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"gitvault/internal/backup"
	"gitvault/internal/config"
	"gitvault/internal/secrets"
	"gitvault/internal/store"
)

type fakeCredentialStore struct {
	mu        sync.Mutex
	values    map[secrets.Handle][]byte
	storeErr  error
	deleteErr error
	read      func(context.Context, secrets.Handle) ([]byte, error)
	pending   bool
}

func newFakeCredentialStore() *fakeCredentialStore {
	return &fakeCredentialStore{values: make(map[secrets.Handle][]byte)}
}

func (f *fakeCredentialStore) Store(ctx context.Context, h secrets.Handle, value []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.storeErr != nil {
		return f.storeErr
	}
	if _, exists := f.values[h]; exists {
		return secrets.ErrExists
	}
	f.values[h] = append([]byte(nil), value...)
	return nil
}

func (f *fakeCredentialStore) ReadInteractive(ctx context.Context, h secrets.Handle) ([]byte, error) {
	if f.read != nil {
		return f.read(ctx, h)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.values[h]
	if !ok {
		return nil, secrets.ErrNotFound
	}
	return append([]byte(nil), v...), nil
}

func (f *fakeCredentialStore) Delete(ctx context.Context, h secrets.Handle) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.deleteErr != nil {
		return f.deleteErr
	}
	clear(f.values[h])
	delete(f.values, h)
	return nil
}

func (f *fakeCredentialStore) CloseLeases()  {}
func (f *fakeCredentialStore) Pending() bool { return f.pending }

func (f *fakeCredentialStore) handles() []secrets.Handle {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := make([]secrets.Handle, 0, len(f.values))
	for h := range f.values {
		result = append(result, h)
	}
	return result
}

func (f *fakeCredentialStore) value(h secrets.Handle) []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]byte(nil), f.values[h]...)
}

type credentialTestEngine struct{}

func (credentialTestEngine) Create(context.Context, backup.Request) (backup.Result, error) {
	return backup.Result{}, errors.New("unexpected snapshot creation")
}
func (credentialTestEngine) Inspect(context.Context, string) (backup.Manifest, error) {
	return backup.Manifest{}, errors.New("unexpected snapshot inspection")
}
func (credentialTestEngine) Verify(context.Context, string) (backup.Verification, error) {
	return backup.Verification{}, errors.New("unexpected snapshot verification")
}

func newCredentialWorkflow(t *testing.T, f *appFixture, credentials CredentialStore) *LocalWorkflow {
	t.Helper()
	w, err := NewLocalWorkflowWithCredentials(context.Background(), f.store, func(store.RootBinding, config.Revision, chan<- backup.ProgressEvent) (SnapshotEngine, error) {
		return credentialTestEngine{}, nil
	}, credentials)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := w.Close(ctx); err != nil {
			t.Errorf("close credential workflow: %v", err)
		}
	})
	return w
}

func waitCredentialState(t *testing.T, w *LocalWorkflow, predicate func(ViewState) bool) ViewState {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		state := w.Snapshot()
		if predicate(state) {
			return state
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("credential state did not converge: %+v", w.Snapshot())
	return ViewState{}
}

func submitToken(t *testing.T, w *LocalWorkflow, revision int64, token string) {
	t.Helper()
	payload := []byte(token)
	if err := w.Submit(Command{Kind: SaveToken, ExpectedRevision: revision, Secret: payload}); err != nil {
		t.Fatal(err)
	}
	for _, b := range payload {
		if b != 0 {
			t.Fatal("Submit retained caller-owned credential bytes")
		}
	}
}

func TestGitHubCredentialOnboarding(t *testing.T) {
	f := newAppFixture(t)
	native := newFakeCredentialStore()
	w := newCredentialWorkflow(t, f, native)
	initial := w.Snapshot()

	const firstSentinel = "synthetic-GV-PAT-sentinel-one"
	submitToken(t, w, initial.Revision, firstSentinel)
	first := waitCredentialState(t, w, func(s ViewState) bool {
		return s.CredentialConfigured && !s.CredentialBusy && s.Revision > initial.Revision
	})
	firstConfig, err := f.store.CurrentConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	firstHandle := *firstConfig.Config().GitHub.Credential
	if firstHandle.Reference == "" || firstHandle.Generation != 1 || first.Error != "" {
		t.Fatalf("unexpected first credential revision/state: %+v %+v", firstHandle, first)
	}
	stored := native.handles()
	if len(stored) != 1 || string(native.value(stored[0])) != firstSentinel {
		t.Fatal("native store did not retain exactly the synthetic credential")
	}
	stateJSON, err := json.Marshal(first)
	if err != nil || strings.Contains(string(stateJSON), firstSentinel) {
		t.Fatal("credential value appeared in presentation state")
	}
	configJSON, err := json.Marshal(firstConfig.Config())
	if err != nil || strings.Contains(string(configJSON), firstSentinel) {
		t.Fatal("credential value appeared in persisted configuration")
	}

	const replacementSentinel = "synthetic-GV-PAT-sentinel-two"
	submitToken(t, w, first.Revision, replacementSentinel)
	second := waitCredentialState(t, w, func(s ViewState) bool {
		return s.CredentialConfigured && !s.CredentialBusy && s.Revision > first.Revision
	})
	secondConfig, err := f.store.CurrentConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	secondHandle := *secondConfig.Config().GitHub.Credential
	if secondHandle.Reference == firstHandle.Reference || secondHandle.Generation != firstHandle.Generation+1 {
		t.Fatalf("replacement did not create a distinct next generation: first=%+v second=%+v", firstHandle, secondHandle)
	}
	if len(native.handles()) != 2 {
		t.Fatal("replacement removed the prior generation needed by captured work")
	}
	for _, handle := range native.handles() {
		value := native.value(handle)
		if strings.Contains(string(value), firstSentinel) || strings.Contains(string(value), replacementSentinel) {
			// Values are expected only in the isolated fake native-store boundary.
			continue
		}
		t.Fatal("unexpected value in fake native store")
	}

	if err = w.Submit(Command{Kind: RemoveToken, ExpectedRevision: second.Revision}); err != nil {
		t.Fatal(err)
	}
	removed := waitCredentialState(t, w, func(s ViewState) bool {
		return !s.CredentialConfigured && !s.CredentialBusy && s.Revision > second.Revision
	})
	if removed.Error != "" || len(native.handles()) != 1 {
		t.Fatalf("remove did not clear only the configured generation: %+v handles=%v", removed, native.handles())
	}
	current, err := f.store.CurrentConfig(context.Background())
	if err != nil || current.Config().GitHub.Credential != nil {
		t.Fatal("removed credential reference remains in current settings", err)
	}
}

func TestGitHubCredentialOnboardingFailures(t *testing.T) {
	f := newAppFixture(t)
	native := newFakeCredentialStore()
	w := newCredentialWorkflow(t, f, native)
	initial := w.Snapshot()

	native.storeErr = secrets.ErrDenied
	const sentinel = "synthetic-GV-PAT-failure-sentinel"
	submitToken(t, w, initial.Revision, sentinel)
	failed := waitCredentialState(t, w, func(s ViewState) bool { return !s.CredentialBusy && s.Error != "" })
	if failed.CredentialConfigured || strings.Contains(failed.Error, sentinel) || len(native.handles()) != 0 {
		t.Fatal("failed native write changed settings, retained token, or leaked its value")
	}

	native.storeErr = nil
	current, err := f.store.CurrentConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	externalValues := current.Config()
	externalValues.Backup.RepositoryConcurrency++
	externalRevision, err := f.store.SaveConfig(context.Background(), current.ID(), externalValues)
	if err != nil {
		t.Fatal(err)
	}
	submitToken(t, w, initial.Revision, "synthetic-GV-PAT-stale-revision")
	stale := waitCredentialState(t, w, func(s ViewState) bool {
		return !s.CredentialBusy && s.Revision == externalRevision.ID() && strings.Contains(s.Error, "changed elsewhere")
	})
	if stale.CredentialConfigured || stale.Revision != externalRevision.ID() || len(native.handles()) != 0 {
		t.Fatalf("stale settings revision stored a credential: state=%+v handles=%v", stale, native.handles())
	}

	// Settings stop referencing the handle before native deletion, so failure
	// cannot leave a configured reference to a possibly deleted credential.
	native.storeErr = nil
	submitToken(t, w, stale.Revision, "synthetic-GV-PAT-for-remove")
	saved := waitCredentialState(t, w, func(s ViewState) bool {
		return s.CredentialConfigured && !s.CredentialBusy && s.Revision > stale.Revision
	})
	native.deleteErr = secrets.ErrDenied
	if err := w.Submit(Command{Kind: RemoveToken, ExpectedRevision: saved.Revision}); err != nil {
		t.Fatal(err)
	}
	failedRemove := waitCredentialState(t, w, func(s ViewState) bool {
		return !s.CredentialBusy && !s.CredentialConfigured && strings.Contains(s.Status, "not confirmed")
	})
	current, err = f.store.CurrentConfig(context.Background())
	if err != nil || failedRemove.Revision <= saved.Revision || current.Config().GitHub.Credential != nil || len(native.handles()) != 1 {
		t.Fatal("unconfirmed native delete left a dangling setting reference or lost the orphan outcome", err)
	}
}

func TestCredentialChangesRejectedWhileWorkflowIsBusy(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	credentials := newFakeCredentialStore()
	w := &LocalWorkflow{
		ctx: ctx, commands: make(chan Command, 1), credentials: credentials,
		state: ViewState{Busy: true},
	}
	secret := []byte("synthetic-GV-busy-rejection")
	if err := w.Submit(Command{Kind: SaveToken, Secret: secret}); !errors.Is(err, ErrCredentialOperationPending) {
		t.Fatalf("credential change was admitted while workflow was busy: %v", err)
	}
	for _, b := range secret {
		if b != 0 {
			t.Fatal("busy-rejected credential bytes were not cleared")
		}
	}
	if len(w.commands) != 0 {
		t.Fatal("busy credential command entered the queue")
	}
}

func TestCredentialBrokerRejectsLatePromptResult(t *testing.T) {
	values := config.Default()
	values.GitHub.Credential = &config.Credential{Reference: "synthetic-handle", Generation: 1}
	revision, err := config.NewRevision(1, time.Now(), values)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	late := []byte("synthetic-GV-late-prompt-sentinel")
	native := newFakeCredentialStore()
	native.read = func(context.Context, secrets.Handle) ([]byte, error) {
		cancel()
		return late, nil
	}
	broker, err := NewInteractiveGitHubCredentialBroker(ctx, native, revision, "fixture/private", t.TempDir(), "/not-the-current-executable")
	if broker != nil || !errors.Is(err, context.Canceled) {
		t.Fatal("late prompt result was used for a cancelled job", err)
	}
	for _, b := range late {
		if b != 0 {
			t.Fatal("late credential bytes were not cleared")
		}
	}
}

func TestCredentialBrokerUsesCapturedCredentialRevision(t *testing.T) {
	values := config.Default()
	values.GitHub.Credential = &config.Credential{Reference: "captured-handle", Generation: 3}
	revision, err := config.NewRevision(9, time.Now(), values)
	if err != nil {
		t.Fatal(err)
	}
	credentials := newFakeCredentialStore()
	handle := secrets.Handle{Reference: "captured-handle", Generation: 3}
	const sentinel = "synthetic-GV-captured-broker-sentinel"
	if err := credentials.Store(context.Background(), handle, []byte(sentinel)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	privateDir := t.TempDir()
	if err := os.Chmod(privateDir, 0700); err != nil {
		t.Fatal("private fixture permissions unavailable")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal("test executable unavailable")
	}
	// The Go test cache may apply a permissive umask. The broker intentionally
	// rejects group/world-writable executable images; constrain this fixture as
	// an installed application image would be.
	if err := os.Chmod(executable, 0700); err != nil {
		t.Fatal("test executable permissions unavailable")
	}
	broker, err := NewInteractiveGitHubCredentialBroker(ctx, credentials, revision, "fixture/private", privateDir, executable)
	if err != nil {
		t.Fatalf("captured credential did not create a job broker: %v", err)
	}
	defer broker.Close()
	args := broker.HelperArguments()
	config, err := broker.GitHelperConfig(executable)
	if err != nil || strings.Contains(strings.Join(args, " "), sentinel) || strings.Contains(config, sentinel) {
		t.Fatal("captured credential appeared in helper arguments or Git config")
	}
	if string(credentials.value(handle)) != sentinel {
		t.Fatal("broker factory selected a different secret handle")
	}
}
