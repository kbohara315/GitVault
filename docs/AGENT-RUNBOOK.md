# AI execution runbook

## Authority and reading order

**Current mode: implementation.** The user explicitly requested the full application and substantive tests. Scoped code, dependency qualification, builds, and synthetic automated tests are authorized. Real secret access, destructive real-data actions, signing, and publishing still require their separate permissions. Task status and retained evidence—not this paragraph—establish actual completion.

Read `AGENTS.md`, `CONTEXT.md`, [README](README.md), [ARCHITECTURE](ARCHITECTURE.md), [IMPLEMENTATION](IMPLEMENTATION.md), [MODULES](MODULES.md), [RECOVERY-SEMANTICS](RECOVERY-SEMANTICS.md), [CONFIGURATION](CONFIGURATION.md), [DEPENDENCIES](DEPENDENCIES.md), [BACKUP-FORMAT](BACKUP-FORMAT.md), [DATA-MODEL](DATA-MODEL.md), [SECURITY](SECURITY.md), [TESTING](TESTING.md), [OPERATIONS](OPERATIONS.md), then the chosen [task](implementation/TASKS.md) and [JSON record](implementation/tasks.json). Resolve contradictions before work. Preserve domain vocabulary: repository, disposable mirror, immutable snapshot, run, restoration, cloud copy, retention preview, pin, lease, scheduled occurrence.

Historical `plans/001–003` and `plans/baseline.json` provide design provenance only. They are superseded, not completed work or a permanent checksum lock. Never rerun their fixed initial baseline as a veto against authorized predecessor/documentation updates.

## Start protocol

1. Confirm mode, user-authorized objective and workspace. Inspect actual files; do not assume Git exists in this workspace. Never initialize a repository or commit merely to manufacture an audit trail.
2. Validate the tracker: exactly I01–I16, unique IDs, TODO initial states, existing dependency IDs, acyclic edges, matching names/dependencies/allowed scope/test gates in Markdown. Inspect evidence before trusting a later DONE status.
3. Select one task from the dependency frontier: all dependencies implemented, required gates passed and independently reviewed for the behaviors this task consumes. A pending native qualification blocks claims relying on it; do not silently treat host completion as universal DONE. Use BLOCKED with explicit completed-host/pending-target evidence when a required target is unavailable.
4. Reserve the exact owned path set, including shared status documents. Default to one active writer. Logical DAG independence is not permission to edit overlapping `internal/app`, UI, config or docs in parallel. Only user/applicable-instruction-authorized delegation may create agents; independent read-only review is explicitly requested here. Parallel read-only/tool checks are fine.
5. Capture a fresh input inventory with SHA-256, sizes, paths and explicit absent planned outputs, covering repository instructions, task/tracker, normative docs, dependency manifests and consumed predecessor source/tests/evidence. Record predecessor task/evidence IDs and output hashes. If no Git, retain a scoped before-content/diff inventory in the authorized evidence store, outside secret/artifact payloads. A Git status/diff alone does not capture pre-existing uncommitted work.
6. Compare those inputs to predecessor completion records and current authorized changes. Explain legitimate doc/predecessor evolution; investigate unexplained drift. Do not overwrite or reset someone else's work. Scope amendment requires reason, affected paths/acceptance/dependencies and approval before edits.
7. Record IN_PROGRESS only after implementation authorization and reservation. In planning mode leave statuses TODO. Define required observable failure cases before implementation. Keep a task-local checklist rather than inventing new numbered slices.

Evidence location during implementation may be an external execution artifact store. To commit repository evidence under `docs/implementation/evidence/`, explicitly reserve that path via a scoped plan amendment (I16 owns it already). Never place generated build outputs or secret values in documentation. New tracker evidence must reference actual retained artifacts, not placeholders masquerading as proof.

## Execution rules

