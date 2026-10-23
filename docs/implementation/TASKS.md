# Full-version execution tasks

**Implementation authorized by the user.** Scoped application code and synthetic tests/builds are permitted; real credential access, real-data deletion, signing and publication retain separate gates. Status/evidence below determine actual completion. Listed commands/tests are requirements, not automatically passing results.

Read [the runbook](../AGENT-RUNBOOK.md), [MODULES](../MODULES.md), [RECOVERY-SEMANTICS](../RECOVERY-SEMANTICS.md), [CONFIGURATION](../CONFIGURATION.md), [DEPENDENCIES](../DEPENDENCIES.md), [BACKUP-FORMAT](../BACKUP-FORMAT.md), and [CONTEXT](../../CONTEXT.md). [tasks.json](tasks.json) is the machine-readable matching DAG, scope and gate inventory. Neither file may silently override the other. Historical plans 001–003 preserve rationale only; their baseline is not a continuing drift gate and their existence proves no completion.

## Common contract for every task

- Retain Go 1.27.1, MyGo v0.6.2 and installed Git. Pin/review any new dependency. Begin with one deep `internal/backup`; put admission/orchestration in `internal/app`. Add adapters/configuration only with actual consumers. Keep backup logic and blocking I/O outside native views; no speculative framework, universal domain bag, service locator, plugin or shell-hook configuration.
- Full bundles only; no prerequisites. Preserve exact captured valid refs, raw annotated-tag IDs and qualified symbolic/detached/unborn HEAD states. Represent/classify absent HEAD explicitly, but reject it with no artifact when Git cannot recognize/preserve it; schema representation is not a restoration support claim. Qualify SHA-1/SHA-256 actual operations separately. Valid extra packed objects are allowed; integrity and exact saved refs/HEAD are mandatory. Mirror disposable, snapshot immutable. Full restoration needs only snapshot and qualified installed Git, not SQLite/account/source/network.
- Operation outcome, artifact identity/publication/durability, verification dimensions and cloud-copy result are separate facts. Qualified no-replace publication is visibility commit; retain identity/output after post-commit errors. No `Stat` then ordinary replacing rename. No downgrade of verification or automatic older-snapshot deletion to satisfy disk limits.
- Bound bytes/output/concurrency/retries/time; context-first methods, argv subprocesses, wrapped causes. Cancel terminates/reaps owned descendants before cleanup/unlock. A returned cancellation is not rollback. Secrets never enter URLs, argv, settings, manifests, diagnostic logs or real-data fixtures; use synthetic sentinels.
- Default progress is bounded/nonblocking/coalescible. Terminal truth is durable, not dependent on a UI event. UI mutation belongs to main. MyGo `RunOnMain` is synchronous: no main/worker lock or shutdown wait cycle. Native secret prompts may outlive cancellation; bound outstanding calls and reject stale results.
- Each task owns the listed paths **plus** only its status/evidence/capability updates in `README.md`, `docs/{README,IMPLEMENTATION,TESTING}.md`, this file and `tasks.json`. Reserve these shared files before editing. A glob is a scope ceiling, not permission for unrelated rewrites. Scope expansion needs explicit approval and a recorded plan change.
- Before edits capture a per-task predecessor/input SHA-256 inventory and authorized path set; after edits compare actual changes, including new/deleted files. Legitimate predecessor/doc changes are new inputs, not fixed-baseline violations. Investigate unexplained changes; do not discard another worker's work.
- **G (every task):** `gofmt -l main.go internal` (plus every added Go source directory) exits 0 with empty stdout; `go test ./...`, `go vet ./...`, `go build ./...` exit 0. Apply `gofmt` only to owned Go edits. Named gates also require exact tests to exist and execute/pass without skip. Check `go test PACKAGE -list .` first and retain `-json` execution evidence; a regex matching nothing fails. Use anchored commands from JSON. Broad package commands below supplement, not replace, named gates.
- **Review/stop (every task):** independent, read-only user-requested Sol6.1 review; resolve actual model ID, do not invent it. At most two correction rounds, then stop on unresolved findings. Never self-approve or weaken a test/invariant to clear review. Missing prerequisite/evidence, unexplained drift, scope expansion, unavailable required reviewer, failed safety gate or human-only decision blocks the task. Host implementation and pending foreign-platform qualification are distinct; no skipped/invented pass or cross-compile-as-runtime claim.
- Required package commands below are not presently verified and packages/tests may not exist. All fixture repositories/databases/targets are temporary synthetic data. No developer repositories or saved credentials. Evidence includes command/exit, test run/pass/skip events, exact inputs/output hashes, OS/tool/Git/dependency versions, failure injection, observed safety outcome and remaining limitations.

## DAG and initial tracker

| ID | Slice | Dependencies | Status |
| --- | --- | --- | --- |
| I01 | Bounded full-snapshot contract | — | DONE |
| I02 | Controlled Git and process-tree lifetime | I01 | DONE (Linux runner) |
| I03 | Offline full snapshot and no-replace bare restore | I01, I02 | DONE (Linux host; other platforms/filesystems pending) |
| I04 | SQLite durable jobs and reconciliation | I03 | DONE (Linux host; other OS/filesystems pending) |
| I05 | Typed revisions and responsive native local workflow | I04 | DONE (Linux/amd64 implementation scope; broader qualification pending) |
| I06 | MyGo-native secrets and PAT broker onboarding | I05, I02 | DONE (Linux/amd64 implementation scope; native locked/pending-prompt and other platforms pending) |
| I07 | GitHub REST pagination and explicit stable selection | I06, I05 | DONE (Linux/amd64 implementation scope; provider/native UI qualification pending) |
| I08 | Selected GitHub-to-local snapshot pipeline | I07, I03, I04, I05, I06 | TODO |
| I09 | History, import and offline restore UI | I03, I04, I05 | TODO |
| I10 | Optional S3/R2 copy of ready artifacts | I08, I06, I09 | TODO |
| I11 | Remote retrieval through the same restore path | I10, I09 | TODO |
| I12 | Retention preview with exact revision-bound scope | I09, I10, I11 | TODO |
| I13 | Approved retention execution and partial-delete recovery | I12, I04, I10 | TODO |
| I14 | Scheduler through shared admission | I08, I05 | TODO |
| I15 | Tray, window and quit lifecycle | I14, I06, I09 | TODO |
| I16 | Qualified packaging and release | I11, I13, I15 | TODO |

