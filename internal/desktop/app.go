// Package desktop implements the native MyGo interface. Views only read copied
// state and submit bounded commands; workers own Git, networking, and disk I/O.
package desktop

import (
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"gitvault/internal/app"
	"gitvault/internal/config"
)

var pages = [...]struct{ name, title string }{
	{"Overview", "Your repositories. Recoverable."},
	{"Repositories", "Choose what to protect"},
	{"Backups", "Create a verified snapshot"},
	{"Restore", "Recovery comes first"},
	{"Settings", "Configure your vault"},
}

type App struct {
	selected      int
	backend       app.Backend
	loaded        bool
	draftRevision int64
	settingsDirty bool
	rootDirty     bool
	settings      app.SettingsDraft
	filters       config.GitHubFilters
	source        string
	githubToken   secretDraft
	error         string
	filtersDirty  bool
}

func New() *App                               { return NewWithBackend(nil) }
func NewWithBackend(backend app.Backend) *App { return &App{backend: backend} }

// send reports whether the bounded admission queue accepted the command. View
// text never claims that an operation succeeded; workers publish that truth.
func (a *App) send(cmd app.Command) bool {
	defer clear(cmd.Secret)
	if a.backend == nil {
		a.error = "Application is not connected"
		return false
	}
	if err := a.backend.Submit(cmd); err != nil {
		a.error = err.Error()
		return false
	}
	a.error = ""
	return true
}

// observe refreshes editable drafts only when the backend has advanced and the
// user has no unsaved edits. A stale save therefore fails its revision check
// instead of silently overwriting a newer configuration.
func (a *App) observe(s app.ViewState) {
	if !s.CanManageCredential || s.Busy || s.CredentialBusy {
		a.githubToken.Clear()
	}
	if !s.Connected {
		return
	}
	if !a.loaded {
		a.sync(s)
		a.loaded = true
		return
	}
	if s.Revision == a.draftRevision {
		if !a.filtersDirty {
			a.filters = s.Filters
		}
		return
	}
	if !a.settingsDirty && !a.rootDirty && !a.filtersDirty {
		a.sync(s)
		return
	}
	// Refresh only the untouched part; a dirty draft keeps the revision it was
	// based on so its compare-and-swap cannot silently overwrite a newer edit.
	if !a.settingsDirty {
		a.settings.Concurrency = s.Settings.Concurrency
	}
	if !a.rootDirty {
		a.settings.Root = s.Settings.Root
	}
	if a.filtersDirty {
		if a.filters == s.Filters {
			a.filtersDirty = false
		} else {
			a.filters = s.Filters
			a.filtersDirty = false
		}
		if !a.settingsDirty && !a.rootDirty {
			a.draftRevision = s.Revision
		}
	} else {
		a.filters = s.Filters
	}
}

func (a *App) sync(s app.ViewState) {
	a.settings = s.Settings
	a.filters = s.Filters
	a.draftRevision = s.Revision
	a.settingsDirty = false
	a.rootDirty = false
	a.filtersDirty = false
}

func (a *App) View(c *ui.Context) {
	s := app.ViewState{Status: "Application is not connected"}
	if a.backend != nil {
		s = a.backend.Snapshot()
	}
	a.observe(s)
	t := c.Theme()
	ui.Column(c).Fill().Padding(24).Gap(16).Children(func() {
		ui.Row(c).Gap(16).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "GitVault").FontSize(28).Bold()
			ui.Text(c, s.Status).TextColor(t.TextMuted)
			cancelLabel := "Cancel operation"
			if s.DiscoveryBusy {
				cancelLabel = "Cancel repository discovery"
			}
			if s.CanCancel && ui.Button(c, cancelLabel).Clicked() {
				a.send(app.Command{Kind: app.Cancel})
			}
		})
		ui.Row(c).Gap(8).Children(func() {
			for i, p := range pages {
				if ui.Button(c, p.name).Clicked() {
					if a.selected == 1 && i != 1 {
						a.githubToken.Clear()
					}
					a.selected = i
				}
			}
		})
		if a.error != "" {
			ui.Text(c, a.error).Wrap()
		}
		if s.Error != "" {
			ui.Text(c, s.Error).Wrap()
		}
		if s.Notice != "" {
			ui.Text(c, s.Notice).TextColor(t.TextMuted).Wrap()
		}
		ui.Scroll(c).Fill().Children(func() {
			ui.Column(c).Gap(14).Children(func() {
				ui.Text(c, pages[a.selected].title).FontSize(24).Bold()
				switch a.selected {
				case 0:
					a.overview(c, s)
				case 1:
					a.repositories(c, s)
				case 2:
					a.backups(c, s)
				case 3:
					a.restore(c, s)
				case 4:
					a.preferences(c, s)
				}
			})
		})
	})
	// Only painting is scheduled here; this is not the backup scheduler.
	c.After(200 * time.Millisecond)
}

