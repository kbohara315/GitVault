# Full-release implementation plan

**Canonical execution entry point. Status: implementation in progress. I01–I08 slices are accepted for their documented Linux/amd64 implementation scopes; I05 includes automated gates and native Start/Cancel smoke, I06 includes automated gates plus a synthetic native-store round trip and independent review, I07 includes bounded synthetic GitHub discovery/durable inventory/explicit selection, and I08 includes real installed-Git acquisition from a synthetic HTTPS server, offline exact-ref/HEAD restoration, coordinator-backed native selected controls, all named/global gates, and independent GPT-6 Luna review. I09–I16 remain to be implemented. No live GitHub request or real credential was used. Non-Linux, native locked/pending-prompt, broader native UI, and filesystem/crash qualification remain pending. No release approval or universal platform qualification is implied.**

## Destination

Build a native Go/MyGo application that discovers explicitly selected GitHub repositories, retains independently restorable full-bundle snapshots, restores offline into new bare repositories, records durable jobs, and supports optional S3/R2 copies, preview-driven retention, in-app scheduling, tray lifecycle, and qualified platform packaging.

The user delegated technical composition and defaults. This plan follows the existing product scope; it does not simulate user consent to use credentials, upload private data, delete snapshots, select a license, or publish a release.

## One route, one authority

1. Read [the domain language](../CONTEXT.md) and [architecture](ARCHITECTURE.md).
2. Read [module ownership](MODULES.md), [configuration](CONFIGURATION.md), [artifact format](BACKUP-FORMAT.md), and [outcome/recovery semantics](RECOVERY-SEMANTICS.md).
3. Read [integration evidence](DEPENDENCIES.md) and [the data model](DATA-MODEL.md).
4. Follow [the agent runbook](AGENT-RUNBOOK.md).
5. Execute exactly one ready slice from [the task specifications](implementation/TASKS.md) and [machine-readable DAG](implementation/tasks.json).

The task DAG owns execution order/status; task prose owns required behaviors/tests; configuration owns defaults; the artifact document owns portable representation; recovery semantics owns commit points. If they conflict, stop and reconcile the specific contract before code changes. Do not silently prefer whichever is easiest.

`plans/001–003` and their original baseline are **superseded for execution**, not completed. Preserve them as reasoning history; do not execute their frozen baseline checks against the intentionally updated documentation. Capture fresh scoped inputs for each new slice.

## Route

```text
I01 contract -> I02 controlled Git -> I03 offline recovery
                                      |
                                      v
                              I04 durable runs
                                      |
                              I05 native local flow
                                      |
                          I06 secrets -> I07 discovery
                                      |
                              I08 GitHub backup
                                      |
                              I09 history/restore
                                      |
                          I10 cloud copy -> I11 retrieval
                                      |
                       I12 preview -> I13 authorized deletion

               I14 scheduler -> I15 tray/lifecycle
                                      |
                              I16 qualification
```

This is a readable route, not a substitute for the precise dependency edges in `tasks.json`. Default execution is serial. Parallelism requires disjoint owned files and proven independent predecessors; no parallel writes to the same module, dependency files, or status record.

## Design constraints

- Preserve Go 1.27.1 and pinned native MyGo v0.6.2.
- Start with one deep recovery module, then one durable run coordinator. Introduce adapters for actual variation; do not build a generic workflow/plugin framework.
- Full bundles have no earlier-snapshot prerequisites. Keep mirrors disposable. Validate portable full restore independently of account/database/mirror.
- Preserve exact refs/raw tag IDs/HEAD and support capability-detected object formats; unknown or unpreservable cases fail explicitly, never silently omit history.
- No overwrite, verification bypass, TLS bypass, plaintext secret fallback, or arbitrary Git command configuration.
- Separate operation outcome, artifact visibility/durability, and cloud outcome. Post-publication failure must retain known artifact identity.
- Capture normalized configuration at admission; retain credential reference/generation without values.
- Process-tree termination and qualified filesystem publication precede write-enabled engine use, not a later polish milestone.
- Tests cross module interfaces with real temporary Git repositories and isolated fake transports; no real credentials or repositories required by default tests.

## Automation and human gates

Agents can implement/test the specified modules with synthetic fixtures once implementation is requested. They can research facts, choose reversible defaults within these contracts, and run bounded independent review/correction. They cannot weaken acceptance checks, change product coverage, approve their own review, or close human-decision tickets on the user's behalf.

Real credential access, enabling cloud upload, destructive retention, and schedules require explicit in-app user actions. Release identity, license/reporting channel, supported-platform claims, and signing/publication remain [wayfinder decision gates](../plans/wayfinder/MAP.md). Missing release gates do not block local synthetic engine work; they block the affected external operations or distribution.

## Exclusions

Incremental bundle chains, LFS payload storage, submodule recursion, GitHub issue/PR/release-asset exports, client-side encryption, background OS services, team accounts, and a separate worker process remain outside this release. Show coverage limitations; do not label it a complete GitHub account backup.

## Completion

Every slice needs named test evidence, honest implemented/unwired status, reviewed diffs or file hashes, and explicit pending qualification. Full release needs all task gates plus [operations release gates](OPERATIONS.md#release-gates). “Code compiles,” “uploaded,” and “an agent said done” are not recovery or release proofs.

The [planning review record](implementation/REVIEW.md) documents resolved handoff findings and observed checks. Planning acceptance does not change any implementation task's TODO status.