Dependency availability is not edit-disjointness: I09/I14 can be logically available while other work proceeds but share coordinator/views/docs. Default to one writer; do not run overlapping edits in parallel. I16's dependency closure includes every slice.

## I01 — Bounded full-snapshot contract

**Purpose:** one portable interpretation before producing bytes. **Dependencies:** none. **Status:** DONE (pure-Go host contract; see evidence/I01.md).

**Allowed:** `internal/backup/{doc.go,manifest*.go,result*.go}`, `docs/{BACKUP-FORMAT,MODULES,RECOVERY-SEMANTICS}.md`, common status paths.

**Steps:**
1. Define version/full-kind, snapshot/repository identity, UTC time, bundle filename, size, SHA-256, full-only prerequisites and coverage exclusions.
2. Preserve all captured valid refs/raw OIDs, raw annotated tags, object format and independent HEAD alternatives; do not infer empty history from branch count.
3. Implement bounded parse/validate/round-trip; reject duplicate keys/refs, trailing documents, invalid encoding, unsupported values, unsafe paths/ref grammar, inconsistent OID widths and contradictory HEAD.
4. Define typed empty no-artifact and independent operation/publication/verification results. Permit valid extra objects; promise no authenticity, minimal pack or secret removal.

**Config:** concrete backup options/limits, no `internal/config` yet. **Exclude:** subprocesses, UI, database, new dependencies, cloud.

**Tests/gates:** `manifest_contract`: `TestManifestRoundTrip`, `TestManifestRejectsInvalidInput`, `TestManifestBounds`, `TestManifestRefsAndHead`, `TestResultPublicationFacts`. Run `go test ./internal/backup -count=1` plus exact JSON gate and G; expect actual named run/pass, malformed/boundary rejection and semantic equality.

**Failure/cancel/restart:** invalid input has no effects; bounded parser remains bounded. No durable state to reconcile. **Stop:** unjustified grammar/format, silent coverage reduction. **Platform evidence:** host pure-Go parser tests; representation is not Git preservation qualification.

## I02 — Controlled Git and process-tree lifetime

**Purpose:** safe private installed-Git adapter. **Dependencies:** I01. **Status:** DONE for Linux execution; Darwin/Windows fail closed and remain unqualified.

**Allowed:** `internal/backup/{git*.go,process*.go,runner-policy.md}`, `docs/{SECURITY,DEPENDENCIES}.md`, common status paths.

**Steps:**
1. Resolve executable; generate explicit argv and deliberate runtime environment. Neutralize inherited/config-local helpers, hooks/templates, alternates, replacements, partial-clone lazy fetch, prompts/pager/editor and unauthorized protocols.
2. Distinguish explicit local-source policy from strictly offline restore. Caller cannot supply arbitrary commands/environment.
3. Bound machine stdout/diagnostics, deadlines and drain time. Own descendants even when parent exits before a grandchild closes a pipe; terminate and reap before return.
4. Probe actual temporary-repository Git/object-format operations; report missing/unsupported capabilities explicitly. Record minimum supported capabilities from evidence, not guessed version.

**Config:** private finite runner options; dependency changes are not in default scope—stop for reviewed scope amendment if OS support needs one. **Exclude:** authentication, persistent mirrors, publication, UI/global Git edits.

**Tests/gates:** `git_lifetime`: `TestGitEnvironmentIsolation`, `TestGitTransportPolicy`, `TestGitOutputLimits`, `TestGitCancellation`, `TestGitDeadline`, `TestGitCapabilities`. `go test ./internal/backup -count=1`, exact JSON gate and G: all six run/pass; hostile sentinels never execute and no owned writer survives return.

**Failure/cancel/restart:** unavailable capability fails before writes; cancellation/deadline drains bounded pipes and reaps tree before cleanup/unlock. **Stop:** isolation/tree guarantee unproved, unbounded output needed, unreviewed dependency. **Platform evidence:** native process fixtures on Linux/macOS/Windows for each claim; unsupported host is explicit, not a skipped successful integration suite.

## I03 — Offline full snapshot and no-replace bare restore

**Purpose:** first end-to-end recoverable artifact. **Dependencies:** I01, I02. **Status:** DONE for Linux host scope only; macOS/Windows execution and additional filesystem/crash qualification remain pending. Retained evidence: `/tmp/opencode/gitvault-i03-evidence/I03-record.md`.

**Allowed:** `internal/backup/{engine*,create*,verify*,restore*,filesystem*,ownership*}.go`, `docs/{BACKUP-FORMAT,RECOVERY-SEMANTICS}.md`, `.github/workflows/ci.yml` only to execute real engine tests, common status paths. Necessary dependency/scope changes require review first.

**Steps:**
1. Qualify canonical root/overlap/alias policy, cooperating-process and mirror locks, stable-parent trust/no-follow boundary, same-filesystem private staging and no-replace primitive.
2. Race publication against a competing file, empty directory and symlink. Preserve competitor and never clean another operation's data.
3. Acquire disposable local mirror without hardlink/alternate dependence. Capture exact refs/HEAD under exclusive mutation control; qualify detached-only transfer, tag-only/empty/unborn and both object formats. For absent HEAD that Git cannot recognize/preserve, assert an explicit unsupported/corrupt-input result and no published artifact; never invent a branch or claim absent-state restoration.
4. Create no-prerequisite bundle; verify fresh import, exact refs/HEAD and object integrity, hash/size then publish manifest and bundle together. Restore via privately copied immutable inputs into a new bare destination; no source/mirror/database/network.
5. Inject ENOSPC/read/write/close/sync/rename/cancel faults; verify budgets and owned cleanup, including post-commit error identity.

