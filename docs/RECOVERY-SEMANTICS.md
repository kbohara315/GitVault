# Outcomes, commit points, and recovery

Status: normative target behavior for implementation. “Normative” describes the contract to implement, not existing functionality.

## Separate facts

A result contains operation outcome (`succeeded`, `failed`, `cancelled`, `interrupted`, or explicit no-artifact result), any published artifact identity/path, verification evidence, durability/bookkeeping warnings, and cloud outcome. Do not collapse them into one success boolean.

Cancellation after local publication can yield a retained ready snapshot and a cancelled operation. Cloud failure leaves local readiness intact. Empty repository is a no-artifact outcome, not a successful restorable snapshot.

## Commit points

| Effect | Commit point | After that point |
| --- | --- | --- |
| Local snapshot/restore visibility | qualified no-replace publication primitive succeeds | return known artifact identity even if sync/bookkeeping later fails |
| Durable run admission | intent transaction commits under ownership | restart reconciles instead of blindly admitting duplicate work |
| Remote snapshot discoverability | manifest publication is confirmed or reconciled at unique key | distinguish receipt from read-back verification |
| Retention authorization | exact preview approval recorded and revalidated | no additional artifact/copy may be added |
| Individual delete | delete effect is confirmed or reconciled | record effect; cancellation cannot restore removed bytes |

Publication outcome values: `not_published`, `published`, `published_durability_uncertain`, `outcome_unknown_requires_reconciliation`. The caller must not retry an uncertain publication with a new ID until inspecting the original intent/target. Never delete published output to manufacture a clean failure result.

Atomic visibility and power-loss durability are different. Qualify filesystem behavior; flush/close and directory sync where supported. An OS/filesystem that cannot meet the no-replace contract fails explicitly. `exists` followed by plain rename is not a substitute.

## Run state vocabulary

Durable states are `preparing`, `running`, `cancelling`, then `succeeded`, `partial`, `failed`, `cancelled`, or `interrupted`. Admission persists `preparing` before background work; there is no multi-run queue in release one. “Idle” is a UI observation that no admitted run is active, not a database state. Repository stage and outcome are separate fields. A task may retain a local artifact despite a failed/cancelled later stage.

Use one transition validator and table tests. A run summary is derived from durable task/artifact/copy facts. Never mark all repositories failed merely because final summary persistence failed.

## Startup reconciliation

Acquire installation/root ownership before changing interrupted state. Then process bounded batches:

For managed work, root marker UUIDs and installation/database UUIDs must agree under the root lock. A foreign binding or lost database must fail managed admission/deletion closed. Do not rebuild ownership from discovered snapshots. Independent read-only import/restore remains possible without granting pins/leases/deletion authority to a second database.

Managed root ownership is exclusive to one installation/database authority, checked from a versioned root marker under the shared OS root lock. Reject foreign/ambiguous authority rather than letting a second SQLite database maintain independent pins/leases for the same artifacts. Managed readers and deletion cooperate with that authority. Standalone read-only import can copy under a root read lock without accessing the original database, then restore its private copy elsewhere.

The Linux engine trusts paths against **other OS users**, not same-UID code execution. A managed root must be owned by the current user and not group/world writable. Its immediate parent must not be group/world writable, except a sticky shared directory such as `/tmp` when the root entry is owned by the current user. Engine-created managed descendants are private, current-user-owned directories. Restore destination parents use the same trust boundary. Static symlinks are rejected; marker and snapshot files are opened without following a final-component symlink. A same-UID process can impersonate the application and is outside the desktop threat model.

| Observed state | Action |
| --- | --- |
| Active task whose owner is gone | mark interrupted and reconcile its operation ID/staging/publication |
| Valid final snapshot with matching recorded intent but no ready row | validate manifest/hash/identity and index; no new Git transfer |
| Unknown final directory with no intent | offer read-only import inspection; do not silently grant deletion authority |
| Ready row with absent/corrupt files | mark missing/corrupt, disable claims of recoverability, preserve evidence |
| Duplicate snapshot ID with different bytes | quarantine logically and report conflict; never overwrite either |
| Unowned staging | preserve and report until ownership is established; scoped cleanup only |
| Persisted deletion intent | resume only remaining approved effects; never reimport intentionally deleting output |
| SQLite migration/corruption/unavailability | disable new managed jobs and deletion; offer independent local import/restore |
| Root moved or identity changed | require explicit idle rebind/import; no implicit migration or cleanup |

