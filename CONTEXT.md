# GitVault

GitVault protects repository history through retained snapshots and verified restoration.

## Language

**Repository**:
A Git project's history and named references. Its identity remains stable when its owner or name changes.
_Avoid_: Folder, account backup

**Mirror**:
A replaceable local copy used to acquire repository history. It is not a retained recovery point.
_Avoid_: Snapshot, backup

**Snapshot**:
A retained recovery point containing repository data and its manifest. A completed snapshot is not modified in place.
_Avoid_: Mirror, latest copy

**Full bundle**:
A portable Git data file whose included history requires no earlier snapshot.
_Avoid_: Incremental bundle, archive of project files

**Manifest**:
The versioned description of a snapshot's identity, contents, saved references, HEAD, and coverage.
_Avoid_: Settings, job record

**Run**:
One accepted request to back up an explicitly selected set of repositories. It has one recorded intent and a separately recorded result for each repository.
_Avoid_: Schedule, snapshot

**Restoration**:
Reconstruction and validation of a repository from a snapshot into a new destination.
_Avoid_: Checkout, synchronization

**Cloud copy**:
An independently tracked copy of a snapshot at a selected remote target. Its upload status and verification evidence are separate facts.
_Avoid_: Snapshot success, cloud backup success

**Retention preview**:
A reviewable proposal naming the exact snapshots and copies eligible for deletion. It does not authorize deletion by itself.
_Avoid_: Cleanup, automatic permission

**Pin**:
A user's instruction to retain a particular snapshot until the pin is removed.
_Avoid_: Lease

**Lease**:
Temporary protection of snapshot inputs while an operation uses them.
_Avoid_: Pin

**Scheduled occurrence**:
One due time of a particular schedule revision. It can admit at most one run.
_Avoid_: Timer tick, run
