package desktop

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gitvault/internal/app"
	"gitvault/internal/config"
	"gitvault/internal/github"
	"gitvault/internal/secrets"
	"gitvault/internal/store"

	"github.com/egoist/mygo/ui"
)

func TestInitialView(t *testing.T) {
	a := New()
	tt := ui.NewTester(a.View, 1000, 700)
	for _, text := range []string{"GitVault", "Application is not connected", pages[0].title, "Coverage: Git history and references. LFS payloads, submodule repositories, and GitHub discussions are not included."} {
		if !tt.HasText(text) {
			t.Errorf("missing %q; visible text: %q", text, tt.Texts())
		}
	}
}

func TestLogsSidebarCopiesAllEntries(t *testing.T) {
	state := app.ViewState{Connected: true, Logs: []app.LogEntry{{At: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Level: "error", Message: "safe failure detail"}}}
	tt := ui.NewTester(NewWithBackend(&testBackend{state: state}).View, 1000, 800)
	if err := tt.Click("Logs"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("safe failure detail") || !tt.HasText("Copy all logs") {
		t.Fatalf("logs view not rendered: %q", tt.Texts())
	}
	if err := tt.Click("Copy all logs"); err != nil {
		t.Fatal(err)
	}
	if got := tt.Clipboard(); !strings.Contains(got, "[ERROR] safe failure detail") {
		t.Fatalf("unexpected clipboard content %q", got)
	}
}

func TestGuidedSetupNavigationDoesNotStartWork(t *testing.T) {
	for _, tc := range []struct {
		name, action string
		state        app.ViewState
		page         int
	}{
		{"folder", "Set up backup folder", app.ViewState{Connected: true}, 4},
		{"selection", "Choose GitHub repositories", app.ViewState{Connected: true, Settings: app.SettingsDraft{Root: "/synthetic-vault"}}, 1},
		{"review", "Review and start backup", app.ViewState{Connected: true, CredentialConfigured: true, SelectedRepositoryCount: 2, Settings: app.SettingsDraft{Root: "/synthetic-vault"}}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend := &testBackend{state: tc.state}
			a := NewWithBackend(backend)
			tester := ui.NewTester(a.View, 800, 560)
			if err := tester.Click(tc.action); err != nil {
				t.Fatal(err)
			}
			if a.selected != tc.page || len(backend.commands) != 0 {
				t.Fatalf("navigation submitted work or opened wrong page: page=%d commands=%v", a.selected, backend.commands)
			}
		})
	}
}

func TestConfiguredConnectionUsesProgressiveDisclosure(t *testing.T) {
	b := &testBackend{state: app.ViewState{Connected: true, CanManageCredential: true, CredentialConfigured: true, SelectedRepositoryCount: 2}}
	a := NewWithBackend(b)
	a.selected = 1
	tester := ui.NewTester(a.View, 1000, 800)
	if tester.HasText("GitHub personal access token") {
		t.Fatal("configured connection unnecessarily exposes token editor")
	}
	if err := tester.Click("Manage GitHub connection"); err != nil {
		t.Fatal(err)
	}
	if err := tester.Click("GitHub personal access token"); err != nil {
		t.Fatal(err)
	}
	tester.Type("synthetic-sensitive-draft")
	if err := tester.Click("Manage GitHub connection"); err != nil {
		t.Fatal(err)
	}
	if !a.githubToken.Empty() {
		t.Fatal("closing connection editor retained token draft")
	}
	if err := tester.Click("Continue to backup"); err != nil {
		t.Fatal(err)
	}
	if a.selected != 2 || len(b.commands) != 0 {
		t.Fatal("continue should navigate without starting a backup")
	}
}

func TestLogsNewestFirstCopiesChronologicalHistory(t *testing.T) {
	logs := []app.LogEntry{{Level: "error", Message: "older failure"}, {Level: "info", Message: "latest success"}}
	a := NewWithBackend(&testBackend{state: app.ViewState{Logs: logs}})
	a.selected = 5
	tester := ui.NewTester(a.View, 800, 560)
	text := strings.Join(tester.Texts(), "\n")
	if strings.Index(text, "latest success") > strings.Index(text, "older failure") {
		t.Fatal("old failures hide latest result")
	}
	if err := tester.Click("Copy all logs"); err != nil {
		t.Fatal(err)
	}
	if tester.Clipboard() != formatLogs(logs) {
		t.Fatal("copy reordered or omitted history")
	}
}