// ClearSensitiveDrafts overwrites the UI-owned byte draft after the native loop
// stops. This is best-effort; it cannot erase transient strings created inside
// the native text-input implementation or copies already made by the OS.
func (a *App) ClearSensitiveDrafts() { a.githubToken.Clear() }

func field(c *ui.Context, label string, value *string) bool {
	return ui.TextInput(c, value).Label(label).Placeholder(label).Width(560).Changed()
}

func (a *App) overview(c *ui.Context, s app.ViewState) {
	ui.Text(c, "Full Git bundles. Verified local recovery. No account is required for local backups.").Wrap()
	ui.Textf(c, "%d repositories · %d local snapshots", len(s.Repositories), len(s.Snapshots))
	if s.Account != "" {
		ui.Text(c, "Connected as "+s.Account)
	}
	ui.Text(c, "Coverage: Git history and references. LFS payloads, submodule repositories, and GitHub discussions are not included.").Wrap()
	if s.Settings.Root == "" {
		ui.Text(c, "No backup folder is bound. Bind an absolute local folder in Settings before starting a local backup.").Wrap()
	} else {
		ui.Text(c, "Backup folder: "+s.Settings.Root).Wrap()
	}
	ui.Text(c, "Local backups use only the bound folder. Cloud copies, scheduled runs, retention actions, and restore are not available in this build.").Wrap()
	if s.CatchUpOffer != "" {
		ui.Text(c, "Scheduled backup offers are not available in this build.").TextColor(c.Theme().TextMuted).Wrap()
	}
	for _, task := range s.Tasks {
		parts := make([]string, 0, 3)
		for _, part := range []string{task.Repository, task.Stage, task.Detail} {
			if part != "" {
				parts = append(parts, part)
			}
		}
		if len(parts) == 0 {
			parts = append(parts, "Task")
		}
		ui.Text(c, strings.Join(parts, " · ")).Wrap()
		if task.TotalBytes > 0 {
			ui.Progress(c, float64(task.Bytes)/float64(task.TotalBytes)).Width(560)
		}
	}
}

func (a *App) repositories(c *ui.Context, s app.ViewState) {
	if !s.Connected {
		ui.Text(c, "GitHub credential management is unavailable while the application is not connected.").Wrap()
	} else if !s.CanManageCredential {
		if s.CredentialBusy && !s.Busy {
			ui.Text(c, "A previous OS credential prompt may still be pending. GitVault cannot cancel it; wait for it to finish.").Wrap()
			if ui.Button(c, "Check secure-store status").Clicked() {
				a.send(app.Command{Kind: app.Refresh})
			}
		} else if s.Busy || s.CredentialBusy {
			ui.Text(c, "GitHub credential changes are unavailable while another operation is active.").Wrap()
		} else {
			ui.Text(c, "Native GitHub credential setup is not available because the OS secure store is unavailable.").Wrap()
		}
	} else {
		if s.CredentialConfigured {
			ui.Text(c, "A GitHub credential is configured in OS secure storage. Its value is never shown here.").Wrap()
		} else {
			ui.Text(c, "No GitHub credential is configured.").Wrap()
		}
		a.githubTokenInput(c)
		if ui.Button(c, "Save or replace GitHub token").Disabled(s.Busy || s.CredentialBusy || a.githubToken.Empty()).Clicked() {
			secret := a.githubToken.CopyBytes()
			a.githubToken.Clear()
			a.send(app.Command{Kind: app.SaveToken, ExpectedRevision: s.Revision, Secret: secret})
			clear(secret)
		}
		if s.CredentialConfigured {
			if ui.Button(c, "Remove configured GitHub token").Disabled(s.Busy || s.CredentialBusy).Clicked() {
				a.send(app.Command{Kind: app.RemoveToken, ExpectedRevision: s.Revision})
			}
		}
		ui.Text(c, "Use the least access needed and limit fine-grained tokens to repositories you choose. Organization policy may require approval or block personal access tokens. Replacing a token creates a new generation; earlier generations remain for already accepted work. Removal clears only the configured generation.").Wrap()
	}
	if s.Connected {
		if s.CanDiscover && ui.Button(c, "Discover GitHub repositories").Clicked() {
			a.send(app.Command{Kind: app.Discover, ExpectedRevision: s.Revision})
		}
		if s.DiscoveryBusy {
			ui.Text(c, "Discovery is running in the background. Cancellation stops the request or bounded wait.").TextColor(c.Theme().TextMuted).Wrap()
		}
		ui.Text(c, "Display filters (these never change repository selection)").Bold()
		a.repositoryFilters(c, s)
		if s.Account == "" {
			ui.Text(c, "No GitHub inventory has been discovered yet.").TextColor(c.Theme().TextMuted)
		} else if s.InventoryComplete {
			ui.Text(c, "Complete inventory for GitHub account "+s.Account+".").TextColor(c.Theme().TextMuted).Wrap()
		} else {
			ui.Text(c, "Partial inventory for GitHub account "+s.Account+". Unseen repositories are not marked inaccessible.").TextColor(c.Theme().Warning).Wrap()
		}
	}
	ui.Text(c, "Repository selection is explicit and saved by stable GitHub ID. Newly discovered repositories stay unselected; discovery filters do not change saved selection.").Wrap()
	ui.Text(c, "Local backups use a filesystem path and do not require a GitHub account or selection. Remote backup is not available in this build.").Wrap()
	if len(s.Repositories) == 0 {
		ui.Text(c, "No repositories match the current display filters, or no inventory is saved.").TextColor(c.Theme().TextMuted).Wrap()
		return
	}
	for _, repo := range s.Repositories {
		selected := repo.Selected
		checkbox := ui.Checkbox(c, &selected, repo.Name)
		if !repo.Available && !repo.Selected {
			checkbox = checkbox.Disabled(true)
		}
		if checkbox.Changed() && a.backend != nil {
			if err := a.backend.Submit(app.Command{Kind: app.SelectRepository, ExpectedRevision: s.Revision, RepositoryID: repo.ID, Selected: selected}); err != nil {
				a.error = err.Error()
			} else {
				a.error = ""
			}
		}
		line := repo.Availability
		if repo.Private {
			line += " · private"
		}
		if repo.Fork {
			line += " · fork"
		}
		if repo.Description != "" {
			line += " · " + repo.Description
		}
		ui.Text(c, line).Wrap()
	}
}

