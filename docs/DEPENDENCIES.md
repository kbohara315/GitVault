# Integration research and dependency decisions

**Status: selected technical directions, with candidate additions still subject to qualification.** Primary-source review dated 2026-10-22; no dependency was installed or locally compiled by this documentation task. Research establishes API/version facts, not platform support, operational guarantees, credential permission, or release approval. Do not add unused infrastructure before its vertical slice.

## Pins and module seams

| Need | Direction | Admission gate |
| --- | --- | --- |
| Go | retain Go **1.27.1** toolchain pin | exact toolchain in local/CI build evidence |
| Native UI and build tool | retain MyGo **v0.6.2** pin | verify APIs against this tag, qualify each shipped OS/architecture |
| SQLite | selected `modernc.org/sqlite v1.61.0`, `database/sql` | requires Go 1.26+ and matching `modernc.org/libc v1.79.0`; locally compiled and tested on Linux/amd64 with Go 1.27.1; other target runtime behavior remains unqualified |
| Schema migration | embedded ordered SQL, one transaction per migration | no ORM or migration framework; test rollback and refusal paths |
| Secret store | existing MyGo native Secrets behind narrow adapter | no new keychain dependency by default; qualify prompts/OS behavior |
| GitHub discovery | standard `net/http`, narrow typed decoding | bounded pagination/responses, rate-limit tests, no UI networking |
| Git | installed executable behind controlled runner | measured capability minimum, argv/context/output/process-tree qualification |
| S3/R2 | candidate AWS SDK v2 `service/s3 v1.114.4` | stable service API; file-only bounded multipart adapter, compatible module graph review |
| Scheduling | standard `time`, persisted occurrence identity, fake clock interface | not MyGo view/frame timers; stopped process cannot schedule |

Choose interfaces at the consuming module's boundary. The composition module instantiates adapters; keep MyGo out of backup/recovery modules. Preserve locality: SQLite owns transactions, cloud owns provider semantics, secret adapter owns native calls, and the application coordinator owns admission and immutable settings. This provides test leverage without generic infrastructure packages or unrelated stack comparisons.

At implementation time review these exact candidate versions and pin their **complete compatible module graph** in `go.mod`/`go.sum`; no silent `@latest`. If review requires a different version, document evidence, update the plan explicitly, and requalify its API. Record licenses including transitive/embedded code, vulnerability review, supported build pairs, checksums, and reproducible CI results. Candidate version dates or successful cross-compilation alone do not establish runtime support.

## MyGo v0.6.2: UI bridge and lifecycle

Versioned primary sources:

