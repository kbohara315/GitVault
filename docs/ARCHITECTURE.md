# Architecture

**Status: target design.** I01–I07 are accepted for their documented Linux/amd64 implementation scopes. I07 includes GitHub REST discovery and explicit selection; Git acquisition and later integrations remain incomplete. The design below describes the full target, not the current qualification level. [IMPLEMENTATION.md](IMPLEMENTATION.md) is the canonical execution entry point; [MODULES.md](MODULES.md) defines module ownership and composition. Existing numbered plans are historical, not competing execution instructions.

## System context

```text
                         User
                          |
                    Native MyGo UI
                          |
Scheduler ----------> Job manager <---------- SQLite metadata
                          |                         |
                          |                  Snapshot records
                          v
                   Background engine <------ OS secret store
                          |
           +--------------+---------------+
           |              |               |
           v              v               v
       GitHub API     Git executable   Object storage
       discovery      fetch/bundle     optional S3/R2
                          |
                          v
                  Local backup root
                  mirrors + snapshots

Restore UI --------> Restore service -------> New destination
                          ^
                          |
                  Verified snapshot
```

Native Go UI calls the run coordinator through its interface. The coordinator records intent and outcomes; the recovery module owns artifact interpretation and safe effects. [CONTEXT.md](../CONTEXT.md) defines the domain language.

## Runtime decision

Start with one desktop process. Run engine work in bounded background goroutines, never in view callbacks. Git itself runs as a child process. Keep the engine independent of MyGo so a separate worker process can be added later if profiling or crash isolation justifies it.

Introduce a separate worker process only when measured requirements justify the added lifecycle and communication complexity.

## Module ownership

Begin with one deep `internal/backup` module: manifest, controlled Git execution, snapshot creation, verification, private staging, restore, and no-replace publication share one interpretation. Keep process/filesystem adapters private until real variation justifies extraction.

Introduce durable `internal/app` coordination, `internal/store` persistence, and typed configuration only when their implementation slices require them. Discovery, secret access, cloud copying, retention, and scheduling remain separate modules with real ownership; do not create empty directories in advance.

Composition:

```text
native UI / tray / scheduler
              |
              v
       run coordinator -----> SQLite adapter
              |
       +------+-------+
       |      |       |
       v      v       v
 discovery  recovery  cloud copies
              |
       controlled Git + filesystem
```

Keep MyGo types out of the recovery module. Compose concrete adapters at startup. Add an interface at a consumer-owned seam when a real adapter or test requires it; do not build a universal registry/container. The interface is the test surface: prefer full offline round trips over mocks of each Git step.

Typed [configuration](CONFIGURATION.md) owns defaults and validation. Capture immutable effective values at admission; never read mutable global settings during a run. Safety invariants have no bypass knobs.

## Job ownership and concurrency

- Route all backup triggers through one job manager.
- Acquire run ownership before asynchronous discovery, not after it.
- Use an OS-backed lock for the backup root to cover multiple application instances. Do not rely only on a PID file. Validate root identity and overlapping paths; release one manages one active root per installation, with explicit idle rebind rather than implicit migration.
- Restrict a mirror to one writer. Never update a mirror while bundling it.
- Serialize initial backup runs. Bound repository concurrency within a run; default to a conservative value such as two.
- Coordinate retention with backup and restore leases so it cannot delete active inputs.
- A run owns its context. Cancellation signals the engine, waits for termination, records terminal state, then releases ownership.

```text
preparing -> running -> succeeded | partial | failed
    |           |
    +-----------+--> cancelling -> cancelled
    |
    +---- owner lost / restart reconciliation -> interrupted
```

Admission persists `preparing` before background work. Release one rejects a second backup request with a typed busy result; it has no multi-run queue. Idle is a UI observation, not a durable run state. Tasks, published artifacts, and cloud copies retain separate outcome dimensions.

## UI threading

Workers must not modify view state. Send immutable progress events through a bounded channel. One bridge applies bounded batches using the pinned MyGo main-thread interface, then invalidates the window. Coalesce progress. Persist terminal outcomes independently and let the UI query durable truth; notification delivery must not block cleanup or termination. [MODULES.md](MODULES.md) specifies synchronous `RunOnMain` and shutdown deadlock avoidance.

Progress events include run ID, repository task ID, stage, sequence number, and optional measured byte counts. Do not send credential-bearing command output.

## Backup lifecycle

```text
Validate -> Lock -> Discover -> Select
                                  |
                    bounded repository tasks
                                  |
                Mirror -> Bundle -> Verify -> Publish
                                                |
                                        Optional upload
                                                |
                              Record results -> Unlock
```

The mirror is disposable. A ready snapshot must not depend on it. A full snapshot is portable without SQLite; its manifest carries the required interpretation data.

## Failure semantics

- Catch task failures without stopping unrelated repositories unless the failure invalidates the entire root or run.
- Classify authentication, permission, disk-full, corrupt-input, transient-network, and cancellation errors.
- Retry transient operations with limits, backoff, and jitter. Do not retry invalid credentials indefinitely.
- Keep verified local output when upload fails. Retry upload without recreating the bundle.
- On startup, reconcile interrupted database tasks and staged files. Never blindly rerun destructive steps.
- Explicitly handle empty repositories, unsupported object formats, deleted repositories, renamed owners, and expired access.

[RECOVERY-SEMANTICS.md](RECOVERY-SEMANTICS.md) is authoritative for commit points, cancellation after publication, uncertain durability/bookkeeping, startup reconciliation, remote effects, deletion authorization, and scheduled occurrences. Successful publication cannot be rolled back merely by returning a cancelled error. Retain known artifact identity in the result.

## Storage and cloud

Store the application database in the OS application-data directory. Store mirrors and snapshots in the user-selected backup root. Use stable provider/repository IDs in storage paths. Display owner/name separately.

Upload to unique snapshot keys. Publish the remote manifest last. Readers treat a missing manifest as incomplete. A remote receipt does not imply cryptographic verification of every remote byte; distinguish upload receipt from read-back verification.

## Extension points

Later: separate worker process, incremental bundles, explicit LFS storage, metadata exporters, client-side encryption, and OS scheduling. Each requires a new acceptance gate and, where relevant, a format version change. Do not imply these features are included in version one.