type testBackend struct {
	state    app.ViewState
	commands []app.Command
}

func (b *testBackend) Snapshot() app.ViewState { return b.state }
func (b *testBackend) Submit(c app.Command) error {
	c.Secret = append([]byte(nil), c.Secret...)
	b.commands = append(b.commands, c)
	return nil
}

func TestNativeLocalAdmissionAndBusyState(t *testing.T) {
	b := &testBackend{state: app.ViewState{Connected: true, Revision: 7, CanStartLocal: true, CanBindRoot: true, Settings: app.SettingsDraft{Root: "/vault"}}}
	a := NewWithBackend(b)
	tt := ui.NewTester(a.View, 1000, 800)
	if err := tt.Click("Backups"); err != nil {
		t.Fatal(err)
	}
	// An empty source cannot submit an operation.
	if err := tt.Click("Back up local repository"); err != nil {
		t.Fatal(err)
	}
	if len(b.commands) != 0 {
		t.Fatal("empty repository path submitted")
	}
	if err := tt.Click("Local repository path"); err != nil {
		t.Fatal(err)
	}
	tt.Type("/source")
	if err := tt.Click("Back up local repository"); err != nil {
		t.Fatal(err)
	}
	if len(b.commands) != 1 || b.commands[0].Kind != app.BackupLocal || b.commands[0].Source != "/source" || b.commands[0].Root != "" {
		t.Fatalf("wrong admitted intent: %+v", b.commands)
	}
	b.state.Busy = true
	b.state.CanCancel = true
	tt.Frame()
	if err := tt.Click("Back up local repository"); err != nil {
		t.Fatal(err)
	}
	if len(b.commands) != 1 {
		t.Fatal("busy view admitted another backup")
	}
	if err := tt.Click("Cancel operation"); err != nil {
		t.Fatal(err)
	}
	if len(b.commands) != 2 || b.commands[1].Kind != app.Cancel {
		t.Fatalf("cancel not routed: %+v", b.commands)
	}
}

func TestNativeRootBindingIsExplicit(t *testing.T) {
	b := &testBackend{state: app.ViewState{Connected: true, Revision: 11, CanBindRoot: true}}
	a := NewWithBackend(b)
	tt := ui.NewTester(a.View, 1000, 800)
	if err := tt.Click("Settings"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Backup folder"); err != nil {
		t.Fatal(err)
	}
	tt.Type("/vault")
	if err := tt.Click("Bind backup folder"); err != nil {
		t.Fatal(err)
	}
	if len(b.commands) != 1 {
		t.Fatalf("commands: %+v", b.commands)
	}
	got := b.commands[0]
	if got.Kind != app.BindRoot || got.Root != "/vault" || got.ExpectedRevision != 11 {
		t.Fatalf("wrong bind intent: %+v", got)
	}
}

func TestPrivateBackupFolderActionIsExplicit(t *testing.T) {
	b := &testBackend{state: app.ViewState{Connected: true, Revision: 12, CanBindRoot: true}}
	a := NewWithBackend(b)
	tt := ui.NewTester(a.View, 1000, 900)
	if err := tt.Click("Backups"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Create and bind private backup folder"); err != nil {
		t.Fatal(err)
	}
	if len(b.commands) != 1 {
		t.Fatalf("commands: %+v", b.commands)
	}
	command := b.commands[0]
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if command.Kind != app.BindRoot || !command.CreateRoot || command.Root != filepath.Join(home, "GitVault-Backups") {
		t.Fatalf("private-folder action submitted wrong bind: %+v", command)
	}
}

