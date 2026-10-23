// Package app coordinates user intents and exposes immutable presentation data.
// No native UI types belong in this package.
package app

import "time"

type CommandKind string

const (
	Refresh          CommandKind = "refresh"
	BindRoot         CommandKind = "bind_root"
	SaveToken        CommandKind = "save_token"
	RemoveToken      CommandKind = "remove_token"
	Discover         CommandKind = "discover"
	SelectRepository CommandKind = "select_repository"
	BackupLocal      CommandKind = "backup_local"
	BackupSelected   CommandKind = "backup_selected"
	Cancel           CommandKind = "cancel"
	Restore          CommandKind = "restore"
	SaveSettings     CommandKind = "save_settings"
	Upload           CommandKind = "upload"
	ListRemote       CommandKind = "list_remote"
	DownloadRestore  CommandKind = "download_restore"
	PinSnapshot      CommandKind = "pin_snapshot"
	PreviewRetention CommandKind = "preview_retention"
	ExecuteRetention CommandKind = "execute_retention"
	SaveSchedule     CommandKind = "save_schedule"
	AcceptCatchUp    CommandKind = "accept_catch_up"
	DismissCatchUp   CommandKind = "dismiss_catch_up"
)

// Command is copied into a bounded admission queue. Sensitive fields are never
// serialized to settings/history/logs; the receiver consumes and discards them.
type Command struct {
	ExpectedRevision                                                  int64
	Kind                                                              CommandKind
	Source, Root, SnapshotID, SnapshotPath, Destination, RepositoryID string
	Selected, Confirm                                                 bool
	Secret                                                            []byte
	Settings                                                          SettingsDraft
	Schedule                                                          ScheduleDraft
	Retention                                                         RetentionDraft
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
	ID, Name, Description   string
	Private, Fork, Selected bool
}

type SnapshotView struct {
	ID, Repository, Path, Verification, CloudStatus string
	CreatedAt                                       time.Time
	LocalReady, Pinned                              bool
}

type TaskView struct {
	Repository, Stage, Detail string
	Bytes, TotalBytes         int64
}

type ViewState struct {
	Revision                                                  int64
	Busy, CanCancel, CanBindRoot, CanStartLocal, Connected    bool
	CredentialConfigured, CanManageCredential, CredentialBusy bool
	Status, Notice, Error, Account                            string
	Settings                                                  SettingsDraft
	Schedule                                                  ScheduleDraft
	Retention                                                 RetentionDraft
	Repositories                                              []RepositoryView
	Snapshots                                                 []SnapshotView
	Remote                                                    []SnapshotView
	Tasks                                                     []TaskView
	RetentionPreviewID, RetentionSummary, CatchUpOffer        string
}

// Backend's snapshot is an in-memory copy, not a database/network query. Submit
// is bounded and nonblocking; its worker owns all blocking application effects.
type Backend interface {
	Snapshot() ViewState
	Submit(Command) error
}
