# Full-release acceptance ledger

**Specification only.** These are required outcomes, not completed test results. Concrete test names, ownership, and commands belong to [implementation/TASKS.md](implementation/TASKS.md). [AGENT-RUNBOOK.md](AGENT-RUNBOOK.md) defines evidence and restart handling.

## Recovery and artifact contract

| Gate | Required observable evidence |
| --- | --- |
| REC-01 | A full snapshot restores from only its manifest/bundle and installed Git after source/mirror are unavailable; no database, account, or network is consulted. |
| REC-02 | All supported captured refs and raw annotated-tag IDs match; symbolic/detached/unborn HEAD is restored exactly where qualified; unsupported state is explicit, never silently omitted. |
| REC-03 | Corrupt payload, incorrect size/hash, prerequisites, malformed/oversized metadata, duplicate keys, unsupported versions, and invalid refs are rejected before publication. |
| REC-04 | Tag-only/detached-history inputs are not mistaken for empty; truly empty input produces no fake ready snapshot. |
| REC-05 | Reference equality and object integrity are checked separately. Valid extra packed objects are not mistaken for an authenticity/minimality guarantee. |

## Process, filesystem, and lifecycle

| Gate | Required observable evidence |
| --- | --- |
| SAFE-01 | Hostile Git environment/config/templates/helpers cannot change controlled operations; offline restore cannot retrieve missing objects. |
| SAFE-02 | Cancellation/deadline terminates and reaps owned descendant writers before staging cleanup/unlock; pipes and output are bounded. |
| SAFE-03 | A competing file/directory/symlink created at publication is preserved; destination is never replaced. Native filesystem evidence is recorded per target. |
| SAFE-04 | Disk-full/write/sync/rename failure produces truthful results; published identity survives post-commit errors; only owned uncommitted staging is eligible for cleanup. |
| SAFE-05 | Rapid double-start, another cooperating process, root aliases, conflicting nested paths, and a second installation/database cannot bypass managed root ownership or its shared pin/lease authority. Foreign/lost bindings still allow read-only portable import. |
| SAFE-07 | Two installations/databases targeting the same root cannot establish independent pin/lease/deletion authority; foreign bindings fail, while read-only import and private-copy restore remain available. |
| SAFE-06 | View callbacks do no blocking engine work; coalesced/unread progress cannot lose durable terminal truth or deadlock quit. |

## Durable operation and configuration

| Gate | Required observable evidence |
| --- | --- |
| STATE-01 | Admission persists intent before work; SQLite restart tests reconcile interrupted jobs, matching orphan final output, missing/corrupt artifacts, and uncertain publication. |
| STATE-02 | Unknown external output gains neither automatic deletion authority nor resurrection over a deletion intent. |
| STATE-03 | One current-revision pointer and immutable configuration records govern all settings. Projection updates are atomic, stale edits conflict, and accepted selection/target/credential generations remain fixed during a run. |
| STATE-04 | Settings/database/artifact versions are distinct; migrations are transactional, tested, and refuse future schemas/altered history. |
| STATE-05 | Every documented default and invalid cross-field setting has a table-driven test; no verification/no-overwrite/TLS/plaintext bypass exists. |
| STATE-06 | Concurrent edits cannot publish partial selection/target/schedule projections: one atomic current-revision pointer controls configuration and admission captures that exact revision. |

## Identity, selection, cloud, and retention

| Gate | Required observable evidence |
| --- | --- |
| DATA-01 | Synthetic credential sentinels do not appear in arguments, stored remotes, config, logs, manifests, or artifacts. Native unavailable/locked/denied states never trigger plaintext fallback. |
| DATA-02 | Paged GitHub discovery handles bounded rates/errors, renames retain stable selection, new discoveries stay unselected, and empty selection starts no work. |
| DATA-03 | Optional cloud copy consumes ready local output, publishes manifest last, distinguishes receipt/read-back/restoration, and retries without regenerating snapshots. |
| DATA-04 | Cancelled/lost-response multipart work has persisted cleanup identity, bounded abort/reconciliation, finite retry ownership, and qualified provider behavior. |
| DATA-05 | A remote download is bounded and verified through the same private-input restoration path; ETag is not treated as universal SHA-256. |
| DATA-06 | Preview names exact IDs/copy scope/revisions; pins, leases, and at least one valid local snapshot per repository are protected; stale previews fail rather than expand. |
| DATA-07 | Authorized deletion persists per-effect intent, recovers partial local/remote effects, and does not claim secure erasure/version-history purge. |

## Schedule, platform, and release

| Gate | Required observable evidence |
| --- | --- |
| REL-01 | Fake-time tests cover DST gap/fold, monthly rule, clock rollback, wake/restart, occurrence identity, busy admission, and a single catch-up offer requiring acceptance. |
| REL-02 | Unattended jobs do not trigger unqualified native unlock prompts; expired/missing permission yields a visible interaction-required result. |
| REL-03 | Tray availability, close versus quit, activation/reopen, pending work, main-thread dispatch, and blocked native-prompt behavior are exercised on claimed platforms. |
| REL-04 | Native runtime/process/filesystem/secret-store/accessibility evidence supports each advertised OS/architecture/filesystem combination; compilation alone does not. |
| REL-05 | Maintainer provides identity/license/security contact and privately supplied signing credentials where needed, authorizes publication, and completes a clean-install offline recovery drill. |

## Evidence discipline

Record task ID, exact required test enumeration/execution, exit status, host/OS/filesystem/Git/dependency versions, source revision or scoped hashes, reviewer objections/resolutions, and remaining limits. Keep native tests, fake-provider tests, real-provider opt-in checks, and cross-compilation evidence separate.

An agent cannot change a failing gate into an optional one to finish. A missing credential, native platform, or release approval blocks the relevant qualification/action; record it accurately and continue only independent authorized work. No test result is asserted by this ledger.
