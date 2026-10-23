# Documentation map

GitVault uses native MyGo UI and Git bundle snapshots. These documents use short, direct language inspired by ASD-STE100. They are not certified STE documents.

## Current status

Reviewed modules: bounded snapshot contract, Linux controlled Git runner, SQLite/configuration, native secrets and credential broker, HTTPS GitHub discovery, S3/R2 transport, scheduling/retention policies, native command controls, and bounded UI event bridge. I03 offline recovery is accepted for Linux host scope only. Behavioral tests use synthetic inputs and temporary resources. Non-Linux/filesystem behavior, native platform behavior, and real provider behavior are not qualified by headless tests.

In progress: I04 durable application coordination and reconciliation; the native desktop still starts without its real backend. I03's Linux host gates and global checks pass, but non-Linux and additional filesystem qualification remain pending. Real S3/R2 and native credential behavior remain unqualified. Module acceptance does not complete its full integration task; see [TASKS.md](implementation/TASKS.md) and retained evidence. Do not use this version to protect data.

## Read in this order

1. [../CONTEXT.md](../CONTEXT.md): domain glossary.
2. [PRODUCT.md](PRODUCT.md): release target and exclusions.
3. [IMPLEMENTATION.md](IMPLEMENTATION.md): canonical full-release execution authority.
4. [AGENT-RUNBOOK.md](AGENT-RUNBOOK.md): automated execution, restart, and bounded review.
5. [implementation/TASKS.md](implementation/TASKS.md) and [tasks.json](implementation/tasks.json): task scopes, dependency DAG, and acceptance gates.
6. [ARCHITECTURE.md](ARCHITECTURE.md) and [MODULES.md](MODULES.md): module depth, ownership, composition, and interfaces.
7. [CONFIGURATION.md](CONFIGURATION.md): typed defaults, limits, revisions, and activation timing.
8. [BACKUP-FORMAT.md](BACKUP-FORMAT.md) and [RECOVERY-SEMANTICS.md](RECOVERY-SEMANTICS.md): portable representation, commit points, and recovery.
9. [DATA-MODEL.md](DATA-MODEL.md) and [SECURITY.md](SECURITY.md): persistence and required controls.
10. [DEPENDENCIES.md](DEPENDENCIES.md): primary-source integration research and qualification.
11. [ACCEPTANCE.md](ACCEPTANCE.md), [TESTING.md](TESTING.md), and [OPERATIONS.md](OPERATIONS.md): observable acceptance outcomes, verification, and release gates.
12. [Foundation decision](decisions/0001-foundation.md) and [module decision](adr/0002-deep-modules-and-configured-composition.md): technical direction.
13. [Wayfinder map](../plans/wayfinder/MAP.md): decision frontier and human gates.
14. [SOURCES.md](SOURCES.md): primary reference index.

Historical numbered handoffs in `plans/` are superseded for execution, not completed. Their original baseline is old review evidence, not a drift gate after intentional documentation consolidation.

[Planning review record](implementation/REVIEW.md): independent Sol findings, corrections, document validation, and the distinction between the existing scaffold checks and future required tests.

See [implementation/REVIEW.md](implementation/REVIEW.md) for independent planning-review findings, corrections, actual document checks, and the distinction between accepted planning and unimplemented features.

## Terms

- **Mirror:** A mutable bare repository used to fetch Git data.
- **Snapshot:** An immutable backup directory with a bundle and manifest.
- **Full bundle:** A bundle with no external prerequisite commits.
- **Incremental bundle:** A bundle that requires Git objects from earlier snapshots.
- **Manifest:** A versioned description of the snapshot and its saved references.
- **Verification:** A named check. State whether it is a checksum, bundle, object-integrity, or restoration check.
- **Run:** One user or scheduler request that contains repository tasks.
- **Restoration:** Reconstructing a repository from a snapshot into a separate directory.
