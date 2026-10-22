# Plan 003: create and restore a safely published offline snapshot

> **Historical; superseded for execution** by [the canonical tasks](../docs/implementation/TASKS.md). This artifact does not indicate that engine code exists.

> Executor: implement a complete offline slice behind `internal/backup`, not the whole application. Honor no-overwrite, ownership, and cancellation rules even when they require stopping to qualify an OS primitive.

## Status

- Priority: P1. Effort: L. Risk: HIGH. Category: correctness/direction.
- Depends on completed plans 001 and 002, including their approved contracts and runner policies.
- Planned at: 2026-10-22; no commit SHA exists. Content baseline: `plans/baseline.json`.

## Why this matters

The product promises recoverability, not merely successful bundle creation. One verified offline round trip proves that artifact interpretation, process handling, filesystem ownership, and restore agree. It should work without SQLite, GitHub credentials, or UI integration.

## Current state and drift check

At planning time only native scaffold code exists. `internal/desktop/app.go:15` says the next milestone is local backup/restore. `docs/IMPLEMENTATION.md:19–22` requires fresh verification and offline recovery. `docs/BACKUP-FORMAT.md:83,92–98` requires no-replace publication; line 100 requires independence from the database, network, and mirror.

Before starting, run:

```sh
python3 - <<'PY'
import hashlib, json
from pathlib import Path
expected = {'docs/BACKUP-FORMAT.md', 'docs/ARCHITECTURE.md', 'docs/IMPLEMENTATION.md',
            'docs/SECURITY.md', 'go.mod', 'go.sum'}
b = json.loads(Path('plans/baseline.json').read_text())
bad = [p for p,d in b['files'].items() if p not in expected and
       (not Path(p).is_file() or hashlib.sha256(Path(p).read_bytes()).hexdigest() != d)]
assert not bad, 'Unexpected drift: ' + ', '.join(bad)
print('Unrelated baseline inputs match; inspect all predecessor outputs')
PY
```

Inspect every excepted path and all added `internal/backup` files against plans 001/002; stop on unrelated changes. Confirm both predecessor rows are DONE and their tests execute successfully. Expected modifications are not blanket permission to ignore drift.

Use standard Go `testing`, temporary fixtures, wrapped error causes, and context-first operations. Preserve MyGo v0.6.2 / Go 1.27.1. Do not put MyGo types in the engine.

## Scope

Create `internal/backup/engine.go`, `create.go`, `verify.go`, `restore.go`, `filesystem.go`, necessary private OS files, and associated tests. Reuse plan-001 manifest/result and plan-002 runner; change those files only to satisfy an approved shared contract, retaining all predecessor tests. Update README status accurately and `docs/{README,ARCHITECTURE,BACKUP-FORMAT,IMPLEMENTATION,TESTING}.md` with tested behavior, distinguishing implemented offline engine methods from unwired native UI. Add CI steps only if required to execute real engine integration tests on the existing OS matrix. Platform dependencies require explicit review and pinning.

Out of scope: native UI changes, SQLite/durable jobs, GitHub access, credentials, cloud, retention, schedules, incremental bundles, LFS payloads, checked-out working copies, source-repository cleanup, initializing or committing the workspace repository, and production-readiness claims. Initializing and committing synthetic temporary fixture repositories is permitted.

## Proposed external interface

Use one deep module with context-first `CreateLocal`, `Verify`, and `Restore` operations. Requests carry explicit paths and validated metadata. Results carry verification dimensions and typed outcomes. Prefer concrete types and private helpers; introduce interfaces only at a seam with real test or platform variation.

No operation may overwrite an existing destination. A full snapshot must restore offline from its own manifest/bundle. Progress is optional and nonblocking; terminal results return directly. A cancelled call may report an already-published artifact separately; never remove valid published output just to force a simple error model.

## Steps

### 1. Qualify filesystem ownership and no-replace publication

Define canonical root identity, conflicting overlap policy, and source/staging/mirror/destination ownership. Reject unsafe nested/aliased managed locations. Use cooperating-process protection, not only a Go mutex or PID-file check. Restore must work outside the original root.

Use private staging on the final destination filesystem. Do not implement no-replace as `Stat` followed by plain `Rename`. Identify a native no-replace primitive or explicitly qualified safe protocol per platform/filesystem. Document the stable-parent trust boundary and no-follow/junction behavior. Unsupported capability must fail before destructive work. Sync/close behavior and post-rename errors need explicit recovery semantics.

Add tests `TestPublishNeverReplaces`, `TestPublishRejectsUnsafePaths`, `TestConflictingRoots`, and `TestConcurrentWriters`. Include a synchronization hook that lets a competitor create a file, empty directory, or symlink exactly before publication. Preserve the competitor and never clean up another process's data.

