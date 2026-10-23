# Development and operations

Implementation execution starts at [AGENT-RUNBOOK.md](AGENT-RUNBOOK.md). [RECOVERY-SEMANTICS.md](RECOVERY-SEMANTICS.md) defines operational recovery; [CONFIGURATION.md](CONFIGURATION.md) is the only settings/defaults authority. Documentation does not grant signing or publication permission.

## Local commands

```sh
go mod download
go tool mygo doctor
go test ./...
go vet ./...
go build ./...
go tool mygo dev
```

Use the pinned CLI through `go tool mygo`. Do not use `@latest` for routine builds. MyGo v0.6.2 requires Go 1.27.1; automatic toolchain selection needs download access on the first run.

No `.env` file is required. The Repositories page accepts a masked GitHub PAT into the OS credential store. An explicit discovery action uses bounded HTTPS REST requests to list repository metadata; it does not fetch Git data or start a backup. Synthetic tests only were used for I07; no live GitHub request or real credential was used. Do not add credentials to `mygo.json`. Default builds use the development secret namespace; production namespace selection is a build tag, not release qualification.

## Packaging

```sh
go tool mygo build
go tool mygo build -platform darwin/universal,windows/amd64,linux/amd64
```

Output goes to `build/`. Native-only Linux configuration sets `linux.webview` to false. Runtime GTK 3 is still required on Linux. Test Windows and macOS packages on those operating systems; cross-compilation is not platform qualification.

The scaffold has no custom icon or release-signing configuration. Add an original square PNG at `resources/icon.png` before distributing branded builds. No production release automation is configured.

## Troubleshooting

| Symptom | Action |
| --- | --- |
| Go reports a newer version is required | Install Go 1.27.1+ or enable normal automatic toolchain selection |
| Dependency/toolchain download fails | Check network/proxy access; do not lower `go.mod` below MyGo requirements |
| `go tool mygo` is unavailable | Run from the project root and run `go mod download` |
| Window does not open on Linux | Run doctor; verify graphical session, GTK 3, and graphics support |
| Headless environment has no display | Run UI tests; defer window smoke testing to a desktop session |
| Looking for backup output | None is produced by this scaffold; the engine is not implemented |

## Planned operational behavior

- Use OS application-data paths for SQLite/logs, not the source checkout.
- Keep backup root user-selected and validate it before writes.
- Show local and cloud results separately, with time and verification level.
- Bound and rotate redacted logs. Define retention before adding telemetry or crash reporting.
- No telemetry by default. Any future reporting needs explicit consent and data review.
- Never automatically delete backups to solve a disk-space error.
- In-app scheduling does not run while the app is stopped. Handle missed runs explicitly.
- Retain snapshots independently of mirrors. A damaged mirror can be recreated without deleting snapshots.

## Release gates

- [ ] Replace placeholder module path and application identifier.
- [ ] Choose license, check dependencies, confirm product-name availability.
- [ ] Add original icon, release notes, and private security reporting.
- [ ] Complete all required backup/restore/security acceptance tests.
- [ ] Test native runtime, secret store, tray behavior, and keyboard/screen-reader access on each supported OS.
- [ ] Verify offline restoration using only exported snapshots and Git.
- [ ] Test interrupted writes, disk exhaustion, cancellation, and root locks.
- [ ] Validate S3 and R2 if those features are shipped.
- [ ] Document minimum Git/OS versions from actual tests.
- [ ] Sign Windows/macOS packages and notarize macOS distribution as required.
- [ ] Define update signing and rollback behavior before enabling auto-updates.
- [ ] State bundle coverage and lack of client-side encryption prominently.

Do not publish benchmark, security, or platform claims that have not been measured or tested.