Keep Go 1.27.1, MyGo v0.6.2 and installed Git unless a reviewed task explicitly changes pins. `modernc.org/sqlite` and the S3 SDK remain candidates until their task's dependency/portability/license/security review and actual gates. Existing MyGo native secrets are the first adapter; no unnecessary second keychain package. Do not bootstrap every package in advance: deep `internal/backup` first; `internal/app` owns shared admission; introduce typed `internal/config` at real consumer integration. Compose concrete dependencies explicitly; interfaces at genuine consumer/test/platform seams.

Implement the smallest **complete** behavior behind one task boundary, with behavioral tests on synthetic temporary repositories/databases/HTTP/cloud fixtures. No Git, network or blocking disk operation in a view. Context-first blocking methods, wrapped causes, argv subprocesses, finite output/time/retries/concurrency. No shell commands or Git executable hooks in config. Configuration defaults and cross-field limits are typed, normalized and immutable per admitted intent, not read from a mutable global mid-run. Add no unused settings/infrastructure.

### Non-negotiable recovery contract

- Full bundles only, no external prerequisites. Capture exact valid saved refs/raw tag IDs and HEAD independently; SHA-1/SHA-256 capability-qualified. Valid extra packed objects allowed; exact refs/HEAD and integrity required. No silent drop of namespaces or detached-only history. LFS/submodule payloads, issues/PR export, incremental chains, client encryption and OS services are excluded from this full-version target.
- Mirrors are replaceable acquisition state; snapshots are immutable. Portable offline bare restoration does not require source, mirror, account, network or original SQLite. Privately copy bounded untrusted input, hash and consume the same bytes.
- No-replace publication is visibility commit, not proof of power-loss durability. Return artifact identity/publication/durability warning even after later failure/cancel. Never delete published output to simplify an error, overwrite destinations, or blindly retry an uncertain original operation with a new ID.
- Cancel owned subprocess tree, bound drain, reap, then handle owned uncommitted staging, persist terminal facts and unlock. Unknown staging is preserved until ownership is established. Cross-process/root/mirror identity and lock order are explicit; release one has one active managed root, no speculative multi-root capability.
- SQLite stores intent/history, not proof that bytes are valid. Reconcile committed output without rows, missing/corrupt rows, unknown/conflicting IDs and deletion intent. Unusable DB disables managed mutation/deletion, not independent local recovery.
- Progress bounded/nonblocking, immutable batches applied on main; terminal result durable/queryable even with no UI consumer. Synchronous MyGo `RunOnMain` cannot participate in a main/worker wait cycle. Native OS secret prompts may not cancel; bound outstanding calls, reject stale completions, report pending interaction and keep shutdown truthful.
- No secret values in URLs, arguments, environment for Git broker, settings, manifests, diagnostic logs or real fixtures. Native store references/generations are not secret values. Synthetic sentinels can exercise leak detection; real credentials require explicit human opt-in.
- Cloud consumes ready artifact only. Unique no-overwrite keys, manifest last, fixed input and separate local/cloud outcomes. Receipt is not read-back; ETag/composite multipart checksum is not plain SHA-256. Read-back is GET/count/hash. One retry owner and bounded aggregate concurrency; fresh bounded abort context after cancel, durable cleanup-required outcome.
- Retention preview names exact IDs/copy/version and inventory/policy revisions; never auto-deletes. Execution needs explicit approval and revalidation, protects pins/leases/at least one valid local per repository. Partial deletes are durable per-effect facts; retries cannot expand scope. Versioned cloud delete is not secure erasure or proof of reclaimed bytes.
- Scheduling disabled by default; display proposed gap-first-valid/fold-earlier-once/month-invalid-last-valid technical defaults on enablement. Persist IANA timezone and revisions; occurrence claim and shared admission atomic. Missed runs coalesce to one `offer_once` requiring explicit acceptance. No stopped-app operation or scheduled deletion authority.

## Named test gates: no empty matches

All package/test commands in TASKS/JSON are **future required deliverables**. Missing package is failure, not permission to remove its gate. Broad `go test ./...` can pass while a named regex matches nothing. Confirm exact top-level test discovery and actual execution:

