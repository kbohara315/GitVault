# Configuration contract

**Status: the typed configuration core is implemented for current local consumers (root, concurrency, finite limits/timeouts, immutable revisions, and the I06 GitHub credential handle); I05 and I06 are accepted for their documented Linux/amd64 implementation scopes. Cloud and schedule settings remain future integration work.** This document owns settings interpretation, not the portable artifact format in `BACKUP-FORMAT.md`. Technical defaults delegated to implementation agents are not human release approval, permission to access credentials, cloud disclosure consent, or deletion authority.

## Ownership and composition

- SQLite is the single source of truth for writable, non-secret runtime settings. Do not add a writable JSON/YAML settings file, environment override layer, or competing per-module store. MyGo build configuration is not application settings.
- `internal/config` owns typed versioned records, defaults, normalization, migration, cross-field validation, and revision creation. Module-owned option types retain locality; a consumer interface exposes only the options its module needs.
- The application composition module maps the normalized configuration to Git, cloud, scheduler, and secret-store adapters. This seam provides testing leverage without global variables, arbitrary plugins, shell hooks, or a service locator.
- Admit each backup, restore, upload retry, or retention execution with one immutable normalized revision and explicit intent. Record effective non-secret values, selected stable repository IDs, target IDs, and credential reference/generation. No module reads changing settings during accepted work.
- Independent configuration, database-schema, artifact-format, and application versions must not be conflated. A settings revision is an immutable identity, not the schema version.
- `config_revisions` owns configuration; a singleton `settings_head` points at the current revision. Selection/target/schedule fields elsewhere are revision-keyed projections or history, never independent mutable sources. Validate a full edit, write revision/projections, and compare-and-swap the head in one transaction; admission captures that same revision atomically. Operational state (due times, receipts, leases) is separate. See DATA-MODEL.md.

## Bootstrap and persistence

1. Resolve the platform application-data directory using the platform adapter and the stable application identifier. The exact production identifier is a release gate. Development and production use separate directories and secret-service namespaces. Default builds use the development namespace; the explicit `gitvaultrelease` build tag selects the production secret namespace, but does not qualify or approve a release.
2. Place `gitvault.sqlite3` below that local application-data directory; log files go in its `logs` child. Do not place SQLite/WAL in the backup root, on a network filesystem, or in a synchronized folder. Fail with an actionable error if the directory cannot be safely created/opened; do not fall back to the working directory.
3. Acquire application/database ownership before migration. Create the first schema and default settings transactionally. Do not require GitHub, cloud, or secret-store access to open Settings or restore an imported full snapshot.
4. The backup root starts unset. The user selects an absolute local directory; validation and an explicit idle rebind establish its identity and managed subdirectories. No default path silently starts a backup. Restore destination is a per-operation intent, never a remembered overwrite permission.
5. Export is a sanitized design-versioned interchange document, not a live file watched by the application. Import passes the same parser and creates a new SQLite revision after review; it always disables schedule/cloud and resets retention to preview-only. Missing secret references remain unresolved; import never imports credential values or permissions.

A managed root has one owner installation/database identity. Validate its non-secret root-binding marker and matching SQLite identity under the root lock before managed access; reject foreign bindings instead of silently registering the same root with an unrelated lease/pin database. Development and production cannot co-manage a root. A lost/foreign binding still permits independent read-only artifact import/restore, not automatic adoption or deletion authority.

Reject duplicate keys, unknown fields, invalid UTF-8, overflow, negative unsigned values, non-integer counters, and unknown/newer versions before side effects. Apply known ordered configuration migrations inside the database transaction; commit values and revision together. Refuse missing/altered migration history and unsupported downgrade. Preserve the original database on failure; never replace unknown settings with defaults.

## Types, units, and application timing

Tables define the complete schema-one design below. Objects have no implicit extra fields. IDs and references are non-secret opaque strings. `null` means intentionally unconfigured, not permission to guess. Bytes and counts are integer values; MiB/GiB/TiB below mean powers of 1024. Durations serialize as integer magnitude plus `ms`, `s`, `m`, or `h`; normalize to checked integer durations.