The manifest and bundle suffice for portable full restoration. SQLite owns operational intent/history, not the truth that every filesystem path with a record is valid.

## Input and Git isolation

Copy bounded external snapshot input into private staging; hash and consume those bytes. Reject unsafe metadata, duplicate keys, external paths, invalid refspec-like names, unsupported versions/formats, and prerequisites for full-only input. Initialize a fresh bare repository of the recorded format. No inherited helper/hook/template/alternate/replacement behavior or network lazy fetch is allowed.

Restore exact saved refs and HEAD, then check object integrity. Preserve raw annotated-tag IDs. Detached-only commits need explicit transfer qualification; `--all` includes HEAD in Git's revision selection, but a mirror is not proof that acquisition preserved detached-only source data.

Valid extra packed objects are permitted by the initial format; exact refs/HEAD and object integrity are required. `fsck` success is not a proof of a minimal object pack, authenticated origin, secret removal, encryption, or complete GitHub account coverage.

## Resource failure and cancellation

Preflight space is advisory. All writes/imports/syncs need disk-full handling. Enforce named input/output budgets and report any soft monitoring limits honestly. Never delete older snapshots or downgrade verification on resource failure.

Cancel Git and owned descendants, bound pipe drain, reap them, handle owned uncommitted staging, record observed outcomes, then release ownership. Copy/hash loops check cancellation. Short publication critical sections must resolve their outcome before returning. SDK abort cleanup uses a fresh bounded context, not the already-cancelled upload context.

Native secret unlock calls may outlive cancellation. Bound outstanding calls, stop accepting stale results, and do not claim the OS prompt was killed. A timeout wrapper cannot safely create unlimited abandoned goroutines. Managed shutdown reports blocked native prompts explicitly rather than deadlocking main.

## Cloud copy lifecycle

Persist operation ID, target revision, unique snapshot keys, upload ID, completed parts as necessary, and cleanup status. Stream from a fixed ready local file. Publish manifest last. On a lost response, inspect the exact expected operation/key/size/digest evidence instead of overwriting a different object.

Verification levels: `upload_receipt`, `read_back_checksum`, and separately `restoration_validated`. ETag, HEAD metadata, and a caller-supplied digest do not prove remote bytes match. Multipart composite checksums are not plain file SHA-256. Portable read-back means GET, bound/count/hash all bytes, and compare with the trusted expected digest; restoration is another check.

Use unique keys and preserve no-overwrite intent. Qualify conditional writes for both simple PUT and multipart completion; if a provider cannot guarantee the selected operation, fail or use a tested operation-key reservation protocol, not a blind overwrite retry. Reconciliation never treats a missing manifest as complete.

## Retention and deletion

Release-one default protection is per repository: preserve at least one valid ready **local** snapshot. A remote receipt does not substitute for that local copy. Policies select the union of daily/weekly/monthly retained buckets; pins and active leases always win.

Preview exact snapshot IDs and local/cloud copy scope with inventory/policy revisions. Explicit user confirmation binds those IDs/revisions. Revalidate under ownership before effects. Any change invalidates the preview; do not silently broaden it. Scheduling a backup does not authorize retention execution. No unattended deletion is enabled in release one.

Record deletion intent before effects. Remove the remote manifest marker first to prevent new discovery of an incomplete copy, then payload; track each effect separately. Local deletion similarly marks intent before removing payload. On failure retain a visible cleanup-required record. Retry only remaining authorized effects.

S3 versioned buckets may create delete markers while retaining older versions and charges. Release one does not purge arbitrary version histories, override Object Lock/legal holds, claim secure erasure, or promise reclaimed bytes from an ordinary delete receipt. Report these limitations and provider-denied deletes accurately.

## Schedule interpretation

Recommended deterministic defaults: IANA timezone plus local time; on spring-forward gap use the first valid local instant after the gap; on fall-back fold use the earlier occurrence once; on invalid monthly day use that month's last valid day. Persist these named semantics and show them before enabling a schedule.

Occurrence identity includes schedule ID/revision, intended local date/time, and resolved UTC instant. Claim it transactionally with run admission so restart between claim/admission cannot orphan or duplicate it. A busy run produces a missed occurrence, never parallel work. On restart/wake coalesce missed occurrences into one **offer**; user acceptance admits one run. Do not auto-replay a backlog. Zone changes create a new revision. Power notifications are hints; periodically reconcile wall-clock due state.

These are editable technical recommendations for the planned UX, not simulated human approval. First-run enablement must display and confirm them. In-app scheduling cannot run while the app is stopped or the computer is off.