**Config:** concrete consumed limits; verification/no-replace never optional. **Exclude:** UI, database, GitHub, working copy, LFS/cloud.

**Tests/gates:** `offline_recovery`: `TestOfflineRoundTrip`, `TestSnapshotRefAndHeadCoverage`, `TestPublishNeverReplaces`, `TestPublishRejectsUnsafePaths`, `TestConflictingRoots`, `TestConcurrentWriters`, `TestRestoreRejectsCorruption`, `TestRestoreInputMutation`, `TestBackupCancellation`, `TestFailureBoundaries`, `TestDiskFull`, `TestProgressDoesNotBlock`. `go test ./internal/backup -count=1`, exact gate and G: twelve run/pass. Assert raw refs/HEAD, deletion between snapshots, valid extra objects and source-inaccessible restore, not just Git exit 0. Required progress test exercises a real nonblocking observer, not an empty placeholder.

**Failure/cancel/restart:** no-replace visibility commit fixes artifact identity even if later sync fails. Preserve uncertain publication; no blind fresh-ID retry. Reap before cleanup/unlock. **Stop:** unsafe publication primitive, unproved independence/coverage, unapproved policy change. **Platform evidence:** Linux/amd64 host gates, full backup package, all global gates and race suite passed; native race/crash/process tests on other claimed OS/filesystem combinations remain pending. Directory sync uncertainty is reported separately from visibility. See retained evidence above.

## I04 — SQLite durable jobs and reconciliation

**Purpose:** durable admission and truth independent of UI delivery. **Dependencies:** I03. **Status:** DONE for Linux-host implementation scope after independent review. Darwin/Windows runtime and broader filesystem/crash qualification remain pending; I05 desktop backend integration is not complete. Retained evidence is under `/tmp/opencode/gitvault-i04-evidence/`.

**Approved scope amendment (2026-10-23):** the user approved adding a caller-reserved snapshot ID so an admitted task can reconcile the exact output after a crash. This adds `internal/backup/engine.go` and `internal/backup/engine_test.go` to the allowed paths; the engine still generates an ID when none is supplied. Acceptance requires the ID to be validated before effects, persisted with the task before work, and reused in the final manifest/path, with a behavioral test. No artifact-format change.

**Allowed:** `internal/{store,app}/**`, approved amendment `internal/backup/{engine.go,engine_test.go}`, `go.{mod,sum}`, `docs/{DATA-MODEL,RECOVERY-SEMANTICS,DEPENDENCIES}.md`, common status paths.

**Steps:**
1. Review/pin modernc SQLite candidate and transitive/license/platform costs. Embed transactional migrations; refuse future version/changed migration checksum. Test connection foreign keys/busy timeout/actual journal-sync policy, trusted DSN and local app-data DB, one initial open/idle connection.
2. Under ownership commit `preparing` intent before async work, reject competing run with typed busy, validate transitions and bound repositories. Bind root marker to installation/store/canonical application-data identity; foreign or lost authority fails managed work closed. All managed pin/lease/retention records use that one authority. No SQL transaction spans Git/network.
3. Persist task/artifact/terminal facts independently of event channel; lease inputs and document fixed lock order.
4. Reconcile bounded interrupted jobs, committed artifact without row, absent/corrupt artifact, conflicting IDs, unowned staging and deletion intents. Unknown final directories offer inspection, not auto-delete authority.

**Config:** constructor options only; real typed settings arrive I05. **Exclude:** UI, GitHub/cloud, ORM, auto deletion.

**Tests/gates:** `sqlite_integrity`: `TestMigrationsAtomic`, `TestFutureSchemaRejected`, `TestSQLiteConnectionPolicy`; `durable_admission`: `TestAdmissionBeforeWork`, `TestCancelRetainsOwnership`, `TestRestartReconciliation`, `TestTerminalPersistenceWithoutUI`; `reserved_snapshot_identity`: `TestReservedSnapshotID`. Commands `go test ./internal/store -count=1`, `go test ./internal/app -count=1`, `go test ./internal/backup -run '^TestReservedSnapshotID$' -count=1`, exact gates and G; eight named passes. Required `TestAdmissionBeforeWork` subcases use two independent installation/database identities against one root and prove foreign/canonical-app-data rejection under the root lock and no access to the other database's pin/lease records. `TestRestartReconciliation` recovers the exact admitted snapshot ID, rejects lost/ambiguous bindings without automatic takeover, preserves unknown outputs for inspection, and never deletes unowned staging. It also reports `deleting`/`deleted` rows as unresolved and leaves startup admission closed; I04 does not create deletion authorization or execute effects. Supporting `TestReconciliationDirectoryScanIsBounded` proves directory inventory has a total entry budget. Read-only portability is exercised in I09.

**Failure/cancel/restart:** reap before ownership release; reconcile original identity before retry; unusable DB disables managed jobs/deletion but not standalone restore. **Stop:** unreviewed driver, destructive migrations, deleting/published ambiguity. **Platform evidence:** native temporary SQLite crash/reopen, verified pragma results and process ownership per claimed OS.

## I05 — Typed revisions and responsive native local workflow

**Purpose:** usable local native creation/cancel with immutable intent. **Dependencies:** I04. **Status:** DONE for Linux/amd64 implementation scope after automated gates, native smoke, and independent GPT-6 Luna review. Other platforms and broader filesystem/crash behavior remain unqualified. Start inventory and observable failure cases: `/tmp/opencode/gitvault-i05-evidence/I05-start.txt` and `input-sha256.txt`.

