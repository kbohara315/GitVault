# Data model and state

**Status: initial SQLite/configuration modules implemented and reviewed; coordinator integration and full recovery reconciliation remain incomplete.** The current store is an incremental subset of this logical model, not the full schema described below.

Execution authority: [IMPLEMENTATION.md](IMPLEMENTATION.md). Transition/commit semantics: [RECOVERY-SEMANTICS.md](RECOVERY-SEMANTICS.md). This logical schema is introduced incrementally; it is not an instruction to create every table in the first migration.

## Persistence boundaries

| Location | Contents |
| --- | --- |
| OS secret store | GitHub token and cloud secrets |
| OS application-data directory | SQLite database and redacted operational logs |
| Selected backup root | Mirrors, temporary staging, immutable snapshots |

The database stores credential **references**, not credentials. Manifests make full snapshots portable without the database. On a new machine, reconnect accounts separately.

## Initial logical schema

| Table | Important fields |
| --- | --- |
| `schema_migrations` | version, applied_at |
| `accounts` | id, provider, provider_user_id, display_name, secret_ref |
| `repositories` | id, provider, provider_repo_id, account_id, observed display_name/clean_clone_url; selection comes from the current configuration revision |
| `managed_roots` | root UUID, canonical filesystem identity, installation/database UUIDs, path, binding revision, active flag |
| `config_revisions` | id, schema_version, normalized non-secret values, created_at |
| `settings_head` | singleton current_config_revision_id; compare-and-swap updates in the same transaction as required projections |
| `backup_runs` | id, trigger, state, immutable intent/config revision, started_at, finished_at, error_code |
| `repository_tasks` | id, run_id, repository_id, operation_id, stage, outcome, attempt, snapshot_id, error_code |
| `snapshots` | id, repository_id, root_id, format_version, relative_path, state, publication_state, sha256, size_bytes, created_at, verification evidence |
| `snapshot_dependencies` | snapshot_id, required_snapshot_id; unused for full-only version one |
| `cloud_copies` | snapshot_id, target_id/revision, state, operation_id, object_prefix, upload_id, cleanup state, receipt, uploaded_at, verification_level |
| `cloud_targets` | id, revision, provider, bucket, region, endpoint, prefix, secret_ref/generation |
| `schedules` | id, revision, enabled, timezone, frequency, local_time, gap/fold/month policy, next_due_at |
| `scheduled_occurrences` | unique occurrence identity, schedule_id/revision, resolved due UTC, state, run_id or catch-up offer |
| `pins` | snapshot_id, created_at |
| `leases` | id, snapshot_id, owner/operation identity, acquired_at, expiry/renewal evidence |
| `retention_previews` | id, policy/inventory revision, exact IDs/copy scope, protection evidence, expiry, authorization state |
| `deletion_intents` | id, authorized preview/revision, exact remaining effects, per-effect outcome, cleanup evidence |

Use stable provider IDs for identity. Enforce uniqueness on provider/repository ID, foreign keys, and valid states. Defer creation of unused dependency tables until the incremental feature needs them.

## Configuration authority and projections

`config_revisions` is the sole authority for all configuration values; `settings_head` selects its current immutable revision. Repository selection, target configuration, and schedule enablement/policy fields in other tables are revision-keyed read projections or historical operational records, never independently writable settings. Generic `settings` JSON is not a second mutable copy: omit that table or use it only for the transactional head/bootstrap metadata, with no duplicate configuration values.

An edit validates a complete new revision, writes it and all required projections, and compare-and-swaps `settings_head` atomically; stale edits report a conflict. Admission reads the head and captures its values/selected IDs in one transaction. Operational changes such as next due instant, occurrence claims, upload receipts, leases, and deletion effects do not mutate configuration. Disable/edit operations invalidate stale schedule offers/previews through the same revision transaction, while already admitted work retains its intent. Do not retain a second mutable `repositories.selected` or generic settings value store.

Each database has a persistent store ID and installation identity. The managed-root marker binds that authority and its canonical application-data location under the shared root lock. A second installation/database cannot use the same root as an independent writer or retention authority. Restore/import without that database remains read-only against original inputs and publishes only into a new destination.

## State machines

```text
Run:
preparing -> running -> succeeded | partial | failed
    |           |
    +-----------+--> cancelling -> cancelled
    +---- owner lost / restart reconciliation -> interrupted

Repository task:
pending -> fetching -> bundling -> verifying -> publishing -> local_ready
                                                               |
                                                    uploading (optional)
                                                               |
                                                        succeeded | failed

Snapshot:
staging -> ready -> deleting -> deleted
    |         |
    v         v
abandoned   corrupt/missing

Deletion intent (separate from artifact state):
authorized -> executing -> complete
                  |
                  +----> cleanup_required -> executing

Cloud copy:
pending -> uploading -> uploaded
               |
               v
             failed -> uploading
```

Persist task outcome separately from stage. An upload failure must not change a ready local snapshot into a failed local artifact. A cancelled task may still have produced a valid local snapshot; record both facts.

The diagrams show principal flows, not permission to omit cancelled/interrupted/empty outcomes. The run coordinator owns one transition validator with exhaustive tests. There is no durable queued backlog; admission commits `preparing`. Idle means no active run. A lease cannot be expired merely because a wall-clock timestamp passed while its owner remains active; qualify liveness/renewal and clock-change behavior before deletion.

Standalone restore has a direct operation result and does not need these tables. Managed restores can record operation intent, publication outcome, and input leases. Keep such tracking optional for the standalone engine path.

## Transactions and recovery

- Use versioned migrations and enable foreign-key enforcement for each connection.
- Select and test a SQLite driver before choosing journaling and busy-timeout settings. Keep the database on a local filesystem, not inside a synced backup folder.
- Keep network and Git operations outside database transactions.
- Use short transactions to persist stage transitions and publish metadata.
- On startup, mark stale in-flight tasks interrupted only after acquiring process/root ownership.
- Reconcile renamed snapshot directories not yet recorded in SQLite. Validate before import.
- Do not classify all orphaned staging output as safe to delete. Confirm it is unowned and inside the controlled staging root.
- Back up database metadata with a SQLite-consistent method if needed. Never copy an active database file and assume it is consistent.

- Persist normalized configuration and selected repository IDs atomically with admission. Read selected IDs explicitly; an empty list admits no repository work.
- Persist schedule occurrence claim and corresponding run/catch-up state in one transaction; enforce a unique occurrence key.
- Persist deletion authorization/intent before effects. Reconciliation must honor deletion intent rather than reimporting deliberately removed snapshots.
- A post-publication database failure reports the artifact identity and bookkeeping uncertainty. Do not retry as if no output exists.
- Begin with one database connection and verified per-connection foreign keys. Future schema versions and changed migration checksums fail closed; no automatic downgrade.
- Separate database schema, settings schema, artifact format, and application version. Never rewrite completed manifests in place as a database migration.

## Events

An event carries run ID, task ID, sequence, stage, timestamp, and optional measured byte counts. Store important results durably; UI events are transient. Redact human-readable errors. Persist structured error codes and retry classification rather than raw subprocess environments.
