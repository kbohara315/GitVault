// Package desktop implements the native MyGo interface. Views only read copied
// state and submit bounded commands; workers own Git, networking, and disk I/O.
package desktop

import (
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"gitvault/internal/app"
)

var pages = [...]struct{ name, title string }{
	{"Overview", "Your repositories. Recoverable."},
	{"Repositories", "Choose what to protect"},
	{"Backups", "Create a verified snapshot"},
	{"Restore", "Recovery comes first"},
	{"Settings", "Configure your vault"},
}

type App struct {
	selected                                                           int
	backend                                                            app.Backend
	loaded                                                             bool
	draftRevision                                                      int64
	settings                                                           app.SettingsDraft
	schedule                                                           app.ScheduleDraft
	retention                                                          app.RetentionDraft
	source, token, search, snapshotPath, destination, selectedSnapshot string
	error                                                              string
}

func New() *App                               { return NewWithBackend(nil) }
func NewWithBackend(backend app.Backend) *App { return &App{backend: backend} }

func (a *App) send(cmd app.Command) {
	if a.backend == nil {
		a.error = "Application is not connected"
		return
	}
	if err := a.backend.Submit(cmd); err != nil {
		a.error = err.Error()
	} else {
		a.error = ""
	}
}

func (a *App) View(c *ui.Context) {
	s := app.ViewState{Status: "Application is not connected"}
	if a.backend != nil {
		s = a.backend.Snapshot()
	}
	if !a.loaded && s.Connected {
		a.settings = s.Settings
		a.schedule = s.Schedule
		a.retention = s.Retention
		a.draftRevision = s.Revision
		a.loaded = true
	}
	t := c.Theme()
	ui.Column(c).Fill().Padding(24).Gap(16).Children(func() {
		ui.Row(c).Gap(16).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "GitVault").FontSize(28).Bold()
			ui.Text(c, s.Status).TextColor(t.TextMuted)
			if s.CanCancel && ui.Button(c, "Cancel operation").Clicked() {
				a.send(app.Command{Kind: app.Cancel})
			}
		})
		ui.Row(c).Gap(8).Children(func() {
			for i, p := range pages {
				if ui.Button(c, p.name).Clicked() {
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

func field(c *ui.Context, label string, value *string) {
	ui.TextInput(c, value).Label(label).Placeholder(label).Width(560)
}

func (a *App) overview(c *ui.Context, s app.ViewState) {
	ui.Text(c, "Full Git bundles. Verified local recovery. Optional cloud copies.").Wrap()
	ui.Textf(c, "%d repositories · %d local snapshots", len(s.Repositories), len(s.Snapshots))
	if s.Account != "" {
		ui.Text(c, "Connected as "+s.Account)
	}
	ui.Text(c, "Coverage: Git history and references. LFS payloads, submodule repositories, and GitHub discussions are not included.").Wrap()
	if s.CatchUpOffer != "" {
		ui.Text(c, "A scheduled backup was missed. Run one catch-up?")
		ui.Row(c).Gap(8).Children(func() {
			if ui.Button(c, "Run catch-up").Disabled(s.Busy).Clicked() {
				a.send(app.Command{Kind: app.AcceptCatchUp})
			}
			if ui.Button(c, "Dismiss catch-up").Clicked() {
				a.send(app.Command{Kind: app.DismissCatchUp})
			}
		})
	}
	for _, task := range s.Tasks {
		ui.Text(c, task.Repository+" · "+task.Stage+" · "+task.Detail).Wrap()
		if task.TotalBytes > 0 {
			ui.Progress(c, float64(task.Bytes)/float64(task.TotalBytes)).Width(560)
		}
	}
}

func (a *App) repositories(c *ui.Context, s app.ViewState) {
	ui.Text(c, "Your token is kept in the native OS credential store, never in a backup.").Wrap()
	ui.TextInput(c, &a.token).Label("GitHub token").Placeholder("GitHub personal access token").Password().Width(560)
	ui.Row(c).Gap(8).Children(func() {
		if ui.Button(c, "Save GitHub token").Disabled(s.Busy || a.token == "").Clicked() {
			token := a.token
			a.token = ""
			a.send(app.Command{Kind: app.SaveToken, Token: token})
		}
		if ui.Button(c, "Refresh repositories").Disabled(s.Busy).Clicked() {
			a.send(app.Command{Kind: app.Discover})
		}
	})
	field(c, "Search repositories", &a.search)
	ui.Text(c, "Newly discovered repositories are not selected automatically.")
	for _, repo := range s.Repositories {
		if a.search != "" && !strings.Contains(strings.ToLower(repo.Name), strings.ToLower(a.search)) {
			continue
		}
		checked := repo.Selected
		ui.Row(c).Key(repo.ID).Gap(8).Children(func() {
			if ui.Checkbox(c, &checked, repo.Name).Disabled(s.Busy).Changed() {
				a.send(app.Command{Kind: app.SelectRepository, ExpectedRevision: s.Revision, RepositoryID: repo.ID, Selected: checked})
			}
			if repo.Private {
				ui.Text(c, "private").TextColor(c.Theme().TextMuted)
			}
		})
	}
}

func (a *App) backups(c *ui.Context, s app.ViewState) {
	field(c, "Local repository path", &a.source)
	field(c, "Backup folder", &a.settings.Root)
	ui.Row(c).Gap(8).Children(func() {
		if ui.PrimaryButton(c, "Back up local repository").Disabled(s.Busy || a.source == "" || a.settings.Root == "").Clicked() {
			a.send(app.Command{Kind: app.BackupLocal, Source: a.source, Root: a.settings.Root})
		}
		if ui.Button(c, "Back up selected repositories").Disabled(s.Busy).Clicked() {
			a.send(app.Command{Kind: app.BackupSelected})
		}
	})
	for _, snap := range s.Snapshots {
		ui.Column(c).Gap(6).Children(func() {
			ui.Text(c, snap.Repository+" · "+snap.CreatedAt.Local().Format("2006-01-02 15:04"))
			ui.Text(c, snap.Verification+" · cloud: "+snap.CloudStatus).TextColor(c.Theme().TextMuted)
			ui.Row(c).Gap(8).Children(func() {
				if ui.Button(c, "Restore "+short(snap.ID)).Clicked() {
					a.snapshotPath = snap.Path
					a.selectedSnapshot = snap.ID
					a.destination = ""
					a.selected = 3
				}
				if ui.Button(c, "Upload "+short(snap.ID)).Disabled(s.Busy || !snap.LocalReady).Clicked() {
					a.send(app.Command{Kind: app.Upload, SnapshotID: snap.ID})
				}
				pinned := snap.Pinned
				if ui.Checkbox(c, &pinned, "Pin "+short(snap.ID)).Disabled(s.Busy).Changed() {
					a.send(app.Command{Kind: app.PinSnapshot, SnapshotID: snap.ID, Selected: pinned})
				}
			})
		})
	}
	ui.Text(c, "Retention is preview-first. A policy never authorizes deletion.").Wrap()
	ui.Row(c).Gap(8).Children(func() {
		if ui.Button(c, "Preview retention").Disabled(s.Busy).Clicked() {
			a.send(app.Command{Kind: app.PreviewRetention, Retention: a.retention})
		}
		if ui.Button(c, "Delete exactly this preview").Disabled(s.Busy || s.RetentionPreviewID == "").Clicked() {
			a.send(app.Command{Kind: app.ExecuteRetention, SnapshotID: s.RetentionPreviewID, Confirm: true})
		}
	})
	if s.RetentionSummary != "" {
		ui.Text(c, s.RetentionSummary).Wrap()
	}
}

func (a *App) restore(c *ui.Context, s app.ViewState) {
	ui.Text(c, "Restore a verified full snapshot into a new bare repository. Existing destinations are never overwritten.").Wrap()
	if ui.TextInput(c, &a.snapshotPath).Label("Snapshot folder").Placeholder("Snapshot folder").Width(560).Changed() {
		a.selectedSnapshot = ""
		a.destination = ""
	}
	field(c, "New restore destination", &a.destination)
	if ui.PrimaryButton(c, "Verify and restore").Disabled(s.Busy || a.snapshotPath == "" || a.destination == "").Clicked() {
		a.send(app.Command{Kind: app.Restore, SnapshotID: a.selectedSnapshot, SnapshotPath: a.snapshotPath, Destination: a.destination})
	}
	if ui.Button(c, "List cloud snapshots").Disabled(s.Busy).Clicked() {
		a.send(app.Command{Kind: app.ListRemote})
	}
	for _, snap := range s.Remote {
		if ui.Button(c, "Download and restore "+short(snap.ID)).Disabled(s.Busy || a.destination == "").Clicked() {
			a.send(app.Command{Kind: app.DownloadRestore, SnapshotID: snap.ID, Destination: a.destination})
		}
	}
}

func (a *App) preferences(c *ui.Context, s app.ViewState) {
	if ui.Button(c, "Reload saved settings").Disabled(s.Busy).Clicked() {
		a.settings = s.Settings
		a.schedule = s.Schedule
		a.retention = s.Retention
		a.draftRevision = s.Revision
		a.error = ""
	}
	field(c, "Backup folder", &a.settings.Root)
	field(c, "Repository concurrency", &a.settings.Concurrency)
	ui.Checkbox(c, &a.settings.CloudEnabled, "Enable cloud copies")
	field(c, "Cloud provider (s3 or r2)", &a.settings.CloudProvider)
	field(c, "Bucket", &a.settings.CloudBucket)
	field(c, "Region", &a.settings.CloudRegion)
	field(c, "HTTPS endpoint (optional for S3)", &a.settings.CloudEndpoint)
	field(c, "Object prefix", &a.settings.CloudPrefix)
	ui.TextInput(c, &a.settings.CloudAccessKey).Label("Cloud access key").Password().Width(560)
	ui.TextInput(c, &a.settings.CloudSecretKey).Label("Cloud secret key").Password().Width(560)
	ui.Checkbox(c, &a.settings.CloudReadBack, "Read back cloud bytes after upload")
	ui.Text(c, "Cloud copies contain unencrypted Git history. Enable only a target you control.").Wrap()
	if ui.Button(c, "Save settings").Disabled(s.Busy).Clicked() {
		draft := a.settings
		a.settings.CloudAccessKey = ""
		a.settings.CloudSecretKey = ""
		a.send(app.Command{Kind: app.SaveSettings, ExpectedRevision: a.draftRevision, Settings: draft, Confirm: a.settings.CloudEnabled})
	}
	ui.Text(c, "In-app schedule").Bold()
	ui.Checkbox(c, &a.schedule.Enabled, "Enable scheduled backups")
	field(c, "Timezone (IANA)", &a.schedule.Timezone)
	field(c, "Time (HH:MM)", &a.schedule.LocalTime)
	field(c, "Frequency (daily, weekly, monthly)", &a.schedule.Frequency)
	field(c, "Weekday (Monday=1)", &a.schedule.Weekday)
	field(c, "Day of month", &a.schedule.Monthday)
	ui.Text(c, "DST: first valid instant in gaps, earlier occurrence in folds. Short months use their last day. Missed runs create one offer; the app must remain running.").Wrap()
	if ui.Button(c, "Save schedule").Disabled(s.Busy).Clicked() {
		a.send(app.Command{Kind: app.SaveSchedule, ExpectedRevision: a.draftRevision, Schedule: a.schedule, Confirm: true})
	}
	ui.Text(c, "Retention buckets (UTC)").Bold()
	field(c, "Keep daily", &a.retention.Daily)
	field(c, "Keep weekly", &a.retention.Weekly)
	field(c, "Keep monthly", &a.retention.Monthly)
}

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return fmt.Sprint(id)
}