**Allowed:** `internal/{config,app,store,desktop}/**`, `main.go`, `docs/{CONFIGURATION,MODULES}.md`, common status paths.

**Steps:**
1. Add typed schema/version/defaults/migration/normalization for actual root, concurrency, full-kind and resource/time consumers; reject unknown fields/version, duplicate keys, overflow and safety bypasses.
2. Make `config_revisions` authoritative and `settings_head` the sole current pointer. Validate edits, write revision plus revision-keyed selection/target/schedule projections, and compare-and-swap head in one transaction. Stale edits conflict; admission captures that head atomically. Operational records cannot independently override settings. Map module-owned options at composition. Root change is explicit idle validation/rebind, never move/delete.
3. Wire native local source/root, Start/busy/Cancel/status to app services; no blocking view work, no fake success.
4. Use one bounded event bridge: immutable batch/main-thread mutation, durable terminal refresh; no goroutine per event and no synchronous `RunOnMain` wait cycle.

**Config:** owns shared typed core now; later fields require real consumers. No secret values. **Exclude:** unused cloud/scheduler framework, GitHub acquisition, web UI.

**Tests/gates:** `config_revisions`: `TestConfigBoundsAndMigration`, `TestConfigImmutableRevision`; `native_local`: `TestLocalWorkflowCommands`, `TestBoundedProgressBridge`, `TestMainThreadDispatchNoWaitCycle`. Commands `go test ./internal/config -count=1`, `go test ./internal/desktop -count=1`, exact gates and G; five named passes. `TestConfigImmutableRevision` must cover concurrent head-CAS conflict, rollback between projection/head writes, atomic admission during edit, and no mixed selection/target/schedule revisions. Slow engine never blocks view; active intent retains its original revision.

**Failure/cancel/restart:** invalid config has no effects, UI closure/saturation never blocks terminal persistence, restart queries durable state without second start. **Stop:** worker/main cycle, unpinned MyGo API assumption, mutable run settings or view I/O. **Platform evidence:** headless controls plus native Start/Cancel responsiveness on each claimed OS; record measured latency and setup honestly.

**Current evidence:** `/tmp/opencode/gitvault-i05-evidence/I05-review-status.md` records passing Linux/amd64 gates and an independent GPT-6 Luna review with no actionable findings; the user selected GPT-6 Luna in place of DeepSeek. The native smoke used an isolated synthetic repository and vault; Start action returned in 0.111 ms, Cancel appeared after 215.026 ms, Cancel action returned in 0.116 ms, and the durable run ended cancelled with no snapshot. ATK warnings during test-only accessibility-bridge shutdown are recorded. This acceptance is not release, data-protection, or broader platform qualification.

## I06 — MyGo-native secrets and PAT broker onboarding

**Purpose:** secure native onboarding and job-scoped acquisition authentication. **Dependencies:** I05, I02. **Status:** DONE for Linux/amd64 implementation scope after thirteen named tests across five gates, global checks, synthetic native Set/Get/Delete qualification, and independent GPT-6 Luna follow-up review. Other platforms and native locked/pending-prompt states remain unqualified; GitHub discovery/acquisition is I07. Start inventory and pre-implementation failure checklist: `/tmp/opencode/gitvault-i06-evidence/I06-start.json` and `I06-checklist.md`; results and review evidence: `/tmp/opencode/gitvault-i06-evidence/I06-results.md` and `I06-review-round-2.md`.

**Allowed:** `internal/secrets/**`, `internal/{app,config,store,desktop}/**`, `internal/backup/{auth*,git*}.go`, `main.go`, `main_test.go`, `docs/{SECURITY,CONFIGURATION,DEPENDENCIES}.md`, common status paths.

**Steps:**
1. Adapt pinned MyGo native Secrets Service Set/Get/Delete (2,560-byte cap), missing/denied/unavailable results, dev/prod namespace and bounded outstanding blocking calls; no second keychain package by default.
2. Wire explicit PAT save/replace/remove, secret reference/generation, least privilege/organization policy disclosures and actionable errors.
3. Implement restricted job-local credential broker/helper peer/host/lifetime protocol. No token argv/environment/URL, persistent Git config or arbitrary helper surface; pass only bounded secret channel data.
4. Reject stale prompt completions, revoke broker access on termination and preserve stricter offline runner policy. Scan sentinel surfaces, including process captures and error paths.

**Config:** references/generations only, actual secret only native store/job memory. **Exclude:** real accounts, discovery, plaintext fallback, pretending context kills OS prompts.

**Tests/gates:** `native_secret_adapter`: `TestSecretAdapterOutcomes`, `TestSecretPromptBoundedLifetime`; `credential_onboarding`: `TestGitHubCredentialOnboarding`, `TestGitHubCredentialOnboardingFailures`, `TestCredentialChangesRejectedWhileWorkflowIsBusy`, `TestCredentialBrokerRejectsLatePromptResult`, `TestCredentialBrokerUsesCapturedCredentialRevision`; `credential_broker`: `TestCredentialBrokerScope`, `TestCredentialSentinelIsolation`, `TestBrokerCancellationAndOfflineIsolation`; `native_credential_controls`: `TestNativeCredentialOnboardingControls`, `TestSecretInputClientOnlyExposesMask`; `helper_dispatch`: `TestCredentialHelperDispatchIsIsolated`. Run the exact anchored commands in `tasks.json` with `-json -count=1`, plus G; all thirteen named tests must run/pass without skips. Synthetic values only; no secret-bearing diagnostics. The UI draft owns bytes and native text queries receive only a mask; transient strings passed by MyGo's native text callback remain outside the app's control and are not retained.

