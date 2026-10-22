# Product specification

## Goal

Help a developer retain and restore Git repositories. Make recovery status clear. Keep credentials outside backup data.

GitVault is the working name. Primary platforms are macOS, Windows, and Linux. Support is conditional on platform qualification, not just cross-compilation.

## Version-one scope

- Discover accessible GitHub repositories with pagination.
- Select repositories explicitly. An empty selection means **back up none**, not all.
- Store the GitHub token through an OS-backed secret mechanism.
- Create or update a bare mirror for each selected repository.
- Create a self-contained full Git bundle and a versioned manifest.
- Verify a snapshot before marking it ready.
- Restore a snapshot into a new directory and validate the result.
- Show repository-level stages, errors, cancellation, and run history.
- Limit concurrency. Prevent manual and scheduled runs from overlapping.
- Add optional S3/R2 upload, retention, and in-app scheduling in later version-one milestones.

The full execution route is [IMPLEMENTATION.md](IMPLEMENTATION.md). Technical defaults are configurable according to [CONFIGURATION.md](CONFIGURATION.md); safety invariants are not settings. Retention means preview plus explicitly confirmed deletion, not automatic pruning.

## Explicit exclusions

- Complete GitHub account export: issues, pull-request discussions, settings, Actions data, and release assets.
- Automatic backup of LFS objects, submodule repositories, or separate wiki repositories.
- Incremental bundle chains in the first release.
- Hosted accounts and team collaboration.
- Running backups while the application is stopped or the computer is off.
- Application-level encryption of bundle payloads in the first release. Clearly disclose this limitation.

Detect or disclose excluded resources. Do not claim a complete account backup.

## User flows

### Setup

Check Git availability -> choose a backup root -> configure credentials -> validate access -> select repositories. Do not persist secrets in normal settings.

### Backup

Confirm selection -> acquire run lock -> discover current metadata -> process selected repositories -> report local and cloud results separately. Use stable repository IDs so renames do not create a new identity.

### Restore

Select/import snapshot -> inspect manifest and exclusions -> select a new destination -> verify -> restore -> check references and object integrity -> show result. No GitHub connection is required for a local full-bundle restore.

### Scheduling

Store schedule timezone and next due time. Run only while the application is active. On wake or restart, offer at most one catch-up run; do not replay every missed interval. Display this behavior in Settings.

Before enablement show named gap/fold/month-day semantics from [RECOVERY-SEMANTICS.md](RECOVERY-SEMANTICS.md). Enabling a schedule authorizes future backup attempts for the selected scope, not cloud enablement or retention deletion.

## Acceptance criteria

1. A local full snapshot restores without network access or access to the mirror.
2. Included references resolve to the manifest's recorded object IDs after restoration.
3. A damaged bundle is rejected before a completed restore is reported.
4. Cancellation keeps the run lock until child processes stop and temporary output is handled.
5. A cloud failure leaves a verified local snapshot available and reports partial success.
6. A crash never makes a partial snapshot appear ready.
7. A locked secret store produces an actionable error, not plaintext fallback.
8. An empty repository selection starts no work.
9. A snapshot with excluded LFS/submodule data shows an explicit coverage warning.
10. The UI stays responsive during fetch, bundling, verification, and upload.

## Screen responsibilities

| Screen | Planned behavior |
| --- | --- |
| Overview | Last results, coverage warnings, next scheduled run |
| Repositories | Search, filters, explicit selection, access failures |
| Backups | Run stages, cancellation, local/cloud status, retry |
| Restore | Snapshot inspection, destination selection, validation |
| Settings | Secret references, locations, concurrency, retention, schedule |

Use stage labels when work size is unknown. Do not invent a precise overall percentage.
