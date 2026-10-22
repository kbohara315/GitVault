# Backup format and restore protocol

**Status: version-one implementation contract; not implemented yet.** [RECOVERY-SEMANTICS.md](RECOVERY-SEMANTICS.md) defines effect/commit behavior and [CONFIGURATION.md](CONFIGURATION.md) defines finite resource limits. Qualify representation and Git capabilities with temporary repositories before shipping compatible artifacts.

## Layout

```text
<backup-root>/
  mirrors/github/<repository-id>.git/
  staging/<snapshot-id>.partial/
  snapshots/github/<repository-id>/<snapshot-id>/
    repository.bundle
    manifest.json
```

Use generated IDs that are safe path components. Do not construct paths directly from a remote repository name. Staging and final snapshots must be on the same filesystem for the rename protocol.

## Bundle creation

Illustrative commands below describe Git operations, not implemented GitVault commands. All real invocations must use separate arguments, a controlled environment, timeouts, and credential isolation.

```sh
git clone --mirror https://github.com/OWNER/REPO.git MIRROR.git
git -C MIRROR.git remote update --prune
git -C MIRROR.git bundle create SNAPSHOT.bundle --all
git -C MIRROR.git bundle verify SNAPSHOT.bundle
git bundle list-heads SNAPSHOT.bundle
```

Run clone only for a new mirror. For a full bundle, additionally verify with a fresh empty bare repository to prove no external prerequisite objects are required. Compare bundle heads against the manifest reference set. The mirror must remain locked throughout reference capture, bundle creation, and verification.

`--all` includes references available in the mirror. It does not guarantee access to provider-hidden references or preserve unreachable objects, reflogs, hooks, working files, configuration, LFS payloads, or external submodule repositories.

An empty repository may have no bundleable commits. Record an explicit `empty_repository` result. Do not publish an empty or invalid bundle as ready.

## Manifest example

The example shows required concepts, not a copyable artifact. Angle-bracket placeholders are not literal valid field values. SHA-256 and object IDs must contain full validated hexadecimal values. Implement the explicit validation rules below rather than inferring a schema solely from the example.

```json
{
  "format_version": 1,
  "snapshot_id": "<generated-snapshot-id>",
  "created_at": "2026-10-22T10:30:00Z",
  "application_version": "0.1.0",
  "repository": {
    "provider": "github",
    "id": "<stable-repository-id>",
    "display_name": "owner/project"
  },
  "git_object_format": "sha1",
  "bundle": {
    "file": "repository.bundle",
    "kind": "full",
    "sha256": "<64-hex-digits>",
    "size_bytes": 123456,
    "prerequisites": []
  },
  "head": { "kind": "symbolic", "target": "refs/heads/main" },
  "refs": {
    "refs/heads/main": "<full-object-id>",
    "refs/tags/v1.0.0": "<full-object-id>"
  },
  "coverage": {
    "git_refs": "advertised_and_fetched",
    "lfs_objects": "not_included",
    "submodule_repositories": "not_included",
    "github_metadata": "not_included"
  }
}
```

Support symbolic, detached, and absent HEAD explicitly in the eventual schema. Record the object format and validate compatible object-ID lengths. Never guess the default branch as `main` or `master`.

### Required representation and checks

- `format_version` is exactly 1; `bundle.kind` is exactly `full`; `prerequisites` must be empty. Unknown fields/versions and duplicate JSON keys are rejected, not silently ignored.
- `snapshot_id` is a generated lowercase UUID string. Repository identity is provider plus stable provider ID; the local-test source uses provider `local` with a generated ID. Display names never become path components.
- Creation time is RFC3339 UTC. App version is diagnostic, not a substitute for format version. A compatible parser does not require the original app version to restore.
- `repository.bundle` is the only payload filename. Manifest paths are never interpreted as arbitrary filesystem access. Byte size is a nonnegative bounded integer and SHA-256 is exactly 64 lowercase hex characters.
- Object format is `sha1` or `sha256`; raw IDs are respectively 40 or 64 hex characters. Parser representation supports both; actual installed Git must pass capability tests. Unsupported operations return explicit errors before publication.
- Ref keys are complete valid `refs/...` names and values are raw, unpeeled object IDs. Preserve all captured supported refs; reject an unpreservable namespace rather than silently filtering it. Ref names must pass Git's ref grammar and cannot be injected into flags/refspec syntax. Symbolic refs other than HEAD require explicit qualification; do not flatten one silently.
- HEAD is a tagged value: symbolic (`kind`, `target`), detached (`kind`, `oid`), unborn (`kind`, `target` pointing to a missing branch), or exactly `{"kind":"absent"}` with neither target nor oid. Reject contradictory fields and invalid targets. Parser acceptance of absent state is not an engine support claim: if Git cannot recognize/preserve it, return unsupported input with no published artifact, never an invented branch or a purportedly valid restore. Capture/restore qualification belongs to the engine tasks, not the parser alone.
- A detached HEAD root must be included even if unreachable from all refs. Compare the bundle's special HEAD advertisement separately from the `refs` map. Never assume a mirror clone copied the source's detached-only history; prove acquisition with a fixture or fail explicitly.
- Coverage states exclusions explicitly. A full bundle does not include LFS payloads, external submodule repositories, or GitHub metadata. Detect and warn about excluded resources where possible.
- Valid extra packed objects are permitted. Integrity of imported objects and exact saved refs/HEAD are required; object-pack minimality, confidentiality, authenticity, or secret removal are not guaranteed.