**Failure/cancel/restart:** one outstanding native call maximum; cancellation does not spawn replacement prompts and any late prompt result is discarded. A native write with uncertain completion is not published to settings; a database commit with uncertain outcome does not trigger deletion of a possibly referenced generation. Removal first commits the settings reference clear, then deletes only the captured native generation; if native deletion is unconfirmed, report that the token may remain but is no longer used by GitVault. Broker access is revoked with job context and its caller must reap the Git child before releasing job/root ownership. Restart resolves only the persisted reference/generation, never a token. **Stop:** unsafe broker transport or sentinel leak, API mismatch, unauthorized credential request. **Platform evidence:** separately record native store/prompt locked/unavailable behavior per claimed OS; Linux Secret Service/session bus qualification. Fake pass is not native qualification.

## I07 — GitHub REST pagination and explicit stable selection

**Purpose:** complete discovery without implicit permission to back up everything visible. **Dependencies:** I06, I05. **Status:** DONE for the documented Linux/amd64 implementation scope after thirteen named tests across four gates, global format/test/vet/build/race checks, and independent GPT-6 Luna review. No live GitHub request or real credential was used. Non-Linux behavior, native secure-store prompt and broader native UI qualification remain pending; GitHub acquisition is I08. Baseline/checklist: `/tmp/opencode/gitvault-i07-evidence/I07-start.json` and `I07-checklist.md`; final gates/review: `/tmp/opencode/gitvault-i07-evidence/I07-results.md`.

**Allowed:** `internal/{github,app,store,config,desktop}/**`, `main.go`, `docs/{SECURITY,CONFIGURATION}.md`, common status paths.

**Steps:**
1. Use bounded standard HTTP/typed decode for PAT identity and all REST pages; approved HTTPS same-origin links/redirects only, cycle/duplicate handling and explicit completeness.
2. Classify 401/403/404/429, organization constraints and transient failure; enforce cancellation-aware total retries/rate-reset wait budget.
3. Persist account/provider/repository IDs; rename preserves identity, deleted/inaccessible is visible and partial inventory never erases saved selections.
4. Wire native display filters and explicit selection by stable IDs. Newly discovered unselected; empty selection starts no work; capture selection revision.

**Config:** filters, selected IDs, bounded wait; no bearer settings or filter-as-selection. **Exclude:** Git transfer, GraphQL, issues/PR export, live credentials.

**Tests/gates:** `github_discovery`: `TestGitHubPagination`, `TestGitHubRateAndAuthErrors`, `TestGitHubDiscoveryCancellation`, `TestGitHubOriginAndResponseBounds`, `TestGitHubDisplayFilters`, `TestGitHubDiscoveryBytes`; `explicit_selection`: `TestStableExplicitSelection`, `TestPartialDiscoveryPreservesSelection`, `TestCancelledDiscoveryPromptRemainsVisibleUntilRefresh`, `TestDiscoveryCancellationStopsWorkerAndShutdownWaits`; `inventory_migration`: `TestVersionOneMigrationPreservesConfiguration`, `TestGitHubInventorySurvivesReopen`; `native_selection_controls`: `TestNativeGitHubDiscoveryFiltersAndExplicitSelection`. Run the exact anchored commands in `tasks.json`, `go test ./internal/github -count=1`, `go test ./internal/app -count=1`, `go test ./internal/store -count=1`, `go test ./internal/desktop -count=1`, and G. Thirteen named tests must run/pass; cover malformed/cyclic and query-altered links, duplicate JSON keys, bounded rate/auth/timeout, partial/complete reconciliation, durable inventory, rename/delete, stable explicit IDs, new/empty selection, filter independence, cancellation/shutdown and pending secure-store status, migration preservation, and native controls.

**Failure/cancel/restart:** cancel paging/wait promptly; distinguish partial from complete; restart restores selected identities only. **Stop:** cross-origin bearer leakage, partial-as-complete, auto-selection. **Platform evidence:** mock HTTP is default; native selection presentation separately per supported OS.

## I08 — Selected GitHub-to-local snapshot pipeline

**Purpose:** real selected remote acquisition through verified local recovery. **Dependencies:** I07, I03, I04, I05, I06. **Status:** TODO.

**Allowed:** `internal/{app,github,store,desktop}/**`, `internal/backup/{acquire*,git*}.go`, `main.go`, `docs/{BACKUP-FORMAT,SECURITY}.md`, common status paths.

**Steps:**
1. Admit captured account/selected IDs/config/credential-generation before refresh; empty/busy produces no transfer.
2. Validate canonical approved GitHub HTTPS acquisition identity; real Git plus job broker updates stable-ID disposable mirror under ownership; handle rename/deletion/access loss explicitly.
3. Invoke I03 full create/verify/publication, not duplicate manifest rules. Bound repository tasks and isolate independent task failures.
4. Persist per-repository artifact/results and derived partial summary; wire native selected Start/Cancel/eligible-stage Retry to actual coordinator work.

**Config:** consume immutable revisions; rotation applies next intent, not silent identity switch. **Exclude:** cloud, all-account auto-backup, LFS/submodule payloads, metadata export, unauthorized live private repo.

**Tests/gates:** `selected_pipeline`: `TestSelectedGitHubSnapshotPipeline`, `TestPipelinePartialResults`, `TestPipelineCancellationAndRestart`, `TestPipelineSelectionAndCredentialRevision`; `native_selected_start`: `TestSelectedBackupControls`. Commands `go test ./internal/app -count=1`, `go test ./internal/desktop -count=1`, exact gates and G; five named passes. Use real installed Git against synthetic HTTPS smart-Git plus fake GitHub metadata; remove source and prove offline exact-ref/HEAD restore. A fake engine-only pass is insufficient. Test-origin injection must not relax production host policy.

**Failure/cancel/restart:** unrelated successes survive auth/network failure; child reaping precedes release; uncertain publication reconciles same ID, retry only eligible stage. **Stop:** IDs/URL mismatch, mock-only transfer evidence, lost publication identity or still-unwired native control. **Platform evidence:** real Git fixture and native smoke by claimed OS; optional live access only after human opt-in.