Verify: `go test ./internal/backup -run 'TestPublish|TestConflictingRoots|TestConcurrentWriters' -count=1` exits 0 on qualified platforms; unsupported capabilities yield explicit typed errors. STOP before claiming a platform supported if these are skipped.

### 2. Create a full local snapshot

Under ownership, create a controlled local mirror without hardlink/alternate dependence that could invalidate independence. Capture approved refs and HEAD while preventing concurrent mirror changes. Refuse unsupported namespaces/formats rather than drop them. Create a full bundle, import into fresh verification storage, compare exact refs/HEAD, check prerequisites and object integrity using the approved policy, hash bytes, then publish manifest/bundle together.

Handle truly empty, tag-only, symbolic/unborn, and detached cases according to plan 001. Preserve annotated tag objects. Do not invent ref mappings or silently force HEAD to `main`.

Handle ENOSPC and read/write/sync failures at every stage. A free-space check is advisory only. Verification needs additional live disk space; never downgrade it silently. Clean only staging the operation demonstrably owns; retain recoverable failed-publication state when cleanup would destroy diagnostic or valid output.

Verify: `go test ./internal/backup -run 'TestCreateLocal|TestEmptyRepository|TestTagOnly|TestDetachedHead' -count=1` exits 0 with explicit supported/unsupported behavior tested.

### 3. Restore exclusively from staged snapshot inputs

Bound manifest reads and copy bundle inputs into private staging to avoid hashing one path and consuming another. Revalidate checksum and schema; verify/import in a fresh bare repository of the recorded format. Apply explicit saved refs/HEAD. Enforce the approved extra-ref/object policy and `git fsck` integrity semantics. Never fetch missing objects or consult a source mirror. Publish into a new destination with the qualified primitive.

Verify: `go test ./internal/backup -run 'TestOfflineRoundTrip|TestRestoreRejectsCorruption|TestRestoreExistingDestination|TestRestoreInputMutation' -count=1` exits 0. Remove or make source and mirror inaccessible before restoring. Assert exact raw refs/HEAD and object integrity, not just subprocess exit zero.

### 4. Inject failures and prove termination

Add cancel/deadline tests during Git work and verification, plus failure injection around staging writes, verification, manifest write, sync, and publication. Test unread progress delivery without deadlock if a progress interface is introduced. Cancellation must wait for owned process-tree termination before cleanup/unlock. A crash must leave either unready staging or a verifiable final snapshot; no database is required to discover the latter.

Verify: `go test ./internal/backup -run 'TestBackupCancellation|TestFailureBoundaries|TestDiskFull|TestProgressDoesNotBlock' -count=1` exits 0; if progress is omitted, omit that test and record the intentional absence rather than a fake passing test.

### 5. Run full gates and report platform evidence

Run `go test ./...`, `go vet ./...`, `go build ./...`: exit 0. `gofmt -l main.go internal`: no output. Run `go test -race ./internal/backup` where supported. Execute native integration tests on Linux/macOS/Windows; distinguish executed behavior from cross-compilation. Record host implementation evidence and pending qualification separately when another native platform is unavailable; do not claim all-platform support. Keep the existing headless UI tests passing and its unavailable-feature messages accurate until integration actually occurs.

## Done criteria

- A fresh full snapshot restores from only manifest/bundle and installed Git, without network, source, mirror, or database.
- Required fixtures include multiple branches, annotated/lightweight tags, supported HEAD/object formats, empty and tag-only history, and deletion of refs between snapshots.
- Unsupported formats/namespaces receive explicit results, not partial silent backups.
- Corrupt input, unexpected refs, prerequisite dependencies, unsafe paths, conflicting roots, and existing destinations cannot yield success.
- Named publication/race/failure tests execute and prove user data is not replaced or removed.
- Runner predecessor tests still pass; no UI/db/cloud features are smuggled into the slice.
- Index records commands/results, platform qualification, and remaining limitations. README distinguishes tested engine capability from still-unwired UI actions.

## STOP conditions

Stop if a predecessor is unapproved/incomplete; no safe no-replace protocol exists for a claimed target; local source/mirror independence cannot be demonstrated; resource limits or HEAD/ref policies require an unapproved compatibility change; the implementation needs out-of-scope infrastructure; a platform guarantee is only inferred from compilation; or a verification gate fails twice. Do not weaken no-overwrite or offline requirements to finish faster.

## Maintenance

The final artifact, not a database row or UI event, determines snapshot portability. Later job integration can persist its result. Cloud copies consume ready artifacts. Incremental chains, encryption, and LFS need separate contracts; they must not change full restore implicitly. Document native filesystem durability limits rather than claim perfect crash consistency on every filesystem.
