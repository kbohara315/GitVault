# Coordinator seam

Only `internal/config/**` and `internal/store/**` were changed in this partition.
No app, engine, UI, dependency, shared-document, or tracker edits are included.

## Configuration

`config.Default() Config`, `Decode(io.Reader) (Config,error)`,
`Normalize(Config) (Config,error)`, and `Import(io.Reader) (Config,error)` implement
schema one. Omitted fields receive defaults only during `Decode`; programmatic
edits must start from `Default` or an existing revision. The schema version is
mandatory on input. There is no supported schema-zero migration. Import disables
schedule/cloud execution and resets retention to `preview_only`.

`Duration` wraps `time.Duration`; use `duration.Value()` at composition.
Serialization requires whole milliseconds and emits integral ms/s/m/h values.
`Config.Clone()` deep-copies every pointer/slice. `Revision.ID()`, `CreatedAt()`,
and `Config()` expose immutable captured revisions and independent copies.

Configuration validation does **not** establish filesystem identity, resolve
secret handles, grant disclosure consent, confirm a timezone on the user's
behalf, or check native tray capability. Those are coordinator admission gates.
Monthly days 29–31 are valid; occurrence computation must apply the last-valid-
day rule in the scheduler. No automatic scheduling implementation is in store.

## SQLite

```go
s, err := store.Open(ctx, absoluteApplicationDataDBPath)
// or OpenWithInstallation(ctx, path, stableInstallationID)
head, err := s.CurrentConfig(ctx)
edit := head.Config()
edit.Backup.RepositoryConcurrency = 3
next, err := s.SaveConfig(ctx, head.ID(), edit) // ErrConflict on a stale CAS
_ = next
```

`ConfigRevision(ctx,id)` retrieves history. Revisions, selection/target/schedule
projections, and the sole `settings_head` change transactionally. Target IDs may
never be reused for a different destination, including after removal. Credential
rotation applies next run and does not require idle. Root changes and changes to
the target-identity set require no active run or lease. Root changes do not move
files or implicitly bind a marker. There is no public raw database handle.

Open requires a clean absolute file path. The caller must establish a trusted,
local, non-synchronized application-data directory and process ownership. Store
canonicalizes its parent, rejects a symlink/nonregular database, sets mode 0600,
and persists store/installation/canonical-data-directory identity. Relocated or
foreign installation databases fail closed. Migration uses a transactional
checksummed embedded ledger; missing/altered/future histories fail closed.
SQLite serializes migration and admission using immediate transactions. Each
connection receives foreign keys, 5000ms busy timeout, WAL, synchronous FULL, and
trusted_schema OFF through an encoded internally constructed URI. One open/idle
connection is allowed. Native platform/filesystem qualification remains pending.

## Managed identity and runs

Under the coordinator's OS root lock, construct `RootBinding` from the validated
marker and `s.Identity()`. `BindRoot(ctx,binding)` persists an explicit idle bind;
`ValidateRoot(ctx,binding)` checks every identity field, not just the path.
`RootBinding(ctx,id)` returns only the active locally owned binding. A different
filesystem requires a new root ID; historical roots are retained, not adopted.
These APIs do not create markers, take OS locks, or inspect root files.

```go
run, err := s.BeginRun(ctx, store.RunIntent{
    ID: operationID, Trigger: "manual", Kind: "local_backup",
    RootID: binding.ID, RepositoryIDs: []string{stableRepositoryID},
    SourcePath: absoluteSourcePath,
})
// Commit of preparing precedes launching any discovery/engine goroutine.
err = s.SetRunState(ctx, run.ID, "running", "")
// Execute outside transactions; persist tasks/artifacts even if UI is absent.
err = s.FinishRun(ctx, run.ID, "succeeded", "")
```

`BeginRun` supports `local_backup`, `github_backup`, `restore`, `upload_retry`;
triggers are `manual`, `scheduled`, `catch_up`, `retry`. Github selection is
captured from the same head transaction. Local backup takes explicit IDs. A
second active run returns `*BusyError` with `RunID`, across database instances.
Managed `local_backup` and `github_backup` admission requires a nonempty RootID
and checks it against the active binding. Restore and upload retry may omit it
when their operation contract does not use a managed backup root. Admission
does not perform filesystem marker, secret, consent, or engine policy checks.

`ValidRunTransition` is the durable transition vocabulary. `GetRun(ctx,id)` and
`QueryRuns(ctx,limit)` return intent and immutable configuration. Reaping and
root-lock release must precede terminal admission ownership release. After
proving old ownership gone, `ReconcileInterrupted(ctx)` marks active runs and
pending task outcomes interrupted, retaining publication/cloud facts. It never
deletes files, releases leases, inspects artifacts, or adopts unknown output.

## Facts and protection

- `CreateTask(ctx,Task)`, `UpdateTask(ctx,id,TaskUpdate)`, `ListTasks(ctx,runID,limit)`.
  Task identity must be explicitly selected in the run. Outcome, stage,
  indexed SnapshotID, retained ArtifactID/ArtifactPath, PublicationState, and
  CloudOutcome remain separate. Keep ArtifactID/ArtifactPath even if indexing
  failed. Structured ErrorCode is not arbitrary command output. Terminal tasks
  are immutable. Known publication identity and an existing index link cannot
  be erased by a later update. Retries use a new admitted run/task.
- `IndexSnapshot(ctx,Snapshot)`, `GetSnapshot(ctx,id)`,
  `ListSnapshots(ctx,repositoryID,limit)`. Empty repositoryID queries all history.
  Ready requires known publication and all six verification checks passed.
  SQLite records evidence; the engine must actually validate the bytes first.
  Duplicate index calls are idempotent only for exactly matching facts; changed
  bytes or path collisions return ErrConflict.
- `MarkSnapshotState(ctx,id,state)` accepts missing/corrupt/abandoned only. It
  cannot promote readiness, authorize deletion, or remove any bytes.
- `SetPin(ctx,snapshotID,pinned)`, `ListPins(ctx,limit)`;
  `AcquireLease(ctx,Lease)`, `ReleaseLease(ctx,id)`, `ListLeases(ctx,limit)`.
  These validate active local root authority. Leases require ready inputs;
  expiration is evidence, never automatic permission to delete. Reconciliation
  leaves leases protected until the coordinator explicitly resolves owners.

Limits: runs 1–1000; snapshots/tasks/pins/leases 1–10000. Lists are bounded, not
an unbounded inventory API. Larger retention inventories need explicit paging
before retention is enabled. There are no deletion intents, cloud receipts,
scheduled occurrence claims, remote effects, or automatic recovery filesystem
actions in this partition. Those require their consumers and acceptance tests.

## Actual scoped checks

`gofmt -l internal/config internal/store` (empty), `go vet`, `go build`, and
`go test -race ./internal/config ./internal/store -count=1` pass on this Linux
host. Both packages also pass ten consecutive non-race executions. All required
names exist and pass without skip: TestConfigBoundsAndMigration,
TestConfigImmutableRevision, TestMigrationsAtomic, TestFutureSchemaRejected,
TestSQLiteConnectionPolicy. Additional real-file tests cover root authority,
task/artifact/pin/lease reopen, interrupted recovery, projection rollback,
atomic admission during edit, historical target identity, and exhaustive run
transitions. Test-only failure hooks are private; the external config fixture
uses a temporary SQLite abort trigger to fail the head write after projections.
No mocked SQL, native credentials, real repositories, or paid cloud were used.