## I09 — History, import and offline restore UI

**Purpose:** account-independent recovery on clean installation. **Dependencies:** I03, I04, I05. **Status:** TODO.

**Allowed:** `internal/{app,store,desktop}/**`, `main.go`, `docs/{BACKUP-FORMAT,RECOVERY-SEMANTICS}.md`, common status paths.

**Steps:**
1. Query bounded history with separate readiness, operation/cloud outcomes, verification dimensions and exclusions; mark missing/corrupt rows honestly.
2. Inspect/import external artifacts through common bounded private staging; conflicting identity/bytes fails. Unknown/external paths receive no implicit deletion authority.
3. Wire explicit bare restore destination, lease, no-replace, progress/cancel and terminal facts through I03.
4. Allow local import/restore with account/metadata store absent or unavailable; explain absent durable history rather than blocking recovery. `TestRestoreWithoutMetadataStore` must also import read-only from a foreign/lost managed-root binding under its input-copy locking policy, restore a private copy, and prove original marker/artifacts/pins/leases are neither changed nor adopted.

**Config:** captured restore limits/timeouts; explicit destination, no overwrite preference. **Exclude:** cloud retrieval, checkout, account requirement, trust-by-database-row.

**Tests/gates:** `portable_restore_workflow`: `TestCleanInstallOfflineImportRestore`, `TestImportIdentityConflict`, `TestRestoreWithoutMetadataStore`, `TestRestoreLeaseAndCancel`; `native_history_restore`: `TestHistoryVerificationLabels`, `TestOfflineRestoreControls`. Commands `go test ./internal/app -count=1`, `go test ./internal/desktop -count=1`, exact gates and G; six passes, exact restored refs/HEAD and existing target preservation with no original source/account/DB.

**Failure/cancel/restart:** copy/hash/consume same private bytes; post-commit cancel retains restored identity; reconcile import where DB exists, no false ready claims. **Stop:** network/secret lookup in offline path, common verifier bypass, imported path deletion permission. **Platform evidence:** clean-profile native offline restore on each qualified OS/filesystem, separate headless evidence.

## I10 — Optional S3/R2 copy of ready artifacts

**Purpose:** independent optional remote copy, never a prerequisite for local readiness. **Dependencies:** I08, I06, I09. **Status:** TODO.

**Allowed:** `internal/{cloud,app,store,config,desktop}/**`, `internal/secrets/**`, `main.go`, `go.{mod,sum}`, `docs/{CONFIGURATION,DEPENDENCIES,SECURITY,RECOVERY-SEMANTICS}.md`, common status paths.

**Steps:**
1. Review/pin compatible S3 SDK; add explicit validated target revision and secret references, default disabled. Disclose unencrypted Git payloads and plaintext-cloud limits before enablement.
2. Stream existing fixed ready artifact to unique keys; qualify conditional PUT/multipart completion or reservation, manifest last. No blind overwrite retries.
3. Persist upload IDs/parts/ambiguous result/cleanup state; finite global upload+part concurrency and one retry owner across SDK/app, bounded total transfer.
4. Abort via fresh bounded context after cancellation; expose pending cleanup. Retry copy without re-bundling; optional GET/count/SHA-256 read-back distinct from receipt and restoration. ETag is not SHA-256 proof.

**Config:** target/provider/endpoint/bucket/prefix/references, part sizing/concurrency/read-back/time budgets with actual consumers. Import cannot enable cloud. **Exclude:** bundle creation in uploader, encryption, retention deletion, unapproved real cloud access.

**Tests/gates:** `cloud_copy`: `TestCloudManifestLast`, `TestCloudNoReplaceAndLostResponse`, `TestMultipartCancelFreshAbort`, `TestCloudRetryAndConcurrencyBounds`, `TestCloudReadBackNotETag`; `local_survives_cloud`: `TestCloudFailurePreservesLocal`, `TestRetryUploadDoesNotRebundle`. Commands `go test ./internal/cloud -count=1`, `go test ./internal/app -count=1`, exact gates and G; seven passes, including same-length corruption and multipart composite checksum ambiguity.

**Failure/cancel/restart:** local output survives cloud errors; missing manifest incomplete; reconcile exact operation/key/size/digest on lost response, retain failed abort. **Stop:** no provider no-overwrite evidence, retry multiplication, secret SDK logging, receipt mislabeled verified bytes. **Platform evidence:** fake transport default; separately authorized isolated-prefix S3 **and** R2 multipart/abort/read-back smoke before claiming both work.

## I11 — Remote retrieval through the same restore path

**Purpose:** remote recovery without a second Git interpretation. **Dependencies:** I10, I09. **Status:** TODO.

**Allowed:** `internal/{cloud,app,store,config,desktop}/**`, `main.go`, `docs/{CONFIGURATION,RECOVERY-SEMANTICS}.md`, common status paths.

**Steps:**
1. Bound published-manifest listing to selected target; remote metadata untrusted, missing manifest/incomplete copies never ready.
2. Capture exact key/version/target identity; privately stage bounded GET, hash/count trusted expected artifact bytes, reject unsafe metadata and changing object identity.
3. Feed common I03/I09 import/verify/bare restore, never direct unverified remote import or duplicate Git rules.
4. Wire explicit remote choose/download/restore with accurate labels and explicit managed policy for retaining verified downloaded artifact for offline reuse.

**Config:** reuse target/transfer/restore revision and finite listing limit if needed. **Exclude:** ETag trust, overwrite, incremental chain.

**Tests/gates:** `remote_inputs`: `TestRemoteListingRequiresManifest`, `TestDownloadBoundsAndIntegrity`, `TestDownloadVersionMutation`; `shared_remote_restore`: `TestRemoteUsesOfflineRestoreContract`, `TestRemoteRestoreCancellationAndRestart`. Commands `go test ./internal/cloud -count=1`, `go test ./internal/app -count=1`, exact gates and G; five passes, corruption/truncation/version mutation fails, no-replace restored output and second restore with network removed.