- **Next run:** captured at admission of the next relevant operation, including restore/import/transfer; never mutates active work.
- **Idle:** reject changes while a run, restore, transfer, preview execution, or lease is active; validate and rebind explicitly. No automatic data move/delete.
- **Live:** UI-thread/lifecycle change only, not an engine revision mutation.
- **Restart:** bootstrap/composition policy, not a hot setting. Database path and application identity are restart-only and not user-editable schema-one fields.
- **Schedule revision:** atomically replace future scheduling state and recompute due times; already accepted work retains its revision.

## Schema-one defaults and finite bounds

These are recommended operational budgets, not claims every machine can process every allowed value. No default is unbounded.

| Field | Default | Type, finite bounds, or rule | Timing |
| --- | --- | --- | --- |
| `schema_version` | `1` | integer, exactly supported version | load |
| `backup.root` | `null` | null or validated absolute local directory | idle |
| `backup.repository_concurrency` | `2` | integer 1–8 | next run |
| `backup.snapshot_kind` | `"full"` | full only; no prerequisite chains | next run |
| `backup.keep_mirror` | `true` | boolean; false only requests separately scoped post-run mirror cleanup | next run |
| `selection.repository_ids` | `[]` | unique stable provider IDs; at most 10,000; each 1–256 UTF-8 bytes | next run |
| `github.credential` | `null` | null or credential handle defined below | next run |
| `github.rate_limit_wait` | `"15m"` | 0–60 min; zero fails rather than waits | next run |
| `limits.manifest_bytes` | 4 MiB | integer 64 KiB–16 MiB | next run |
| `limits.reference_count` | 100,000 | integer 1–1,000,000; manifest limit also applies | next run |
| `limits.reference_name_bytes` | 4,096 | integer 256–16,384; valid Git grammar also required | next run |
| `limits.machine_output_bytes` | 32 MiB | integer 1–256 MiB per Git command; fail on excess | next run |
| `limits.diagnostic_bytes` | 64 KiB | integer 4 KiB–1 MiB per command; redact then bounded truncation | next run |
| `limits.bundle_bytes` | 100 GiB | integer 1 MiB–1 TiB | next run |
| `limits.verification_scratch_bytes` | 250 GiB | integer 16 MiB–4 TiB per operation; expansion budget | next run |
| `limits.discovery_repositories` | 10,000 | integer 1–100,000; stop with explicit incomplete result | next run |
| `limits.http_response_bytes` | 16 MiB | integer 1–64 MiB per discovery/API response, not artifact GET | next run |
| `limits.event_capacity` | 256 | integer 32–4,096 immutable events | restart |
| `limits.ui_batch_size` | 64 | integer 1–256 events per dispatch | restart |
| `limits.log_file_bytes` | 8 MiB | integer 1–64 MiB | restart |
| `limits.log_files` | 4 | integer 1–16 rotated files, including active file | restart |
| `timeouts.git` | `"60m"` | 1 min–24 h per Git operation | next run |
| `timeouts.http_request` | `"60s"` | 5–300 s per HTTP attempt; stream body included | next run |
| `timeouts.transfer` | `"4h"` | 1 min–24 h per complete transfer, retries included | next run |
| `timeouts.cleanup` | `"30s"` | 5–120 s; fresh bounded abort context | next run |
| `retry.max_attempts` | `3` | integer 1–8 total attempts per retryable request | next run |
| `retry.initial_backoff` | `"1s"` | 100 ms–10 s | next run |
| `retry.max_backoff` | `"30s"` | initial backoff–120 s, cancellation-aware full jitter | next run |
| `cloud.enabled` | `false` | boolean; requires explicit target and disclosure acknowledgement | next run |
| `cloud.active_target_id` | `null` | null or one configured target ID | next run |
| `cloud.targets` | `[]` | at most 16 validated target records | next run |
| `cloud.upload_concurrency` | `1` | integer 1–4 global transfers | next run |
| `cloud.part_concurrency` | `2` | integer 1–4 parts per transfer; global product at most 8 | next run |
| `cloud.part_size_bytes` | 16 MiB | integer 8–128 MiB, whole MiB | next run |
| `cloud.multipart_threshold_bytes` | 32 MiB | integer 8–256 MiB, at least part size | next run |
| `cloud.read_back` | `false` | boolean; false labels result upload receipt only | next run |
| `retention.mode` | `"preview_only"` | preview_only or explicit_delete; latter is an opt-in policy, not authority | next preview/run |
| `retention.keep_daily` | `7` | integer 0–365 | next preview |
| `retention.keep_weekly` | `4` | integer 0–104 | next preview |
| `retention.keep_monthly` | `6` | integer 0–120 | next preview |
| `schedule.enabled` | `false` | boolean; enabling requirements below | schedule revision |
| `schedule.timezone` | `null` | null or explicitly confirmed IANA zone, at most 128 bytes | schedule revision |
| `schedule.local_time` | `"02:00"` | exact HH:MM, 00:00–23:59 | schedule revision |
| `schedule.frequency` | `"daily"` | daily, weekly, monthly | schedule revision |
| `schedule.weekday` | `null` | null or integer 1–7, Monday=1; weekly only | schedule revision |
| `schedule.monthday` | `null` | null or integer 1–31; monthly only; shorter months use last valid day | schedule revision |
| `schedule.catch_up` | `"offer_once"` | only supported value | schedule revision |
| `ui.close_to_tray` | `false` | boolean; true requires operational tray | live |

