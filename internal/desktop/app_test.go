package desktop

import (
	"testing"

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
}

func (b *testBackend) Snapshot() app.ViewState { return b.state }
func (b *testBackend) Submit(c app.Command) error {
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

func TestUnavailableFeaturesDoNotExposeControls(t *testing.T) {
	b := &testBackend{state: app.ViewState{Connected: true}}
	a := NewWithBackend(b)
	tt := ui.NewTester(a.View, 1000, 850)
	if err := tt.Click("Repositories"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("GitHub discovery, credential setup, and repository selection are not available in this build.") {
		t.Fatalf("missing explicit unavailable status: %v", tt.Texts())
	}
	for _, control := range []string{"Save GitHub token", "Select all", "owner/repo"} {
		if tt.HasText(control) {
			t.Errorf("unimplemented control %q was exposed", control)
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