**Failure/cancel/restart:** no combining changed object bytes on resume; clean only owned uncommitted staging, retain published restoration and reconcile original download intent. **Stop:** bypass common verifier, unknown object identity, incomplete listed ready. **Platform evidence:** deterministic transport failures plus opt-in S3/R2 clean-profile retrieval/restore on qualified platforms.

## I12 — Retention preview with exact revision-bound scope

**Purpose:** explain candidates without deleting. **Dependencies:** I09, I10, I11. **Status:** TODO.

**Allowed:** `internal/{retention,app,store,config,desktop}/**`, `main.go`, `docs/{CONFIGURATION,RECOVERY-SEMANTICS}.md`, common status paths.

**Steps:**
1. Define bounded inventory/current-time/policy revision with actual local validity, pins, leases and copy identities.
2. Compute union of retained daily/weekly/monthly buckets; pins/leases always protect and preserve at least one valid ready **local** snapshot per repository. A cloud receipt cannot substitute.
3. Produce exact IDs/local/copy/key/version scope, inventory/policy revisions and reasons; reclaim estimates never secure-erasure or guaranteed-byte claims.
4. Wire preview-only policy UI. Inventory/policy change invalidates proposal; enabling policy grants no effect permission.

**Config:** `retention.mode` defaults to `preview_only`; the only other supported mode is `explicit_delete`, which permits requesting a fresh approved preview execution, not automatic deletion. Test both modes without effects in `TestRetentionPreviewHasNoEffects`. Bucket counts/UTC interpretation follow CONFIGURATION.md. No `retention.enabled` boolean exists. **Exclude:** all deletions, automatic retention, mirror cleanup.

**Tests/gates:** `retention_preview`: `TestRetentionBucketUnion`, `TestRetentionProtectsPinsLeasesLastLocal`, `TestRetentionPreviewExactRevisions`, `TestRetentionPreviewHasNoEffects`. `go test ./internal/retention -count=1`, exact gate and G; four passes with mixed corrupt/remote-only/pinned/leased inventories and deterministic boundaries.

**Failure/cancel/restart:** stale/unknown inventory cannot become executable scope; cancellation has no deletion effect, saved proposal is not fresh approval after restart. **Stop:** unproved last-valid-local, unknown external deletion path, broadened scope. **Platform evidence:** pure inventory/clock tests plus native exact-scope presentation, no real delete necessary.

## I13 — Approved retention execution and partial-delete recovery

**Purpose:** explicit scoped effects with durable partial-outcome recovery. **Dependencies:** I12, I04, I10. **Status:** TODO.

**Allowed:** `internal/{retention,app,store,cloud,desktop}/**`, `main.go`, `docs/{SECURITY,RECOVERY-SEMANTICS}.md`, common status paths.

**Steps:**
1. Record explicit approval bound to exact preview IDs/policy/inventory/target revisions; revalidate under ownership and protection immediately before effects. Revision changes invalidate approval, not broaden it.
2. Persist intent before first deletion; remove remote manifest marker before payload and record each local/remote confirmed/missing/pending effect. Only demonstrably owned paths/approved keys/versions.
3. Recheck pins/leases/last-valid-local; serialize effects with readers/writers. Stop on new protection; do not auto-delete substitute candidates.
4. Recover lost response/crash by reconciling exact effects and retry only remaining authorized scope; visible cleanup-required state. Show delete-marker/versioning/Object Lock/legal-hold and no secure-erasure/reclaimed-byte promises.

**Config:** no deletion-authorizing setting; approval separate durable input. **Exclude:** unattended retention, arbitrary paths, historical version purge, Object Lock override, unapproved real deletion.

**Tests/gates:** `authorized_retention`: `TestDeleteRequiresExactApproval`, `TestDeleteRevalidatesProtection`, `TestDeletePartialFailureRecovery`, `TestDeleteCancellationAndRestart`, `TestVersionedDeleteDoesNotClaimErasure`. `go test ./internal/retention -count=1`, exact gate and G; five passes, every intent/effect boundary injected, no new ID during retry.

**Failure/cancel/restart:** cancel halts new effects, cannot undo deletes. Persisted deleting state must not be reimported as ready. Revalidation never repeats already confirmed effects blindly. **Stop:** stale approval, changed protection, uncertain effect without safe reconciliation, missing real-data authorization. **Platform evidence:** temporary local/fake versioned-cloud fixtures default; real cloud delete separately opt-in with exact isolated scope.

## I14 — Scheduler through shared admission

**Purpose:** deterministic in-app due times, no duplicate admission/backlog. **Dependencies:** I08, I05. **Status:** TODO.

**Allowed:** `internal/{scheduler,app,store,config,desktop}/**`, `main.go`, `docs/{CONFIGURATION,RECOVERY-SEMANTICS}.md`, common status paths.

**Steps:**
1. Keep schedule disabled by default. Show and confirm explicit IANA zone/local time/frequency and proposed gap=first valid, fold=earlier once, invalid monthly day=last valid day semantics before enablement.
2. Define occurrence ID from schedule ID/revision/intended local time/resolved UTC; claim occurrence and coordinator admission in one transaction.
3. Use Go timers plus periodic wall-clock reconciliation/wake hints. Busy manual run means missed occurrence, never concurrent engine invocation.
4. Coalesce missed work to one `offer_once`; only explicit acceptance admits one catch-up. Revision edits cancel stale future timers and preserve active immutable intent.
5. Unattended work must never call a potentially prompting native secret backend. `TestScheduleClaimAdmissionAtomic` includes a backend that fails the test if called from unattended admission; without a qualified noninteractive path or explicitly authorized valid bounded memory lease, require an interaction-required result and manual retry. Include expired-lease and cancelled-prompt-result cases; do not infer prompt-free access from a previous successful read.