## Target and credential records

No target is created by default. When adding one, all fields below are required except the explicitly nullable endpoint. A target's stable ID is distinct from its object-key prefix and display name. Editing destination identity requires idle rebind and a new target ID if it would reinterpret old receipts. Retain historical target identities for recovery; never repoint a deletion intent.

| Target field | Validation; creation default where applicable |
| --- | --- |
| `id` | Unique opaque ID, 1–64 ASCII letters/digits/hyphen/underscore; no generated default before explicit creation |
| `provider` | s3 or r2; explicit choice |
| `bucket` | Provider-valid bucket name, 3–63 bytes; explicit choice |
| `region` | 1–64 ASCII bytes; explicit AWS region for s3, exactly auto for r2 |
| `endpoint` | Default null for s3 standard resolution; r2 requires approved account HTTPS endpoint; at most 2,048 bytes |
| `prefix` | Creation default `gitvault/`; 1–256 ASCII bytes, letters/digits/hyphen/underscore/slash, trailing slash; no leading slash, empty segment, dot segment, or backslash |
| `credential` | Required credential handle, no default |

A credential handle is exactly `{"reference":"opaque-store-key","generation":1}`. Reference is 1–128 ASCII letters/digits/hyphen/underscore; generation is integer 1–2,147,483,647. It points into a stable dev/prod-separated OS service namespace, not a credential file or URL. Payload encoding belongs to the secret adapter; validate its 2,560-byte maximum before calling MyGo. Do not expose access keys, tokens, session tokens, passwords, or values in configuration, run records, manifests, diagnostics, or fixtures.

Rotation writes a new immutable generation before publishing a settings revision. Accepted work retains its original generation; revoked/missing credentials fail explicitly instead of switching identity. Retain old generations until active leases end, then offer scoped cleanup. Removal clears the settings reference before deleting only that captured generation; if native deletion cannot be confirmed, GitVault stops using the token and reports that it may remain in OS storage. Bound native secret operations to one outstanding call; an OS unlock prompt has no cancellable MyGo API. Cancellation may discard its eventual result but cannot promise terminating the prompt. No plaintext fallback.

## Cross-field validation and invariants