1. Read that task's `test_gates` and `required_assertions` arrays. For each package, execute `go test PACKAGE -list .` with bounded command timeout and captured exit/output. Compare output lines exactly to every required name; absent names block acceptance. Retain the executed fixture/subcase proving each required assertion; passing a top-level name alone does not prove scenario coverage.
2. Run its anchored command with `-json` added (and `-count=1`, already specified). Retain raw events and process exit. For each required top-level name require `Action=run` followed by `Action=pass`, correct package, and **no** `Action=skip` for it or its required subcases. Require final package pass and exit 0. A top-level pass that hides skipped required format/OS/integration subcases is not adequate qualification. Fixtures must assert behavior, not simply return early.
3. Keep ordinary negative capability tests separate from a positive supported-operation gate: successfully rejecting unsupported Git is not proof that Git integration ran. Required integration CI provisions qualified Git and executes positive tests. No t.Skip to greenwash unavailable native/cloud evidence.
   Task `required_assertions` are mandatory scenarios of the named gates. Retain the executed fixture/subcase mapping and inspect its behavioral assertions; passing a top-level test name alone does not prove a cross-installation, transactional rollback, or no-prompt scenario was exercised.
4. Run global gates: `gofmt -l main.go internal` (include all added Go source roots; exit 0, empty stdout), `go test ./...`, `go vet ./...`, `go build ./...` (each exit 0). Format only owned edits. Run `go test -race ./...` once concurrency exists on supported native toolchains; record missing race tooling as pending, not a pass.
5. Execute task platform/failure gates. Cross-compilation is compile evidence only. Native OS process trees, no-replace filesystem behavior, secret prompts, tray/window shutdown, installation/accessibility and actual S3/R2 interoperability need separate evidence. No hosted CI run is implied by a workflow file.

A gate checker may be delivered later as scoped implementation tooling; this runbook does not claim one exists. Without it, manually inspect discovery and JSON events with retained evidence. Do not install Python or another runtime solely for gate bookkeeping; equivalent native tooling/manual comparison is sufficient. Never mark a command passed from a planned/expected-output table.

## Failure, restart and retry protocol

On any failed gate: retain diagnostics (sanitized/bounded), mark blocked stage and actual artifact state, identify hypothesis and make only scoped corrections. No more than two correction rounds for a verification/review cycle; unresolved failure stops and reports the blocker. Do not loop indefinitely, suppress errors, weaken invariants or replace a behavioral test with a mock assertion solely to go green.

After interruption:

1. Inspect current task status/reservations and evidence; establish previous worker ownership is gone before touching staging or managed state.
2. Re-read current instructions/task and build a fresh restart input inventory. Compare actual files against previous before/after hashes and authorized outputs, including newly added/deleted files. Explain every difference; do not require a stale initial baseline to match or bless all edits merely because a path is allowed.
3. Recover operation intent via normal coordinator/engine reconciliation, not hand-edited database rows or filesystem cleanup. Published identity survives cancellation/post-commit errors. Do not recreate full artifact for cloud retry; do not reimport intentionally deleting artifacts.
4. Rerun prerequisite and affected regression gates if consumed inputs changed; resume only remaining task scope after reservation. Preserve failed/partial evidence and approved deletion bounds. Unknown facts remain explicit.

## Independent review and bounded self-improvement

The user's latest model direction is **GPT Luna 6 or DeepSeek V4.1 Flash through OpenCode**; earlier Sol6.1 work retains its recorded reviewer identity. Resolve the requested reviewer model using available model discovery (provider/model ID), report actual choice and obtain a genuinely independent session. Current discovery found `opencode/deepseek-v4.1-flash-free`; Luna is listed only as `openai/gpt-6-luna`. Do not invent a model ID, silently substitute, or call a self-review independent. If unavailable, report blocked; do not manufacture approval. An independent review can be requested for the planning documents too; it cannot turn TODO tasks into implementation approvals.

