# Testing strategy

The canonical [task specifications](implementation/TASKS.md) define new test names, scopes, and per-slice gates. Tests listed as future deliverables do not exist until their task is implemented. Confirm enumeration and execution; a `go test -run` pattern that matches zero tests is not a pass for that behavior.

## Current checks

```sh
go test ./...
go vet ./...
go build ./...
```

`internal/desktop/app_test.go` uses MyGo's headless tester to check the initial disclosure and all navigation destinations. It requires no display or real GitHub credentials.

The CI definition runs formatting, module consistency, tests, vet, and compilation on Linux, macOS, and Windows. A green compile does not prove a usable desktop package.

### Initial scaffold verification

Checked on Linux/amd64 with Go 1.27.1 and MyGo v0.6.2:

- `go test ./...`: passed, including headless view/navigation tests.
- `go vet ./...` and `go build ./...`: passed.
- `go tool mygo doctor`: found Go and GTK 3; confirmed WebKit is not needed.
- `go tool mygo build`: produced a Linux executable, desktop entry, Debian package, and tar archive.
- `GOOS=windows GOARCH=amd64 go build ./...`: cross-compilation passed.
- `GOOS=darwin GOARCH=arm64 go build ./...`: cross-compilation passed.
- Local Markdown link targets and `mygo.json` syntax: checked.

Not checked: a real desktop window session, Windows/macOS runtime behavior, screen-reader behavior, signed installers, or hosted CI execution. No backup engine exists to validate yet.

## Engine test pyramid

[ACCEPTANCE.md](ACCEPTANCE.md) supplies stable REC/SAFE/STATE/DATA/REL outcome IDs. Implementation evidence should map required behavioral tests to those outcomes without treating this planning ledger as already passing.

| Layer | Required coverage |
| --- | --- |
| Unit | Manifest bounds, IDs/paths, state transitions, retry classification, retention, schedules |
| Git integration | Full bundle offline round trip using temporary repositories |
| Persistence | Migrations, transactions, restart reconciliation, interrupted publication |
| Job integration | Double-start, per-mirror ownership, cancellation and process trees |
| HTTP integration | Fake GitHub server: pagination, 401/403/404/429, rate-limit reset, timeout |
| Cloud adapter | Test double for upload/retry; opt-in real S3 and R2 verification |
| Native UI | Input validation, events, start/cancel states, errors, snapshot selection |
| Desktop smoke | Window/tray lifecycle, keyboard/accessibility, packaging on each OS |

## Mandatory recovery fixtures

Create all fixtures in `t.TempDir()` with synthetic author identity and no user Git configuration. Cover multiple branches, annotated and lightweight tags, a non-main default branch, detached/absent HEAD policies, branch/tag deletion between snapshots, empty repositories, and supported object formats.

Remove or make the source and mirror inaccessible before restoration. Compare exact saved references and run `git fsck --full`. Do not accept “bundle create exited zero” as the only assertion.

## Failure injection

- Kill the process after each write, rename, database transition, and remote publication boundary.
- Simulate disk full, unwritable directories, locked files, and an existing restore destination.
- Modify bundle bytes and manifest fields. Test missing and extra references.
- Try traversal, absolute paths, symlink escapes, malicious URLs, and oversized input.
- Cancel during discovery, fetch, bundling, verification, and upload.
- Simulate a cloud failure after local publication, and a lost upload response after remote success.
- Test no available OS secret store, expired credentials, and unavailable network.
- Confirm concurrent application processes cannot write the same backup root.
- Confirm terminal events persist even if the UI event channel is saturated.
- Inject post-publication sync/database failure and preserve the published artifact ID with uncertainty instead of pretending no output exists.
- Test monthly last-day resolution, DST gap/fold, atomic occurrence/run admission, clock rollback, and a single catch-up offer without automatic dispatch.
- Verify deletion revision changes invalidate a preview; retries touch only authorized remaining effects and do not resurrect deleted artifacts.
- Test native secret-prompt non-cancellability and unattended prompt refusal without real credentials.

## Performance and race checks

When engine concurrency exists, run `go test -race ./...` on supported host toolchains. Race builds may need platform C tooling even though MyGo's normal build does not. Measure startup, memory, UI response, disk use, bundle size, and restore duration on representative repositories. Do not advertise the framework homepage's footprint as GitVault measurements.

## Cloud and secret tests

Never put real credentials in fixtures or CI logs. Use a private secret mechanism for opt-in provider tests, isolated bucket prefixes, and a documented cleanup plan. Unit and default integration tests must not require paid services.