- Backup requires a bound root and explicit nonempty selection; selected count must not exceed discovery limit. Discovery filters never select new repositories automatically. Restore/import of a local full snapshot requires neither account nor configured root.
- Reject root/restore/staging/database/source overlap, symlink escapes, and managed-path identity changes. Recheck filesystem identity at execution, not just settings save. Restoration/publication never replaces existing destinations.
- `ui_batch_size <= event_capacity`; `max_backoff >= initial_backoff`; `http_request <= transfer`; `cleanup <= transfer`. Outer operation deadlines can shorten attempts. SDK and application share one retry owner; no multiplicative retries.
- `verification_scratch_bytes >= bundle_bytes` is a necessary preflight only, not a sufficient expansion estimate. Bound machine output and reference decoding while streaming; never allocate a configured maximum eagerly.
- Enabled cloud requires active target, resolvable handle, and separately recorded disclosure acknowledgement scoped to its destination. Disabled cloud can retain valid non-secret target metadata without contacting it. Endpoint credentials, query strings, fragments, non-HTTPS schemes, unexpected redirects, and arbitrary DSN/HTTP-header overrides are forbidden.
- Use configured part size, increased deterministically to a whole MiB if needed to stay within the qualified provider's part-count limit, never above 128 MiB. Reject the transfer if impossible. Bound global parts by the concurrency product; file-backed section readers, not unbounded part buffers. Files below the threshold use PUT; others use multipart. Enforce the stricter qualified provider object/part limits in addition to these budgets.
- Bucket/prefix collisions across configured targets for the same endpoint are rejected. Unique target IDs and snapshot keys prevent accidental overlap; prefixes do not grant bucket-wide cleanup permission.
- Retention buckets form a union in UTC calendar periods. Pins, active leases, dependency checks, and the last ready snapshot per repository take precedence. Preview changes never delete. `explicit_delete` only makes a scoped execution request possible: require a fresh reviewed preview, exact snapshot IDs/local and remote scope, destination identity, and explicit deletion permission. Revalidate under ownership at execution. No schedule automatically grants deletion authority.
- Configured budgets are admission/resource policy, not artifact semantics. Keep full bundles self-contained and manifests portable. A valid larger artifact fails with a resource-limit explanation and can be retried with larger valid budgets; do not rewrite its format, truncate refs, split into dependency chains, or weaken verification.

No switch may bypass required checksum, bundle, ref/HEAD, object-integrity or restoration checks; TLS verification; no-replace publication/restore; process/root ownership; secret isolation; or destructive-scope checks. There is no `insecure`, `skip_verify`, `force_restore`, custom Git argument list, or shell hook. Optional cloud read-back is a distinct verification level, not a bypass of mandatory local verification.

## Capacity enforcement

Preflight free space is advisory, not a reservation. Mirror + bundle + fresh verification repository + import copy can coexist; show estimated cost before large operations. Count copied/written bytes and scan owned Git expansion scratch. Stop on budget exhaustion with a typed result; polling may overshoot and is not a strict OS quota. Qualify the sampling/cancellation bound and use an OS quota where strict containment is required. Disk-full errors must remain safe at every write/sync. Never delete retained snapshots or downgrade verification to make room.

Bound discovery pagination by repository/response budgets, diagnostic output by per-command caps, log storage by rotation, and event memory by channel capacity. Coalesce intermediate progress; persist terminal outcomes independently and deliver them without dropping final state. No per-event goroutine or unbounded pending-work queue. One accepted backup run; a second request is rejected, not silently queued.

## Scheduling semantics

Enabling requires root, nonempty selection, a usable credential handle for authenticated discovery, and an explicitly confirmed IANA timezone; null is invalid when enabled. A suggested OS zone is not confirmation. Daily requires null weekday/monthday; weekly requires weekday and null monthday; monthly requires monthday and null weekday. Monthly dates 29–31 resolve to the month's last valid day when necessary; show this explicit rule before enablement.