Provide the reviewer complete scoped inputs/diff, task acceptance, test evidence and remaining limitations. Reviewer writes findings only: severity, precise path/behavior, violated contract and required evidence; no edits or side effects. The implementer may correct scoped findings in **at most two rounds**, rerunning affected/full gates and rehashing outputs each time. Reviewer then explicitly accepts or leaves unresolved blockers. Stop after the second unresolved round; further scope or policy decisions go to the parent/user. The reviewer cannot approve work it implemented, and the builder cannot approve its own release. Review approval is neither credential consent nor human release authorization.

Record useful local lessons as small task/runbook clarifications only after evidence, within owned/reserved paths. Self-improvement may tighten examples, fixture reliability and diagnostics, not relax no-replace/offline/secret/retention gates, change the full-version scope, authorize effects or add more autonomous correction rounds. Refactoring is not permission to edit unrelated modules. Resolve technical AFK facts from primary sources/fixtures; classify missing product/legal/security choices as HITL rather than impersonating the user.

## Human-only gates

Ask for and retain the exact decision/scope, not a generic assumed approval, for:

- License/contribution terms, product/module/application identity, icon rights, security contact and distribution/support claims.
- Access to real accounts/secrets, PAT scopes, organization policies, real cloud targets/cost/privacy and real-data fixtures. Routine synthetic tests need none.
- Deletion of real snapshots/cloud copies: exact preview IDs/revisions, paths/keys/versions and intent; no future scheduled blanket permission. Settings/schedule enablement cannot stand in for destructive approval.
- Root rebinding or data migration touching real installations; source repositories are never cleanup targets.
- Signing/key handling, distribution channel/account, publication and final release authorization. Build/signature verification and independent review are not consent to publish.

If a human gate is unavailable, complete safe scoped code/tests if otherwise authorized, then report precisely what remains blocked/unqualified. Do not claim the whole task DONE while mandatory acceptance is pending.

## Completion and evidence record

Use this template for every task/round; empty fields are pending, never implied approval:

```text
Task ID / name / mode / status:
Executor and reserved owned paths (including shared docs):
User implementation authority / separate human effect decisions:
Dependency IDs + accepted evidence references + consumed output hashes:
Start/restart input inventory reference (SHA-256, added/absent/deleted paths):
Actual scoped changes + before/after hashes + drift explanation:
Behavior implemented / native controls wired / explicitly unwired:
Config fields/defaults/consumers + captured revision semantics:
Named gates: discovery evidence, command, exit, run/pass/skip event references:
Global gates: format/test/vet/build/race commands and observed outputs:
Failure/cancel/restart injections and observed artifact/process/lock state:
Platform/filesystem/Git/Go/MyGo/dependency versions and native evidence:
Cloud provider receipt/read-back/restore evidence (or NOT QUALIFIED):
Artifact paths/identities/checksums (synthetic, sanitized, no secret payload):
Reviewer identity/model/independence, findings, round 0/1/2, disposition:
Human release/effect approvals (exact scope, or PENDING/NOT REQUESTED):
Remaining blockers/limitations + next eligible frontier:
```

Compare actual changes against the captured authorization, not just allowed globs. Keep JSON/Markdown status/dependencies/gate names aligned and update implemented-status entrypoints honestly. DONE requires implementation, wiring promised by that task, actual named/global passes, required target evidence and independent review; no automatic promotion from command exit alone. Keep TODO for untouched tasks, IN_PROGRESS for active authorized work, BLOCKED for unmet acceptance. Record host-complete/platform-pending explicitly rather than falsifying DONE.

Final report leads with outcome and separates **implemented**, **wired/unwired**, **tested**, **native/provider qualified**, **pending human gates**, and **not included**. Include exact failed/pending gate and artifact identity after partial errors. Do not label a scaffold, a successful package build, an upload receipt or a planning review as a released recoverable product.
