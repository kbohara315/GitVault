# Plan 001: define and validate the full-snapshot contract

> **Historical; superseded for execution** by [the canonical tasks](../docs/implementation/TASKS.md). Do not independently execute this old baseline.

> Executor: read the entire plan. Follow each gate, stop on uncertainty, and update your row in `plans/README.md` with evidence. Preserve existing coverage promises; new product restrictions are not authorized.

## Status

- Priority: P1. Effort: M. Risk: MED. Category: architecture/direction.
- Dependencies: none.
- Planned at: 2026-10-22; **no Git commit exists**. Content baseline: `plans/baseline.json`.

## Drift check

Run from the project root before changing files:

```sh
python3 - <<'PY'
import hashlib, json
from pathlib import Path
baseline = json.loads(Path('plans/baseline.json').read_text())
bad = [p for p, digest in baseline['files'].items()
       if not Path(p).is_file() or hashlib.sha256(Path(p).read_bytes()).hexdigest() != digest]
assert not bad, 'Review changed inputs before proceeding: ' + ', '.join(bad)
print('Baseline matches')
PY
```

Expected: `Baseline matches`. If Python is unavailable, compare the baseline hashes using an equivalent local hash tool; do not install a runtime solely for this check.

## Why this matters

The app has a tested native UI but no engine. Its proposed manifest is not yet a compatibility contract. Create, verification, and restore need one shared interpretation of refs, HEAD, object formats, and success before any artifacts are published.

## Current state and conventions

- `docs/BACKUP-FORMAT.md:38`: “The following is a schema sketch.”
- `docs/BACKUP-FORMAT.md:73`: “Support symbolic, detached, and absent HEAD explicitly in the eventual schema.”
- `docs/BACKUP-FORMAT.md:96`: exact saved-reference comparison is required.
- `internal/desktop/app.go:29`: “View renders local UI state. Never perform network, Git, or disk work here.” Keep the UI untouched.
- `CONTRIBUTING.md:22` requires wrapped error causes and context-first blocking methods. Current tests use standard Go `testing`, e.g. `internal/desktop/app_test.go:9–17`.
- Stack is pinned in `go.mod`: Go 1.27.1 and MyGo v0.6.2. No engine or third-party validation dependency exists.

## Commands

| Purpose | Command | Expected |
| --- | --- | --- |
| Baseline tests | `go test ./...` | exit 0 |
| Vet | `go vet ./...` | exit 0 |
| Engine tests once created | `go test ./internal/backup -count=1` | exit 0; tests actually execute |
| Format check | `gofmt -l main.go internal` | no output |
| Compile | `go build ./...` | exit 0 |

Existing test and vet commands passed in advisory recon. New engine test names below are required deliverables, not existing commands demonstrated by the advisor.

## Scope

Create `internal/backup/manifest.go`, `manifest_test.go`, `result.go`, and `doc.go`. Update `docs/BACKUP-FORMAT.md`, `docs/ARCHITECTURE.md`, and `docs/IMPLEMENTATION.md` to record the approved contract and one-module initial layout. Update only this plan's status row when complete.

Out of scope: UI, SQLite, authentication, Git subprocess implementation, cloud, retention, scheduling, dependency upgrades, Git initialization/commits, and code claiming a working backup engine.

## Steps

### 1. Resolve the contract explicitly

Define raw ref object IDs, a discriminated HEAD representation, object format, full-only prerequisites, exact bundle filename, snapshot IDs, UTC timestamps, checksum and size, and coverage exclusions. Make malformed combinations unrepresentable or rejected by validation.

Represent every captured valid ref without silently narrowing the existing “refs available in the mirror” coverage. Do not restrict the contract to branches and tags. Define symbolic, detached, unborn, and absent HEAD separately. Plan 001 defines representations; Git preservation qualification occurs in plans 002/003. An operation whose required capability is unqualified must fail explicitly, but this is not approval to remove that state from the intended contract. Represent SHA-1 and SHA-256 using format-specific IDs; capability-test actual operations later.

Record verification dimensions separately: manifest, checksum, self-contained prerequisites, refs/HEAD, object integrity, offline restore. Permit valid extra packed objects in this initial format; extra unreachable objects do not necessarily imply `fsck` failure. Require object integrity without promising exact object minimality, authenticity, encryption, or secret removal.

Verify: `go test ./...` remains green; compare every contract choice with `docs/BACKUP-FORMAT.md` and the newly added tests. STOP before implementing an unapproved scope reduction, not merely because actual Git qualification is scheduled in the next plan.

### 2. Implement bounded manifest parsing and typed results

Use the standard library first. Bound input bytes, reference count/name lengths, and total metadata size with named documented limits. Require exact version-one supported values and reject unsafe filenames/path components, duplicate JSON keys, trailing documents, invalid ref names, inconsistent OID lengths, negative sizes, invalid checksums, and contradictory HEAD fields. Establish a safe Git-compatible ref validator and test its grammar; do not assume a slash prefix makes a name valid.

Use typed no-artifact `empty_repository` results. A tag-only or detached-history repository must not be classified from branch count. Keep domain result values independent of MyGo.

Verify: `go test ./internal/backup -run 'TestManifest|TestResult' -count=1` exits 0 and executes the named tests. `go vet ./...` exits 0.

### 3. Add round-trip and negative contract tests

Table-test supported HEAD/format combinations, annotated-tag raw IDs, duplicate refs/keys, unsupported version/kind/prerequisites, malicious path strings, size/count bounds, invalid UTF-8 policy, and incomplete metadata. Marshal then parse supported manifests and compare semantic equality. No real credentials or private repositories.

Verify: `go test ./internal/backup -count=1`, `go test ./...`, `go vet ./...`, `go build ./...` all exit 0; `gofmt -l main.go internal` prints nothing.

## Done criteria

- Tests named `TestManifestRoundTrip`, `TestManifestRejectsInvalidInput`, and `TestResultEmptyRepository` execute and pass.
- All approved contract choices are documented, with coverage limitations explicit.
- No parser success accepts unsupported incremental prerequisites or external artifact paths.
- All verification commands above pass; no UI source or dependencies change.
- `plans/README.md` marks 001 DONE only when verification and approval evidence is recorded.

## STOP conditions

Stop if baseline inputs drift unexpectedly, implementation requires reducing existing coverage, a safe ref/object-format rule cannot be justified, source changes require files outside scope, or verification fails twice. Do not implement later engine steps to make this contract appear complete.

## Maintenance

Changing artifact semantics after publication requires compatibility/version policy. Store raw annotated-tag IDs. A full bundle need not be a minimal object pack; keep claims precise. Later runners and restores must use this validator rather than duplicate manifest rules.
