// Package app coordinates user intents and exposes immutable presentation data.
// No native UI types belong in this package.
package app

import (
	"time"

	"gitvault/internal/config"
)

type CommandKind string

const (
	Refresh              CommandKind = "refresh"
	BindRoot             CommandKind = "bind_root"
	SaveToken            CommandKind = "save_token"
	RemoveToken          CommandKind = "remove_token"
	Discover             CommandKind = "discover"
	SelectRepository     CommandKind = "select_repository"
	SetRepositoryFilters CommandKind = "set_repository_filters"
	BackupLocal          CommandKind = "backup_local"
	BackupSelected       CommandKind = "backup_selected"
	RetryGitHubTask      CommandKind = "retry_github_task"
	Cancel               CommandKind = "cancel"
	Restore              CommandKind = "restore"
	SaveSettings         CommandKind = "save_settings"
	Upload               CommandKind = "upload"
	ListRemote           CommandKind = "list_remote"
	DownloadRestore      CommandKind = "download_restore"
	PinSnapshot          CommandKind = "pin_snapshot"
	PreviewRetention     CommandKind = "preview_retention"
	ExecuteRetention     CommandKind = "execute_retention"
	SaveSchedule         CommandKind = "save_schedule"
	AcceptCatchUp        CommandKind = "accept_catch_up"
	DismissCatchUp       CommandKind = "dismiss_catch_up"
)

// Command is copied into a bounded admission queue. Sensitive fields are never
// serialized to settings/history/logs; the receiver consumes and discards them.
type Command struct {
	ExpectedRevision                                                         int64
	Kind                                                                     CommandKind
	Source, Root, SnapshotID, SnapshotPath, Destination, RepositoryID, RunID string
	Selected, Confirm, CreateRoot                                            bool
	Secret                                                                   []byte
	Filters                                                                  config.GitHubFilters
	Settings                                                                 SettingsDraft
	Schedule                                                                 ScheduleDraft
	Retention                                                                RetentionDraft
}

type SettingsDraft struct {
	Root, Concurrency string
}

type ScheduleDraft struct {
	Enabled                                           bool
	Timezone, LocalTime, Frequency, Weekday, Monthday string
}

type RetentionDraft struct{ Daily, Weekly, Monthly string }

type RepositoryView struct {
	ID, Name, Description, Availability string
	Private, Fork, Selected, Available  bool
}

type SnapshotView struct {
	ID, Repository, Path, Verification, CloudStatus string
	CreatedAt                                       time.Time
	LocalReady, Pinned                              bool
}

type TaskView struct {
	Repository, Stage, Detail string
	RepositoryID              string
	RunID                     string
	CanRetry                  bool
	Bytes, TotalBytes         int64
}

type LogEntry struct {
	At      time.Time
	Level   string
	Message string
}

type ViewState struct {
	Revision                                                                 int64
	Busy, CanCancel, CanBindRoot, CanStartLocal, CanStartSelected, Connected bool
	CredentialConfigured, CanManageCredential, CredentialBusy                bool
	DiscoveryBusy, CanDiscover, InventoryComplete                            bool
	Status, Notice, Error, Account                                           string
	Filters                                                                  config.GitHubFilters
	Settings                                                                 SettingsDraft
	Schedule                                                                 ScheduleDraft
	Retention                                                                RetentionDraft
	Repositories                                                             []RepositoryView
	Snapshots                                                                []SnapshotView
	Remote                                                                   []SnapshotView
	Tasks                                                                    []TaskView
	Logs                                                                     []LogEntry
	RetentionPreviewID, RetentionSummary, CatchUpOffer                       string
	SelectedRepositoryCount                                                  int
}

// Backend's snapshot is an in-memory copy, not a database/network query. Submit
// is bounded and nonblocking; its worker owns all blocking application effects.
type Backend interface {
	Snapshot() ViewState
	Submit(Command) error
}