- [mygo.go](https://github.com/egoist/mygo/blob/v0.6.2/mygo.go): initialization locks the OS thread; `App.Run` belongs in main.
- [loop.go](https://github.com/egoist/mygo/blob/v0.6.2/loop.go): public `RunOnMain(func())` is synchronous; off-main callers wait. Private `postMain` is not an application API. Its internal unbounded queue is not the engine event transport.
- [content.go](https://github.com/egoist/mygo/blob/v0.6.2/content.go): `Window.Invalidate()` is goroutine-safe and coalesces requests, but only schedules redraw; it does not make off-thread model mutation safe.
- [ui/context.go](https://github.com/egoist/mygo/blob/v0.6.2/ui/context.go): view contexts/elements have view-call lifetime. `Context.After(duration)` requests a later frame, not a callback or durable scheduled job.
- [app.go](https://github.com/egoist/mygo/blob/v0.6.2/app.go), [events.go](https://github.com/egoist/mygo/blob/v0.6.2/events.go), [window.go](https://github.com/egoist/mygo/blob/v0.6.2/window.go): readiness, preventable close/quit, last-window policy, show/hide, and activation.
- [modules.go](https://github.com/egoist/mygo/blob/v0.6.2/modules.go), [Linux menu](https://github.com/egoist/mygo/blob/v0.6.2/internal/linux/menu.go), [power.go](https://github.com/egoist/mygo/blob/v0.6.2/power.go): tray creation after ready, menu-based Linux behavior, suspend/resume hints. Library presence/listener registration does not prove desktop tray visibility or wake delivery.

Use a bounded immutable-event channel and one bridge goroutine. Coalesce intermediate progress, apply bounded batches through `RunOnMain`, mutate UI state only there, then invalidate. Persist terminal outcomes independently of delivery. Never spawn one goroutine per event or retain a view context in a worker.

Deadlock gate: do not hold engine locks across synchronous `RunOnMain`; do not wait for a bridge/worker inside a main-thread close/quit callback when it may await `RunOnMain`. Prevent quit with a closing guard, cancel asynchronously, persist and finish cleanup, then request quit. Do not depend on post-loop UI callbacks as completion acknowledgements. Tests must cover quit/cancel while a bridge batch is pending, hidden windows, terminal delivery, tray-unavailable behavior, reopen, and close versus actual quit on each release platform.

## Native secret adapter

Prefer the already-pinned facility, composed behind the engine's narrow secret-store interface:

- [secrets.go](https://github.com/egoist/mygo/blob/v0.6.2/secrets.go): `mygo.Secrets.Service(service)` returns a namespace supporting `Set(key, []byte)`, `Get(key)`, and `Delete(key)`. Maximum value **2,560 bytes**, service/key maximum **256 bytes**. Calls are goroutine-safe, may block on OS unlock/consent, and expose **no context cancellation**. Missing and unsupported conditions are distinct; absent-item deletion succeeds.
- [macOS backend](https://github.com/egoist/mygo/blob/v0.6.2/internal/darwin/secrets.go): direct Security.framework via purego, file-based login keychain; not the data-protection keychain requiring signing/access-group entitlement. No secret-bearing helper argv.
- [Linux backend](https://github.com/egoist/mygo/blob/v0.6.2/internal/linux/secrets.go): direct synchronous libsecret APIs; requires `libsecret-1.so.0` (libsecret >=0.19), a session bus, and an operational Secret Service. A named desktop/keyring product alone is not qualification.
- [Windows backend](https://github.com/egoist/mygo/blob/v0.6.2/internal/windows/secrets.go): direct generic Credential Manager operations with enterprise persistence. Profile/domain roaming may apply; do not claim strictly machine-local storage.

Use stable separate development/production service identifiers and opaque reference/generation keys. Bound outstanding native operations to one; timeout wrappers must not leak prompt-waiting goroutines. Cancellation can decline a late result but cannot terminate the OS prompt. Unattended scheduling must not initiate prompts. MyGo has no no-prompt flag; fail unattended retrieval closed unless a noninteractive adapter capability is qualified or an explicitly established bounded in-memory credential lease remains valid. Designing/qualifying that capability is still an implementation gate, not a property established by the research. No plaintext fallback, secret-bearing URL/argv/log, or arbitrary native error dump.

Qualify synthetic sentinel round trips, maximum bytes, missing/denied/locked/unavailable distinctions, cancellation/shutdown with pending prompts, Linux missing bus/service/library, signed/rebuilt macOS prompts, and Windows roaming expectations. The production identifier and credential-use permission remain separate gates.

Alternative, **not selected by default**: [go-keyring v0.2.8 release](https://github.com/zalando/go-keyring/releases/tag/v0.2.8), [macOS source](https://github.com/zalando/go-keyring/blob/v0.2.8/keyring_darwin.go). Its macOS Set launches `/usr/bin/security -i` and sends the encoded secret-bearing command through **stdin, not exec argv**. Do not repeat the false argv vulnerability claim. Its 4,096-byte command limit is checked after process start; some error paths omit pipe/process cleanup, and calls lack application context cancellation. Base64 is not encryption. [Linux](https://github.com/zalando/go-keyring/blob/v0.2.8/keyring_unix.go) attributes and [Windows](https://github.com/zalando/go-keyring/blob/v0.2.8/keyring_windows.go) target names differ from MyGo, so adapters are not silently interchangeable. Prefer the existing native mechanism unless a concrete requirement justifies qualifying another adapter and explicit secret migration.

## SQLite and migrations

Primary sources: [v1.61.0 module](https://github.com/modernc-org/sqlite/blob/v1.61.0/go.mod), [package docs](https://pkg.go.dev/modernc.org/sqlite@v1.61.0), [driver.go](https://github.com/modernc-org/sqlite/blob/v1.61.0/driver.go), [canonical repository](https://gitlab.com/cznic/sqlite). This release requires Go 1.26.0 and **libc v1.79.0**; keep exactly the libc version selected by its module, not an independent libc upgrade. GitVault's `go.mod`/`go.sum` pin the resolved graph. The module is CGo-free, but its BSD-3-Clause driver, transpiled public-domain SQLite code, and bundled third-party notices still carry redistribution obligations. Its `LICENSE-3RD-PARTY.md` separates linked code from graph-only components (including an MPL-2.0 graph-only dependency); `sqlite-vec` is not imported. Linux/amd64 compile, store tests, and race tests pass with Go 1.27.1. Other target runtime behavior and the app's distribution notice packaging remain unqualified. Documented upstream OS/architecture pairs are not GitVault platform qualification.

Selected implementation policy:

1. Use `database/sql`, driver name `sqlite`, one open and one idle connection initially. Keep SQLite work outside native views. Transactions and context propagation stay inside the store adapter.
2. Build trusted escaped `file:` URIs; no user DSN fragments. A plain pathname plus `?mode=ro` does **not** reliably select read-only mode. Test read-only inspection against attempts to create/write.
3. Configure foreign keys **per physical connection**, busy timeout **5,000 ms**, synchronous **FULL**, and journal **WAL** as the initial technical defaults. Read back effective values, including returned journal mode; refuse a required policy that did not take effect. Trusted v1.61.0 shorthand parameters and `sqlite.StrictPragmas(true)` must be checked against the exact driver API. A one-time pragma on an unconstrained pool is insufficient.
4. Embed ordered SQL migrations and a version/applied-time/checksum ledger. Acquire process ownership before migration; use one connection and transaction per migration. Commit schema and ledger atomically, refuse newer/unknown versions or altered/missing history, never auto-downgrade. Keep SQLite operations forbidden within transactions, such as journal-mode changes or VACUUM, outside them.
5. Preserve the database on failure. Test rollback, cancellation, reopen, concurrent access refusal, connection replacement pragmas, forward upgrade, future-schema refusal, settings revision atomicity, and crash/restart reconciliation.

SQLite primary references: [foreign_keys](https://www.sqlite.org/pragma.html#pragma_foreign_keys), [journal_mode](https://www.sqlite.org/pragma.html#pragma_journal_mode), [user_version](https://www.sqlite.org/pragma.html#pragma_user_version), [transactions](https://www.sqlite.org/lang_transaction.html), [WAL](https://www.sqlite.org/wal.html). WAL still has one writer and local shared-memory requirements; no research-based power-loss or network-filesystem guarantee is asserted. Bound busy waits by operation context and test actual failure behavior.

## S3/R2 file transport adapter

Candidate **`service/s3 v1.114.4`**, using provider signing/HTTP machinery rather than reimplementing it. [Versioned source](https://github.com/aws/aws-sdk-go-v2/tree/service/s3/v1.114.4/service/s3) and [module](https://github.com/aws/aws-sdk-go-v2/blob/service/s3/v1.114.4/service/s3/go.mod) define the exact API and compatible dependency graph. Do not infer all AWS v2 module versions share one version number.

Selected direction: known-size immutable verified local files only. `PutObject` handles smaller files. For multipart, persist application-owned target/key/upload ID, use `CreateMultipartUpload`, bounded `UploadPart` over file-backed `io.SectionReader` ranges, then `CompleteMultipartUpload`. On cancellation/failure call `AbortMultipartUpload` using a **fresh bounded cleanup context**, not the cancelled transfer context. Persist failed/unknown cleanup and reconcile after restart using exact application-owned IDs; never sweep an entire bucket/prefix or another client's upload. Handle crash windows between remote creation and local receipt by reconciling unique owned keys, not assuming persistence is atomic across services.

Apply finite settings budgets from `CONFIGURATION.md`, one retry owner, global transfer/part limits, bounded bodies/diagnostics, provider part/count limits, and closed file/body handles. Publish manifest last under unique snapshot keys. A multipart receipt is not proof of all remote bytes or conditional no-replace semantics; qualify provider-specific conditional publication. Cloud failure keeps verified local snapshots usable.

### High-level upload alternatives

- [Legacy `feature/s3/manager v1.23.15` upload.go](https://github.com/aws/aws-sdk-go-v2/blob/feature/s3/manager/v1.23.15/feature/s3/manager/upload.go): uploader is **deprecated** in favor of transfermanager. Its automatic abort uses the original context and ignores abort errors. Avoid it for the new adapter. The [AWS utility guide](https://docs.aws.amazon.com/sdk-for-go/v2/developer-guide/sdk-utilities-s3.html) remains useful but does not supersede versioned deprecation/cleanup evidence.
- [`feature/s3/transfermanager v0.4.18`](https://github.com/aws/aws-sdk-go-v2/tree/feature/s3/transfermanager/v0.4.18/feature/s3/transfermanager) is a candidate alternative, **not the default**. Exact API: `transfermanager.New(s3Client, ...func(*Options))`, then `UploadObject(ctx, *UploadObjectInput, ...func(*Options))`. Options include `PartSizeBytes`, `MultipartUploadThreshold`, `Concurrency`, `MaxUploadParts`, and `FailTimeout`. Its [module](https://github.com/aws/aws-sdk-go-v2/blob/feature/s3/transfermanager/v0.4.18/feature/s3/transfermanager/go.mod) requires Go 1.24 and s3 v1.114.4; version requirements fit the toolchain, not locally built here.
- v0.4.18 buffers chunks **even for os.File**; do not transfer the old manager's ReadSeeker/ReaderAt no-part-buffering claim to it. Its defaults (8 MiB parts, 16 MiB threshold, five workers per call) are not GitVault's selected defaults. Global concurrency/memory still need enforcement. `FailTimeout > 0` creates a fresh timeout context for failure cleanup; `MultipartUploadError.UploadID()` exposes failed IDs. Complete failure includes raw `log.Printf`, requiring logging/redaction qualification. A v0.x module is not a stability promise. If adopted later, replace—not duplicate—the custom multipart adapter and requalify the same consumer interface.

### Provider and verification qualification

Primary sources: [Cloudflare Go example](https://developers.cloudflare.com/r2/examples/aws/aws-sdk-go/), [R2 S3 compatibility](https://developers.cloudflare.com/r2/api/s3/api/), versioned [HeadObject](https://github.com/aws/aws-sdk-go-v2/blob/service/s3/v1.114.4/service/s3/api_op_HeadObject.go) and [GetObject](https://github.com/aws/aws-sdk-go-v2/blob/service/s3/v1.114.4/service/s3/api_op_GetObject.go).

R2 uses an approved HTTPS account `BaseEndpoint` with region `auto` and an explicit credential provider. Do not copy hard-coded keys or printed presigned URLs from examples. Qualify checksum defaults/trailers, headers, addressing, and request retries separately on S3 and R2; do not blanket-disable integrity to obtain compatibility. The reviewed R2 matrix supports checksums, including CRC64NVME FULL_OBJECT and several COMPOSITE types. Multipart SHA256 may be composite, not the manifest's full-file SHA256. Atomic conditional PUT evidence does not prove identical multipart semantics.

Record transport receipt (key, size, opaque ETag/provider checksum) separately from read-back verification. HEAD or echoed digest metadata is not byte verification. Portable read-back GETs every byte, closes the body, independently counts/hashes it, and compares the trusted expected SHA256/size; bundle validation and restore are further checks. A replaceable manifest plus checksum does not authenticate an attacker-controlled backup.

Required tests: retry ownership/attempt caps, bounded memory/file reads, part ordering/count, cancellation during each stage, abort under fresh context, durable failed cleanup, crash/restart orphan reconciliation, manifest-last publication, redacted errors, remote retrieval, and independent read-back restoration. Opt-in real S3 **and** R2 smoke tests require explicit credentials/resource permission; mocks and research cannot close that gate.

## Handoff and release gates

Each implementation task records exact pins, sources, interface contract, synthetic fixtures, qualification results, and remaining limitations. Run formatting, `go test ./...`, `go vet ./...`, and `go build ./...` when code/dependencies change, plus native and provider tests on the actual release matrix. This documentation-only task installs/builds nothing. Application identity/license, platform support, signing, cloud disclosure, credential use, destructive operations, and publication remain separate explicit decisions; selected technical defaults do not impersonate those approvals.
