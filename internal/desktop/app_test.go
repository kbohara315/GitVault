package desktop

import (
	"errors"
	"testing"
	"time"

	"gitvault/internal/app"

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

type testBackend struct {
	state    app.ViewState
	commands []app.Command
	err      error
}

func (b *testBackend) Snapshot() app.ViewState { return b.state }
func (b *testBackend) Submit(c app.Command) error {
	if b.err != nil {
		return b.err
	}
	b.commands = append(b.commands, c)
	return nil
}

func TestNativeLocalAdmissionAndBusyState(t *testing.T) {
	b := &testBackend{state: app.ViewState{Connected: true, Settings: app.SettingsDraft{Root: "/vault"}}}
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
	if len(b.commands) != 1 || b.commands[0].Kind != app.BackupLocal || b.commands[0].Source != "/source" || b.commands[0].Root != "/vault" {
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

func TestNativeRestoreCarriesExactSnapshotAndDestination(t *testing.T) {
	b := &testBackend{state: app.ViewState{Connected: true, Snapshots: []app.SnapshotView{{ID: "01234567-89ab", Repository: "owner/repo", Path: "/vault/snapshots/one", CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), LocalReady: true, Verification: "object integrity passed"}}}}
	a := NewWithBackend(b)
	tt := ui.NewTester(a.View, 1000, 850)
	if err := tt.Click("Backups"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Restore 01234567"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("Recovery comes first") {
		t.Fatalf("restore page not opened: %v", tt.Texts())
	}
	if err := tt.Click("New restore destination"); err != nil {
		t.Fatal(err)
	}
	tt.Type("/new-bare.git")
	if err := tt.Click("Verify and restore"); err != nil {
		t.Fatal(err)
	}
	if len(b.commands) != 1 {
		t.Fatalf("commands: %+v", b.commands)
	}
	got := b.commands[0]
	if got.Kind != app.Restore || got.SnapshotID != "01234567-89ab" || got.SnapshotPath != "/vault/snapshots/one" || got.Destination != "/new-bare.git" {
		t.Fatalf("wrong restore intent: %+v", got)
	}
}

func TestNativeSubmissionErrorAndSecretClearing(t *testing.T) {
	b := &testBackend{state: app.ViewState{Connected: true}, err: errors.New("operation already active")}
	a := NewWithBackend(b)
	tt := ui.NewTester(a.View, 1000, 850)
	if err := tt.Click("Repositories"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("GitHub token"); err != nil {
		t.Fatal(err)
	}
	tt.Type("synthetic-test-token")
	if err := tt.Click("Save GitHub token"); err != nil {
		t.Fatal(err)
	}
	if a.token != "" {
		t.Fatal("token retained in draft after submission")
	}
	if !tt.HasText("operation already active") {
		t.Fatalf("admission error not visible: %v", tt.Texts())
	}
	if len(b.commands) != 0 {
		t.Fatal("failed admission was recorded as accepted")
	}
}

func TestRepositoryRowsRenderAndSelectByStableID(t *testing.T) {
	b := &testBackend{state: app.ViewState{Connected: true, Revision: 7, Repositories: []app.RepositoryView{{ID: "101", Name: "owner/one"}, {ID: "202", Name: "owner/two"}}}}
	a := NewWithBackend(b)
	tt := ui.NewTester(a.View, 1000, 900)
	if err := tt.Click("Repositories"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("owner/two"); err != nil {
		t.Fatal(err)
	}
	if len(b.commands) != 1 || b.commands[0].Kind != app.SelectRepository || b.commands[0].RepositoryID != "202" || !b.commands[0].Selected {
		t.Fatalf("wrong selection identity: %+v", b.commands)
	}
}

func TestRestoreEditedPathInvalidatesSelectedIdentity(t *testing.T) {
	b := &testBackend{state: app.ViewState{Connected: true, Snapshots: []app.SnapshotView{{ID: "01234567-89ab", Path: "/vault/one"}}}}
	a := NewWithBackend(b)
	tt := ui.NewTester(a.View, 1000, 900)
	if err := tt.Click("Backups"); err != nil {
		t.Fatal(err)
	}
	a.destination = "/old-destination"
	if err := tt.Click("Restore 01234567"); err != nil {
		t.Fatal(err)
	}
	if a.destination != "" {
		t.Fatal("selection change retained stale destination")
	}
	if err := tt.Click("Snapshot folder"); err != nil {
		t.Fatal(err)
	}
	tt.Key(ui.Ctrl, ui.KeyA)
	tt.Type("/external/two")
	if a.selectedSnapshot != "" {
		t.Fatal("manual input retained contradictory saved identity")
	}
	if err := tt.Click("New restore destination"); err != nil {
		t.Fatal(err)
	}
	tt.Type("/new-target")
	if err := tt.Click("Verify and restore"); err != nil {
		t.Fatal(err)
	}
	if len(b.commands) != 1 || b.commands[0].SnapshotID != "" || b.commands[0].SnapshotPath != "/external/two" {
		t.Fatalf("portable external intent not distinguished from managed selection: %+v", b.commands)
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
