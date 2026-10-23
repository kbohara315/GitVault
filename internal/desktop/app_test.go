package desktop

import (
	"bytes"
	"strings"
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