func TestUnavailableFeaturesDoNotExposeControls(t *testing.T) {
	b := &testBackend{state: app.ViewState{Connected: true}}
	a := NewWithBackend(b)
	tt := ui.NewTester(a.View, 1000, 850)
	if err := tt.Click("Repositories"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("Native GitHub credential setup is not available because the OS secure store is unavailable.") {
		t.Fatalf("missing explicit unavailable status: %v", tt.Texts())
	}
	for _, control := range []string{"Save or replace GitHub token", "GitHub personal access token", "Remove configured GitHub token", "Select all", "owner/repo"} {
		if tt.HasText(control) {
			t.Errorf("unimplemented control %q was exposed", control)
		}
	}
}

func TestNativeCredentialOnboardingControls(t *testing.T) {
	b := &testBackend{state: app.ViewState{Connected: true, Revision: 23, CanManageCredential: true}}
	a := NewWithBackend(b)
	tt := ui.NewTester(a.View, 1000, 900)
	if err := tt.Click("Repositories"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("GitHub personal access token"); err != nil {
		t.Fatal(err)
	}
	const sentinel = "synthetic-GV-PAT-ui-sentinel"
	tt.Type(sentinel)
	if !bytes.Equal(a.githubToken.value, []byte(sentinel)) {
		t.Fatal("native text input did not populate the byte-owned draft")
	}
	if strings.Contains(strings.Join(tt.Texts(), "\n"), sentinel) || !strings.Contains(strings.Join(tt.Texts(), "\n"), strings.Repeat("•", len(sentinel))) {
		t.Fatal("password input did not expose only a masked display")
	}
	tt.Key(0, ui.KeyBackspace)
	if !bytes.Equal(a.githubToken.value, []byte(sentinel[:len(sentinel)-1])) {
		t.Fatal("native backspace did not edit the byte-owned draft")
	}
	tt.Type(sentinel[len(sentinel)-1:])
	// Explicitly replace the draft; the previous backing byte slice must be
	// overwritten before the replacement is retained.
	previous := a.githubToken.value
	replacement := "synthetic-GV-PAT-replacement"
	tt.Key(0, ui.KeyHome)
	tt.Key(ui.Shift, ui.KeyEnd)
	tt.Type(replacement)
	if !bytes.Equal(a.githubToken.value, []byte(replacement)) {
		t.Fatal("selection replacement did not update the byte-owned draft")
	}
	for _, b := range previous {
		if b != 0 {
			t.Fatal("replaced credential draft bytes were not cleared")
		}
	}
	tt.Frame()
	if strings.Contains(strings.Join(tt.Texts(), "\n"), replacement) || !strings.Contains(strings.Join(tt.Texts(), "\n"), strings.Repeat("•", len(replacement))) {
		t.Fatal("replacement draft was not masked")
	}
	if err := tt.Click("Save or replace GitHub token"); err != nil {
		t.Fatal(err)
	}
	if !a.githubToken.Empty() || len(b.commands) != 1 {
		t.Fatal("password draft was retained or command was not submitted")
	}
	got := b.commands[0]
	defer clear(got.Secret)
	if got.Kind != app.SaveToken || got.ExpectedRevision != 23 || string(got.Secret) != replacement {
		t.Fatal("token command was not bounded to the explicit settings revision")
	}
	if strings.Contains(strings.Join(tt.Texts(), "\n"), sentinel) || strings.Contains(strings.Join(tt.Texts(), "\n"), replacement) {
		t.Fatal("submitted token remained visible in the native UI")
	}
	cleared := a.githubToken.value
	a.ClearSensitiveDrafts()
	if len(a.githubToken.value) != 0 {
		t.Fatal("sensitive draft remained after cleanup")
	}
	for _, b := range cleared {
		if b != 0 {
			t.Fatal("cleared credential draft bytes were not overwritten")
		}
	}
}

func TestNativeCredentialPasteIsMasked(t *testing.T) {
	b := &testBackend{state: app.ViewState{Connected: true, Revision: 24, CanManageCredential: true}}
	a := NewWithBackend(b)
	tt := ui.NewTester(a.View, 1000, 900)
	if err := tt.Click("Repositories"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("GitHub personal access token"); err != nil {
		t.Fatal(err)
	}
	const pasted = "synthetic-GV-PAT-pasted"
	tt.SetClipboard(pasted)
	tt.Key(ui.Ctrl, ui.KeyV)
	if !bytes.Equal(a.githubToken.value, []byte(pasted)) {
		t.Fatal("Ctrl+V did not paste into the secure token draft")
	}
	if strings.Contains(strings.Join(tt.Texts(), "\n"), pasted) || !strings.Contains(strings.Join(tt.Texts(), "\n"), strings.Repeat("•", len(pasted))) {
		t.Fatal("pasted token was not kept masked")
	}
	tt.SetClipboard("-plain")
	tt.Key(ui.Ctrl|ui.Shift, ui.KeyV)
	if !bytes.Equal(a.githubToken.value, []byte(pasted+"-plain")) {
		t.Fatal("Ctrl+Shift+V did not paste into the secure token draft")
	}
	a.ClearSensitiveDrafts()
}

func TestNativeGitHubDiscoveryFiltersAndExplicitSelection(t *testing.T) {
	b := &testBackend{state: app.ViewState{
		Connected: true, Revision: 41, CanDiscover: true, Account: "alice", InventoryComplete: true,
		Filters: config.GitHubFilters{Owned: true, Organizations: true, Collaborating: true},
		Repositories: []app.RepositoryView{
			{ID: "github:201", Name: "alice/one", Availability: "Available", Available: true},
			{ID: "github:202", Name: "team/two", Availability: "Inaccessible or deleted", Selected: true},
		},
	}}
	a := NewWithBackend(b)
	tt := ui.NewTester(a.View, 1000, 1000)
	if err := tt.Click("Repositories"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("Complete inventory for GitHub account alice.") || !tt.HasText("Newly discovered repositories stay unselected") {
		t.Fatalf("repository inventory status or explicit-selection disclosure missing: %v", tt.Texts())
	}
	if tt.HasText("Select all") {
		t.Fatal("repository discovery exposed implicit select-all")
	}
	if err := tt.Click("Discover GitHub repositories"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Organization repositories"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("alice/one"); err != nil {
		t.Fatal(err)
	}
	if len(b.commands) != 3 {
		t.Fatalf("unexpected repository commands: %+v", b.commands)
	}
	if b.commands[0].Kind != app.Discover || b.commands[0].ExpectedRevision != 41 {
		t.Fatalf("discovery was not revision-bound: %+v", b.commands[0])
	}
	if b.commands[1].Kind != app.SetRepositoryFilters || b.commands[1].ExpectedRevision != 41 || b.commands[1].Filters.Organizations {
		t.Fatalf("display filter edit was not revision-bound or unexpectedly retained: %+v", b.commands[1])
	}
	if b.commands[2].Kind != app.SelectRepository || b.commands[2].ExpectedRevision != 41 || b.commands[2].RepositoryID != "github:201" || !b.commands[2].Selected {
		t.Fatalf("selection did not use the stable explicit ID: %+v", b.commands[2])
	}
}

func TestSelectedBackupControls(t *testing.T) {
	b := &testBackend{state: app.ViewState{
		Connected: true, Revision: 52, CanStartSelected: true, SelectedRepositoryCount: 2,
		Tasks: []app.TaskView{{Repository: "alice/demo", RepositoryID: "github:201", RunID: "run-synthetic-1", CanRetry: true}},
	}}
	a := NewWithBackend(b)
	tt := ui.NewTester(a.View, 1000, 1000)
	if err := tt.Click("Backups"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("Explicitly selected GitHub repositories: 2") {
		t.Fatalf("selected repository count is not visible: %v", tt.Texts())
	}
	if err := tt.Click("Back up selected GitHub repositories"); err != nil {
		t.Fatal(err)
	}
	if len(b.commands) != 1 || b.commands[0].Kind != app.BackupSelected || b.commands[0].ExpectedRevision != 52 {
		t.Fatalf("selected backup did not submit its captured selection revision: %+v", b.commands)
	}
	if err := tt.Click("Retry alice/demo"); err != nil {
		t.Fatal(err)
	}
	if len(b.commands) != 2 {
		t.Fatalf("retry command missing: %+v", b.commands)
	}
	retry := b.commands[1]
	if retry.Kind != app.RetryGitHubTask || retry.ExpectedRevision != 52 || retry.RepositoryID != "github:201" || retry.RunID != "run-synthetic-1" {
		t.Fatalf("retry did not preserve the durable task identity: %+v", retry)
	}

	b.state.Busy = true
	tt.Frame()
	if err := tt.Click("Back up selected GitHub repositories"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Retry alice/demo"); err != nil {
		t.Fatal(err)
	}
	if len(b.commands) != 2 {
		t.Fatalf("busy controls admitted additional selected work: %+v", b.commands)
	}

	empty := &testBackend{state: app.ViewState{Connected: true, Revision: 53, SelectedRepositoryCount: 0}}
	emptyTester := ui.NewTester(NewWithBackend(empty).View, 1000, 1000)
	if err := emptyTester.Click("Backups"); err != nil {
		t.Fatal(err)
	}
	if err := emptyTester.Click("Back up selected GitHub repositories"); err != nil {
		t.Fatal(err)
	}
	if len(empty.commands) != 0 {
		t.Fatalf("empty selection submitted a GitHub backup: %+v", empty.commands)
	}
	testSelectedBackupCoordinatorAdmission(t)
}

func testSelectedBackupCoordinatorAdmission(t *testing.T) {
	t.Helper()
	s, base := workflowStore(t)
	root := filepath.Join(base, "selected-vault")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	credentials := &controlCredentialStore{entered: make(chan struct{})}
	discovery := func(config.Revision) (app.GitHubDiscoverer, error) { return controlDiscovery{}, nil }
	w, err := app.NewLocalWorkflowWithDiscovery(context.Background(), s, localEngineBuilder(s), credentials, discovery)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if closeErr := w.Close(ctx); closeErr != nil {
			t.Errorf("close coordinator-backed selected workflow: %v", closeErr)
		}
	})
	initial := w.Snapshot()
	submitWorkflow(t, w, app.Command{Kind: app.BindRoot, ExpectedRevision: initial.Revision, Root: root})
	bound := waitWorkflow(t, w, func(state app.ViewState) bool {
		return state.CanStartLocal && state.Settings.Root == root
	})
	current, err := s.CurrentConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	values := current.Config()
	values.GitHub.Credential = &config.Credential{Reference: "synthetic-desktop-selection", Generation: 1}
	values.Selection.RepositoryIDs = []string{"github:201"}
	revision, err := s.SaveConfig(context.Background(), current.ID(), values)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ReconcileGitHubInventory(context.Background(), store.GitHubAccount{ID: "101", Login: "alice", Type: "User"}, []store.GitHubRepository{{
		ID: "201", OwnerID: "101", OwnerLogin: "alice", OwnerType: "User", Name: "demo", FullName: "alice/demo",
		CloneURL: "https://github.com/alice/demo.git", Private: true, Available: true,
	}}, true); err != nil {
		t.Fatal(err)
	}
	submitWorkflow(t, w, app.Command{Kind: app.Refresh, ExpectedRevision: revision.ID()})
	ready := waitWorkflow(t, w, func(state app.ViewState) bool {
		return state.CanStartSelected && state.SelectedRepositoryCount == 1 && state.Revision == revision.ID()
	})
	if ready.Revision <= bound.Revision {
		t.Fatal("selected configuration revision was not loaded by the native workflow")
	}

	backend := &localWorkflowBackend{LocalWorkflow: w}
	tester := ui.NewTester(NewWithBackend(backend).View, 1000, 1000)
	if err = tester.Click("Backups"); err != nil {
		t.Fatal(err)
	}
	if err = tester.Click("Back up selected GitHub repositories"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-credentials.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("selected backup did not reach coordinator credential acquisition")
	}
	runs, err := s.QueryRuns(context.Background(), 10)
	if err != nil || len(runs) != 1 || runs[0].State != "running" || runs[0].Trigger != "manual" || runs[0].Intent.AccountID != "101" || len(runs[0].Intent.RepositoryIDs) != 1 || runs[0].Intent.RepositoryIDs[0] != "github:201" {
		t.Fatalf("native selected command did not durably reach the coordinator: runs=%+v err=%v", runs, err)
	}
	tasks, err := s.ListTasks(context.Background(), runs[0].ID, 10)
	if err != nil || len(tasks) != 1 || tasks[0].RepositoryID != "201" || tasks[0].OperationID == "" {
		t.Fatalf("coordinator did not reserve the selected stable repository identity: tasks=%+v err=%v", tasks, err)
	}
	waitWorkflow(t, w, func(state app.ViewState) bool { return state.Busy && state.CanCancel })
	tester.Frame()
	if err = tester.Click("Cancel operation"); err != nil {
		t.Fatal(err)
	}
	waitWorkflow(t, w, func(state app.ViewState) bool { return !state.Busy && strings.Contains(state.Status, "cancelled") })
	terminal, err := s.GetRun(context.Background(), runs[0].ID)
	terminalTasks, tasksErr := s.ListTasks(context.Background(), runs[0].ID, 10)
	if err != nil || terminal.State != "cancelled" || tasksErr != nil || len(terminalTasks) != 1 || terminalTasks[0].Outcome != "cancelled" {
		t.Fatalf("native cancellation did not reach durable coordinator terminal state: run=%+v tasks=%+v err=%v tasksErr=%v", terminal, terminalTasks, err, tasksErr)
	}
}

type localWorkflowBackend struct{ *app.LocalWorkflow }

type controlCredentialStore struct {
	entered chan struct{}
	once    sync.Once
	pending atomic.Bool
}

func (*controlCredentialStore) Store(context.Context, secrets.Handle, []byte) error { return nil }
func (c *controlCredentialStore) ReadInteractive(ctx context.Context, _ secrets.Handle) ([]byte, error) {
	c.pending.Store(true)
	c.once.Do(func() { close(c.entered) })
	defer c.pending.Store(false)
	<-ctx.Done()
	return nil, ctx.Err()
}
func (*controlCredentialStore) Delete(context.Context, secrets.Handle) error { return nil }
func (*controlCredentialStore) CloseLeases()                                 {}
func (c *controlCredentialStore) Pending() bool                              { return c.pending.Load() }

type controlDiscovery struct{}

func (controlDiscovery) DiscoverBytes(context.Context, []byte) (github.Result, error) {
	return github.Result{}, errors.New("discovery must not start before this control test cancels")
}

func TestSecretInputClientOnlyExposesMask(t *testing.T) {
	const sentinel = "synthetic-GV-secret-client-sentinel"
	draft := &secretDraft{}
	draft.ReplaceText(nil, sentinel)
	if !bytes.Equal(draft.value, []byte(sentinel)) {
		t.Fatal("text-input client did not keep the draft in owned bytes")
	}
	masked, actual := draft.TextForRange(ui.TextInputRange{Start: 0, End: draft.Len()})
	if masked != strings.Repeat("•", len(sentinel)) || strings.Contains(masked, sentinel) || actual != (ui.TextInputRange{Start: 0, End: len(sentinel)}) {
		t.Fatal("native text query returned other than the bounded mask")
	}
	if _, marked := draft.MarkedRange(); marked {
		t.Fatal("secret input retained a marked composition range")
	}
	old := draft.value
	draft.SetMarkedText(nil, "synthetic-GV-IME-sentinel", ui.TextInputRange{})
	if !bytes.Equal(draft.value, []byte(sentinel)) {
		t.Fatal("uncommitted composition text was retained")
	}
	draft.ReplaceText(nil, "bad token")
	if !bytes.Equal(draft.value, []byte(sentinel)) {
		t.Fatal("invalid token text modified the draft")
	}
	draft.Clear()
	for _, b := range old {
		if b != 0 {
			t.Fatal("cleared secret-input client bytes were not overwritten")
		}
	}
}

func TestRestorePageIsExplicitlyUnavailable(t *testing.T) {
	b := &testBackend{state: app.ViewState{Connected: true, Snapshots: []app.SnapshotView{{ID: "snapshot-1", Path: "/vault/snapshot", LocalReady: true}}}}
	a := NewWithBackend(b)
	tt := ui.NewTester(a.View, 1000, 800)
	if err := tt.Click("Restore"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("Restore is not available in this build.") || !tt.HasText("No restore command is exposed") {
		t.Fatalf("restore limitation is unclear: %v", tt.Texts())
	}
	for _, control := range []string{"Verify and restore", "New restore destination"} {
		if tt.HasText(control) {
			t.Errorf("unimplemented restore control %q was exposed", control)
		}
	}
	if len(b.commands) != 0 {
		t.Fatalf("unavailable page submitted commands: %+v", b.commands)
	}
}

func TestNavigation(t *testing.T) {
	a := New()
	tt := ui.NewTester(a.View, 1000, 700)
	for i, p := range pages {
		if err := tt.Click(p.name); err != nil {
			t.Fatal(err)
		}
		if a.selected != i || !tt.HasText(p.title) {
			t.Errorf("navigation to %q failed; visible text: %q", p.name, tt.Texts())
		}
	}
}