func (a *App) repositoryFilters(c *ui.Context, s app.ViewState) {
	changed := false
	changed = ui.Checkbox(c, &a.filters.Owned, "Owned repositories").Changed() || changed
	changed = ui.Checkbox(c, &a.filters.Organizations, "Organization repositories").Changed() || changed
	changed = ui.Checkbox(c, &a.filters.Collaborating, "Other user repositories").Changed() || changed
	changed = ui.Checkbox(c, &a.filters.ExcludeForks, "Exclude forks from view").Changed() || changed
	if !changed {
		return
	}
	a.filtersDirty = true
	if a.backend == nil {
		a.filters = s.Filters
		a.filtersDirty = false
		return
	}
	if err := a.backend.Submit(app.Command{Kind: app.SetRepositoryFilters, ExpectedRevision: s.Revision, Filters: a.filters}); err != nil {
		a.filters = s.Filters
		a.filtersDirty = false
		a.error = err.Error()
	} else {
		a.error = ""
	}
}

func (a *App) githubTokenInput(c *ui.Context) {
	t := c.Theme()
	display := a.githubToken.Mask()
	ui.Box(c).
		Width(secretInputWidth).
		Height(42).
		Padding(10).
		Background(t.Surface).
		Border(1, t.Border).
		Radius(t.Radius).
		Label("GitHub personal access token").
		HandleTextInput(&a.githubToken).
		InputMethod(false).
		HandleInput(a.githubToken.HandleInput).
		FocusRing(true).
		Children(func() {
			if a.githubToken.Empty() {
				ui.Text(c, "Paste or type a token (masked)").FontSize(secretInputFontSize).TextColor(t.TextMuted)
				return
			}
			ui.Text(c, display).FontSize(secretInputFontSize)
		})
}

// localDestination presents the one managed folder explicitly. Editing the
// input does not change where a backup starts until the idle bind succeeds.
func (a *App) localDestination(c *ui.Context, s app.ViewState) {
	if s.Settings.Root == "" {
		ui.Text(c, "Managed backup folder: not bound").TextColor(c.Theme().TextMuted)
	} else {
		ui.Text(c, "Managed backup folder: "+s.Settings.Root).Wrap()
	}
	if field(c, "Backup folder", &a.settings.Root) {
		a.rootDirty = true
	}
	if ui.Button(c, "Bind backup folder").Disabled(s.Busy || !s.CanBindRoot || strings.TrimSpace(a.settings.Root) == "").Clicked() {
		if a.send(app.Command{Kind: app.BindRoot, ExpectedRevision: a.draftRevision, Root: a.settings.Root}) {
			a.rootDirty = false
		}
	}
	if !s.CanBindRoot {
		ui.Text(c, "Changing the backup folder is unavailable while an operation or startup reconciliation is active.").TextColor(c.Theme().TextMuted).Wrap()
	}
	ui.Text(c, "Editing the field does not change the destination. Binding validates the folder before it becomes the managed destination; a backup always uses the bound folder. Existing snapshots are never moved or deleted.").Wrap()
}