The parser bounds manifest bytes, counts, names, and total metadata before allocating large collections. Fixtures cover malformed JSON, duplicate keys, extra trailing documents, overflow, invalid encodings, and unknown kinds. Artifact semantics do not change when operational limits change; a smaller local limit may explicitly reject an otherwise valid artifact.

Parse manifests with file-size and collection-count bounds. Reject unknown format versions, absolute paths, traversal components, unexpected bundle filenames, unsupported object formats, and invalid reference names. Do not trust a filename or display name as a filesystem path.

## Publication protocol

1. Create a unique staging directory with restrictive permissions.
2. Write the full bundle. Capture included references and HEAD.
3. Verify prerequisites, bundle heads, and object integrity through restoration into a fresh temporary repository.
4. Compute bundle size and SHA-256. Write the manifest only after successful checks.
5. Flush files, close handles, and sync directory metadata where supported. Publish to the unique final location using a qualified no-replace primitive. Successful publication is the visibility commit point; later sync or bookkeeping errors retain the known snapshot identity and explicit uncertainty. Document platform durability limits.
6. When managed jobs exist, record a ready snapshot in a database transaction. The initial standalone engine returns its result without requiring SQLite. A crash between publication and transaction is reconciled from recorded intent and validated directories; unknown directories are inspected, not automatically granted deletion authority.
7. Upload the bundle if enabled, then publish the remote manifest last. Record cloud result independently.

Logical immutability is enforced by the application, not by normal filesystem permissions alone. External modification must be detected on later reads.

## Restore protocol: full bundle

1. Read and validate the manifest. Require `kind = full` and no prerequisites.
2. Acquire an input lease. Refuse a destination that already exists. Reject unsafe path or symlink redirection.
3. Copy bounded input into private staging and validate size/checksum on the bytes Git will consume. These checks detect changes, not malicious replacement of both manifest and bundle.
4. Create a fresh bare repository with the recorded object format in a temporary sibling directory.
5. Run `git bundle verify` against that repository. Import all recorded references with explicit refspecs. Do not depend on a default clone's branch mapping.
6. Restore HEAD and compare every recorded reference and object ID. Reject missing or unexpected references according to the version-one exact-set policy.
7. Run `git fsck --full`. Disable hooks, credential helpers, protocol extensions, and untrusted configuration during restoration.
8. Publish the restored bare repository to the new destination without replacement using the qualified primitive. Retain its identity in the result if later bookkeeping/durability work fails. Report bare restore completion; no checked-out working copy is included in this release.

No application database, network, GitHub credentials, or local mirror is required for this restore. The Git executable is required.

## Cloud keys and retention

```text
<prefix>/github/<repository-id>/<snapshot-id>/repository.bundle
<prefix>/github/<repository-id>/<snapshot-id>/manifest.json
```

Keep snapshot keys unique; never overwrite `latest.bundle`. A latest pointer, if added, is advisory and not the backup itself. Bucket lifecycle policies must not remove retained inputs behind the app's back.

Start retention with full bundles only. Preview exact snapshot IDs and affected local/remote copies. Keep at least one ready snapshot; honor pins and active restore leases. Do not prune after a failed backup. Track partial deletion and support reconciliation. Removing a mirror is a separate maintenance operation.

The canonical release-one protection is at least one valid ready **local** snapshot per repository. An upload receipt is not a substitute. Deletion needs explicit scoped confirmation and revision revalidation; enabling backup scheduling does not authorize automatic pruning. Remote version history/secure erasure is outside the deletion promise. See `RECOVERY-SEMANTICS.md` for marker-first remote deletion and partial-effect recovery.

## Deferred incremental format

Incremental bundles require prerequisite objects. Before supporting them, define immutable dependency IDs, cycle/missing-base rejection, exact saved reference restoration (including deletions), periodic full checkpoints, and dependency-aware retention. A deleted ancestor can make all dependent snapshots unrestorable. Do not emit incremental snapshots under the version-one full-only contract.
