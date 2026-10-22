# Architecture review and execution plans

> **SUPERSEDED FOR EXECUTION.** Follow [the full-release plan](../docs/IMPLEMENTATION.md), [task specifications](../docs/implementation/TASKS.md), and [agent runbook](../docs/AGENT-RUNBOOK.md). The advisory files below remain history, not completed implementation. Their old baseline is not a current drift requirement.

Date: 2026-10-22. Stack: Go 1.27.1 and native MyGo v0.6.2.

**Status: advisory plans only. No engine code was implemented in this pass.** Two GPT-6.1 Sol OpenCode subagents independently audited and challenged the design. The advisor checked their cited files and consolidated the results.

The user requested self-questioning instead of a user interview. The top three implementation prerequisites were therefore selected by default. Preserve the existing coverage contract using the technical resolutions in `DESIGN-REVIEW.md`; these plans do not authorize a product-scope reduction. An executor must stop before making such a reduction.

## Verification baseline

`go test ./...` and `go vet ./...` passed during this review. Only the native scaffold exists. Git history is unavailable: `git rev-parse --short HEAD` reports that this directory is not a Git repository. No Git repository was initialized. `baseline.json` records content hashes instead of inventing a commit SHA. Each plan includes an executable drift check.

## Vetted findings

These are **target-contract gaps**, not vulnerabilities demonstrated in an implemented backup engine.

| # | Finding | Category | Impact | Effort | Fix risk | Confidence | Evidence |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 1 | Define refs, HEAD, formats, and verification meanings | Architecture | Prevents incompatible artifacts and misleading recovery claims | M | MED | HIGH | `docs/BACKUP-FORMAT.md:38,73–75,95–97` |
| 2 | Define operation-specific Git isolation and process-tree ownership | Security design | Prevents inherited configuration and helpers from changing offline operations | L | MED | HIGH | `docs/SECURITY.md:19,33–35`; `docs/IMPLEMENTATION.md:15,31` |
| 3 | Qualify no-replace publication and cleanup ownership | Correctness design | Prevents overwrites, unsafe cleanup, and partial output reported as ready | L | HIGH | `docs/BACKUP-FORMAT.md:79–98`; `docs/ARCHITECTURE.md:79–83` |
| 4 | Start with one deep engine module | Architecture | Keeps shared artifact invariants local before callers multiply | S decision / L implementation | LOW | HIGH | `docs/ARCHITECTURE.md:48–59`; `docs/IMPLEMENTATION.md:3,13` |

## Execution order

| Plan | Title | Priority | Effort | Depends on | Status |
| --- | --- | --- | --- | --- | --- |
| [001](001-snapshot-contract.md) | Specify and validate the snapshot contract | P1 | M | — | TODO |
| [002](002-controlled-git-runner.md) | Own Git configuration, output, and process lifetime | P1 | L | 001 | TODO |
| [003](003-offline-backup-restore.md) | Prove a safe offline snapshot and restore | P1 | L | 001, 002 | TODO |

`001 -> 002 -> 003`. Run serially because the plans share `internal/backup`. Each executor must read its complete plan, honor STOP conditions, and update the status row with verification evidence. No commits, pushes, or PRs are authorized by these plans.

## Proposed first engine shape

```text
Native UI (unchanged in these plans)
                    |
          future application commands
                    |
                    v
          internal/backup module
      +--------------------------------+
      | CreateLocal / Verify / Restore |
      +--------------------------------+
      | Manifest and coverage checks   |
      | Controlled Git execution       |
      | Private staging and ownership  |
      | Verified no-replace publication|
      +--------------------------------+
                    |
           installed Git + filesystem
```

The module interface includes safety invariants and error meanings, not only method signatures. Keep MyGo outside it. Avoid a generic command-execution interface exposed to callers. Add internal platform adapters only where OS behavior actually differs.

## Direction options, not bug fixes

1. **Offline recovery first:** directly delivers `docs/PRODUCT.md:53–55`. Costs disk space for mirror, bundle, and verification repository; avoids adding credentials and network failures before recovery is proven.
2. **Native job integration next:** fulfills the UI intent in `internal/desktop/app.go:15–18`. Add durable jobs and UI-thread event delivery after the engine; do not make progress-channel delivery the authority for terminal results.
3. **Cloud copy later:** `docs/IMPLEMENTATION.md:57–67` already separates upload from local readiness. Preserve this ordering; a cloud implementation should consume existing snapshots, not rebuild them.

## Findings considered and rejected or deferred

- “There is no backup engine” is an acknowledged milestone, not a newly discovered bug.
- Do not replace the current stack or add a separate worker process without measured need.
- Do not create twelve packages before one tested round trip exists.
- Do not claim exact-reference checks prove an exact set of packed objects. However, rejecting every valid bundle with extra unreachable objects is also not an established requirement. Plan 001 must record a deliberate policy, not silently add a confidentiality guarantee.
- A per-user global lock is not adopted by default. Determine the smallest supported ownership policy in plan 003; unrelated backup roots need not be serialized permanently.
- Do not use string path containment or `Stat` followed by `Rename` as a no-overwrite guarantee.
- No dependency migration or added database is justified for the existing three-file scaffold.

## Not audited

No vulnerability database scan, runtime desktop/accessibility qualification, signed installer testing, S3/R2 test, OS filesystem experiment, or Git-version experiment was performed. No framework footprint claims were measured. Platform mechanisms in the plans remain qualification work, not guarantees.

See [the question-and-answer ledger](DESIGN-REVIEW.md) for proposed decisions and remaining tradeoffs.

## Cold-review corrections

A third GPT-6.1 Sol subagent reviewed the handoff independently. Revisions separate manifest definition from later detached-HEAD qualification, retain existing coverage rather than gate work on new restrictions, include `docs/README.md` in status updates, permit temporary Git fixtures, and distinguish host verification from pending cross-platform qualification.