**Config:** enabled false, validated time/zone/day/policies; no silent enable via import. **Exclude:** OS service, stopped-app operation, automatic catch-up, retention authority, frame-timer jobs.

**Tests/gates:** `schedule_time`: `TestScheduleGapFoldAndMonthEnd`, `TestScheduleClockJumpAndRevision`, `TestCatchUpOfferOnceRequiresAcceptance`; `schedule_admission`: `TestScheduleClaimAdmissionAtomic`, `TestManualTimerAdmissionRace`. Commands `go test ./internal/scheduler -count=1`, `go test ./internal/app -count=1`, exact gates and G; five passes including crash both sides of admission and explicit busy outcome.

**Failure/cancel/restart:** no orphan claims or duplicates; restart/clock jump yields at most one offer, never auto-run; cancellation follows shared coordinator. **Stop:** nontransactional claim, hidden policy/auto-run, direct timer engine call. **Platform evidence:** deterministic IANA fake-clock tests plus actual sleep/wake/zone behavior per claimed OS; app-stopped limitation disclosed.

## I15 — Tray, window and quit lifecycle

**Purpose:** reachable background operation and safe native shutdown. **Dependencies:** I14, I06, I09. **Status:** TODO.

**Allowed:** `internal/{platform,app,desktop,config}/**`, `main.go`, `docs/{CONFIGURATION,OPERATIONS,MODULES}.md`, common status paths.

**Steps:**
1. Create tray after pinned MyGo readiness; detect actual menu availability/Linux AppIndicator and do not rely on portable icon-click callbacks.
2. Default close-to-tray false; enable only with operational reachable menu. Explicit last-window policy and Show/Status/Start use shared coordinator.
3. Build asynchronous quit states: stop admission, request cancel, let UI loop dispatch while workers reap/persist; guarded final quit bypasses interception exactly once. Never main-thread wait on worker calling synchronous `RunOnMain`.
4. Expose pending native-secret OS interaction honestly, bound calls, prevent abandoned writers/locks or endless quit guard.

**Config:** live close-to-tray gated by capability; startup identity separate. **Exclude:** hidden service, force-abandoning child process, pretending native prompt killed.

**Tests/gates:** `lifecycle_state`: `TestQuitAwaitsReapWithoutMainWait`, `TestQuitWithPendingSecretPrompt`, `TestFinalQuitGuard`; `tray_window`: `TestTrayUnavailableKeepsWindow`, `TestTrayCommandsUseAdmission`, `TestWindowClosePolicy`. Commands `go test ./internal/app -count=1`, `go test ./internal/desktop -count=1`, exact gates and G; six passes with bounded synchronization, no fabricated native proof.

**Failure/cancel/restart:** unavailable tray retains usable window; view destruction cannot block terminal facts; external kill reconciles at restart, normal quit reaps before release. **Stop:** inaccessible app after hide, wait-cycle/abandoned process, incorrect pending-prompt claim. **Platform evidence:** native close/reopen/menu/cancel/quit/locked-secret smoke Linux/macOS/Windows; Linux desktop variation scoped explicitly, compile/headless tests insufficient.

## I16 — Qualified packaging and release

**Purpose:** independently reviewed installable candidate with evidence-limited claims. **Dependencies:** I11, I13, I15. **Status:** TODO.

**Allowed:** `mygo.json`, `resources/icon.png`, `.github/workflows/**`, `scripts/release/**`, `internal/releasecheck/**`, `internal/platform/**`, `main.go`, `LICENSE`, `SECURITY.md`, `docs/{OPERATIONS,TESTING,DEPENDENCIES,SECURITY}.md`, `docs/implementation/evidence/**`, common status paths. The icon must have human-approved rights. Module identity change needs human-approved scope amendment for `go.mod` and import updates; do not infer that authority here.

**Steps:**
1. Obtain human choices for license, application/module identity, icon rights, security contact, distribution targets and signing/key handling. Missing decision blocks relevant output; no invented identity or license.
2. Deliver substantive candidate-evidence/install fixture checks, build/package scripts, dependency/license inventory and dev/prod data/secret separation. Preserve user snapshots/settings on install/update/uninstall.
3. Qualify installed selected-backup-to-offline-restore, native accessibility/keyboard/tray/quit, large repos/disk budgets/disk-full/crash/cancel, provider claims and clear LFS/submodule/metadata exclusions.
4. Inventory exact package/checksums/build inputs/platform logs and known limitations. Independent requested Sol6.1 read-only review; maximum two correction rounds. Separate independent release approval and explicit human signing/publish authorization.

**Config:** approved startup/package namespace, no silent migration. **Exclude:** auto publishing, self-approval, signed update service, invented hosted CI execution.

**Tests/gates:** `release_candidate_contract`: `TestReleaseEvidenceCompleteness`, `TestPackageDataNamespaceContract`, `TestReleaseClaimsRequireQualification`. `go test ./internal/releasecheck -count=1`, exact gate and G; three substantive passes against actual candidate metadata/install fixtures, not tests asserting hand-written approval booleans. Additionally `go tool mygo doctor` exits 0 with native requirements recorded; `go tool mygo build` exits 0 with inventoried native candidate files; `go test -race ./...` exits 0 where supported. These are required future commands, **not verified now**. Native install/accessibility/signature/provider evidence cannot be replaced by these tests.

**Failure/cancel/restart:** interrupted build never promotes release; failed/missing qualification blocks claim, not silently marked pass. Install/uninstall/recovery preserves user artifacts. **Stop:** missing human decision/authorization, unavailable independent reviewer or unresolved findings after two rounds, missing runtime evidence for advertised OS/provider. **Platform evidence:** installed package per claimed OS/architecture, exact Git/Go/MyGo/dependency versions, real authorized S3/R2 isolation where advertised, separate signature verification and approval. Candidate build is not permission to distribute.