func (a *App) backups(c *ui.Context, s app.ViewState) {
	ui.Text(c, "Create a full verified bundle from a local Git repository. The bundle is written under the managed backup folder.").Wrap()
	a.localDestination(c, s)
	ui.Text(c, "A backup in progress retains the folder and concurrency revision it was admitted with.").Wrap()
	field(c, "Local repository path", &a.source)
	if s.Settings.Concurrency != "" {
		ui.Text(c, "Saved repository concurrency: "+s.Settings.Concurrency).TextColor(c.Theme().TextMuted)
	}
	if !s.CanStartLocal {
		ui.Text(c, "A new local backup can start only when no operation is active and startup checks have completed.").TextColor(c.Theme().TextMuted).Wrap()
	}
	if ui.PrimaryButton(c, "Back up local repository").Disabled(s.Busy || !s.CanStartLocal || s.Settings.Root == "" || strings.TrimSpace(a.source) == "").Clicked() {
		a.send(app.Command{Kind: app.BackupLocal, Source: a.source})
	}
	ui.Text(c, "A second backup request is rejected while one is active; it is not queued.").Wrap()

	ui.Text(c, "Local snapshots").Bold()
	if len(s.Snapshots) == 0 {
		ui.Text(c, "No local snapshots have been recorded.").TextColor(c.Theme().TextMuted)
	}
	for _, snap := range s.Snapshots {
		label := snap.Repository
		if label == "" {
			label = "local snapshot"
		}
		ui.Text(c, label+" · "+short(snap.ID)).Wrap()
		if !snap.CreatedAt.IsZero() {
			ui.Text(c, snap.CreatedAt.Local().Format("2006-01-02 15:04")).TextColor(c.Theme().TextMuted)
		}
		state := "not ready"
		if snap.LocalReady {
			state = "ready"
		}
		detail := "local: " + state
		if snap.Verification != "" {
			detail += " · verification: " + snap.Verification
		}
		if snap.Path != "" {
			detail += " · " + snap.Path
		}
		ui.Text(c, detail).TextColor(c.Theme().TextMuted).Wrap()
	}
	ui.Text(c, "Snapshot inspection, import, upload, and restore are not available in this build.").Wrap()
}

func (a *App) restore(c *ui.Context, _ app.ViewState) {
	ui.Text(c, "Restore is not available in this build.").Bold()
	ui.Text(c, "Local recovery will restore a verified full snapshot into a new bare destination. Existing destinations are never overwritten.").Wrap()
	ui.Text(c, "No restore command is exposed until that recovery workflow is implemented; no saved folder is treated as restore permission.").Wrap()
}

func (a *App) preferences(c *ui.Context, s app.ViewState) {
	if ui.Button(c, "Reload saved settings").Disabled(s.Busy).Clicked() {
		a.sync(s)
		a.error = ""
	}
	ui.Text(c, "Local backup").Bold()
	a.localDestination(c, s)
	if field(c, "Repository concurrency", &a.settings.Concurrency) {
		a.settingsDirty = true
	}
	ui.Text(c, "Repository concurrency is 1–8. New runs use the saved settings revision; an active run keeps the revision it already captured.").Wrap()
	if ui.Button(c, "Save settings").Disabled(s.Busy || strings.TrimSpace(a.settings.Concurrency) == "").Clicked() {
		draft := app.SettingsDraft{Concurrency: a.settings.Concurrency}
		if a.send(app.Command{Kind: app.SaveSettings, ExpectedRevision: a.draftRevision, Settings: draft}) {
			a.settingsDirty = false
		}
	}
	ui.Text(c, "This save changes non-root local settings only. Use Bind backup folder to change the managed folder explicitly.").Wrap()

	ui.Text(c, "Unavailable in this build").Bold()
	ui.Text(c, "GitHub discovery and explicit selection are available on the Repositories page. GitHub backups, cloud copies, schedules, retention preview or deletion, and restore are not implemented yet.").Wrap()
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return fmt.Sprint(id)
}