Persist the schedule revision and next UTC occurrence. Calculate in its stored timezone, not a floating OS preference. On a daylight-saving gap use the first valid local instant after the gap; on a fold use the earlier occurrence only. Uniquely record occurrence identity so startup/wake/repeated ticks cannot dispatch it twice. Recompute after clock/zone database changes and each startup/wake; resume events are only hints, with a 60-second engine-owned wall-clock check. A stopped application cannot schedule work.

For missed occurrences produce **at most one catch-up offer**, never automatic execution or a backlog. Explicit acceptance submits one new intent through the shared job manager; refusal or dismissal consumes the offer for that missed range. If busy, do not queue a catch-up run; keep at most the single offer until the user responds. Schedule edits invalidate stale offers. Ordinary future due occurrences may submit scheduled runs after opt-in, but cannot bypass ownership, consent, or credential failures. Unattended runs never start an OS unlock prompt; report credential interaction required and offer manual retry. MyGo exposes no no-prompt flag: unattended credential retrieval must remain unavailable unless the adapter can prove a qualified noninteractive path or use an explicitly established, still-valid bounded in-memory credential lease. Do not infer prompt-free behavior from an earlier successful Get, and never persist a plaintext cache.

## Complete default example

**Design example, not implemented runtime configuration.** Valid JSON for all schema-one defaults; root, timezone, account, and targets are deliberately unconfigured. This can be saved as a disabled settings revision, but cannot admit a backup or enabled schedule. Empty targets mean the target-creation defaults above are not instantiated.

```json
{
  "schema_version": 1,
  "backup": {
    "root": null,
    "repository_concurrency": 2,
    "snapshot_kind": "full",
    "keep_mirror": true
  },
  "selection": { "repository_ids": [] },
  "github": { "credential": null, "rate_limit_wait": "15m" },
  "limits": {
    "manifest_bytes": 4194304,
    "reference_count": 100000,
    "reference_name_bytes": 4096,
    "machine_output_bytes": 33554432,
    "diagnostic_bytes": 65536,
    "bundle_bytes": 107374182400,
    "verification_scratch_bytes": 268435456000,
    "discovery_repositories": 10000,
    "http_response_bytes": 16777216,
    "event_capacity": 256,
    "ui_batch_size": 64,
    "log_file_bytes": 8388608,
    "log_files": 4
  },
  "timeouts": {
    "git": "60m",
    "http_request": "60s",
    "transfer": "4h",
    "cleanup": "30s"
  },
  "retry": {
    "max_attempts": 3,
    "initial_backoff": "1s",
    "max_backoff": "30s"
  },
  "cloud": {
    "enabled": false,
    "active_target_id": null,
    "targets": [],
    "upload_concurrency": 1,
    "part_concurrency": 2,
    "part_size_bytes": 16777216,
    "multipart_threshold_bytes": 33554432,
    "read_back": false
  },
  "retention": {
    "mode": "preview_only",
    "keep_daily": 7,
    "keep_weekly": 4,
    "keep_monthly": 6
  },
  "schedule": {
    "enabled": false,
    "timezone": null,
    "local_time": "02:00",
    "frequency": "daily",
    "weekday": null,
    "monthday": null,
    "catch_up": "offer_once"
  },
  "ui": { "close_to_tray": false }
}
```

## Agent implementation gates

1. Add typed options/defaults and table-driven tests for every boundary, normalization, invalid cross-field combination, and the JSON example. Assert all defaults in the example match this contract.
2. At durable-settings integration, add SQLite revision/migration and concurrent-edit tests, unknown-version refusal, atomic rollback, sanitized export/import, and restart-only/live timing tests.
3. Prove admission snapshots do not change on settings edits or credential rotation. Exercise empty selection, root rebind, target collision, resource exhaustion, and lease-aware retention preview invalidation.
4. Use fake clocks for gap/fold, startup/wake, duplicate ticks, disabled schedules, and one catch-up offer with no automatic dispatch. Test native prompt refusal in unattended paths.
5. These gates authorize technical implementation only. Release identity, supported platforms, real credential/provider access, destructive operations, signing, and publication retain their explicit human permissions.
