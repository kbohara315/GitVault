# Adversarial design review

This ledger consolidates independent GPT-6.1 Sol audit and grilling passes. Technical questions were answered from the repository where possible. All newly suggested product restrictions remain **proposals**. Existing source and architecture documents were left unchanged by this advisory pass.

## 1. What is the smallest useful result?

**Answer:** A full snapshot of a local repository that restores into a new bare repository without the source, mirror, database, or network. Evidence: `docs/IMPLEMENTATION.md:11–22`; `docs/PRODUCT.md:53–55`.

**Challenge:** Adding SQLite, authentication, or UI job controls first makes the recovery proof depend on unrelated work.

**Resolution:** Prove the round trip behind one `internal/backup` module. Keep the UI unchanged for these plans. Integrate jobs afterward.

## 2. Does “verified” have one meaning?

**Answer:** No. Schema validity, checksum validity, prerequisite independence, reference/HEAD equality, object integrity, and offline restoration are separate checks. Evidence: `docs/BACKUP-FORMAT.md:91–98`.

**Challenge:** `fsck` can report dangling objects while succeeding. Reference equality does not prove that the pack contains only reachable objects.

**Resolution:** Return an explicit verification report. Do not claim bundle authenticity, encryption, secret removal, or minimal object contents. Check the integrity of imported objects; record a deliberate extra-object policy before implementing external import.

## 3. Which refs are in scope?

**Answer:** The current document says refs available in the mirror, not merely default branches (`docs/BACKUP-FORMAT.md:25–32`).

**Challenge:** Silently narrowing to branches and tags would change coverage. Replacement refs and unusual namespaces need specific handling.

**Resolution:** Plan 001 inventories supported namespaces and rejects unsupported input explicitly. Branches/tags-only may be a useful first limitation, but it must be approved and documented before shipping. Never silently drop discovered refs or label filtered coverage “all refs.”

## 4. Can refs alone represent HEAD?

**Answer:** No. HEAD can be symbolic, detached, or unborn; an absent HEAD needs an explicit policy. Evidence: `docs/BACKUP-FORMAT.md:59,73`; `docs/TESTING.md:44`.

**Challenge:** Detached HEAD may retain a commit unreachable from refs. Annotated-tag IDs must remain tag-object IDs, not peeled commit IDs.

**Resolution:** Use a tagged HEAD representation, preserve exact object IDs, and qualify detached-object bundling with a real Git fixture. Reject a state that cannot be preserved; do not invent a default branch.

## 5. Does empty mean “no branches”?

**Answer:** No. Tag-only history or a detached commit may still be bundleable. Evidence: `docs/BACKUP-FORMAT.md:34`.

**Resolution:** A truly empty repository returns a typed no-artifact result. Tag-only and detached-history fixtures must be tested separately. Never report an invalid empty bundle as a successful snapshot.

## 6. Is argument-vector execution sufficient?

**Answer:** It prevents shell interpolation, not configuration or environment injection. Evidence: `docs/SECURITY.md:19,33–35`.

**Challenge:** Git reads environment variables, templates, repository config, alternates, replacement refs, and helpers.

**Resolution:** Centralize operation-specific environment/configuration policy, allowed transports, resource bounds, and process-tree cancellation. Local-source fixture access and offline restore have different transport requirements. Neither should inherit arbitrary user Git behavior.

## 7. When is cancellation complete?

**Answer:** Only after owned child processes have terminated and been reaped. Evidence: `docs/SECURITY.md:33`; `docs/ARCHITECTURE.md:83`.

**Challenge:** A surviving helper can keep pipes or files open after the parent exits.

**Resolution:** Move process-tree cancellation qualification into the initial runner plan. Durable job cancellation UI can still arrive later. Return cancellation separately from whether a snapshot already reached publication.

## 8. How much disk does verification require?

**Answer:** Source, mirror, staged bundle, verification repository, and Git temporary files can coexist. Evidence: `docs/BACKUP-FORMAT.md:79–83`.

**Challenge:** Compressed bundle size is not a reservation or safe expansion estimate.

**Resolution:** Explain peak-live artifacts, enforce configurable resource limits, and handle write/sync/import failures. Never skip verification or delete old snapshots automatically to hide a disk-space failure.

## 9. Does checking destination absence prevent replacement?

**Answer:** No. Evidence: required contract in `docs/BACKUP-FORMAT.md:92–98`.

**Challenge:** Another writer can create the path after the check. Parent symlinks, Windows junctions, and filesystem differences complicate containment.

**Resolution:** Qualify a native no-replace publication primitive and a documented stable-parent trust boundary on each supported platform/filesystem. Fail closed if unavailable. Do not promise resistance to arbitrary same-user malware; `docs/SECURITY.md:11` excludes that threat.

## 10. Can two root locks still overlap?

**Answer:** Yes, if roots are nested or aliased. Evidence: root locking in `docs/ARCHITECTURE.md:79–82`; root layout in `docs/BACKUP-FORMAT.md:8–16`.

**Resolution:** Plan 003 defines root identity and overlap rejection, plus cooperating-process tests. Restoration must remain independent of the original backup root and database. Do not solve this with an unbounded global lock by accident.

## 11. Can restore hash one file and read another?

**Answer:** A path can change between hashing and Git import. Evidence: `docs/BACKUP-FORMAT.md:87,93–95`.

**Resolution:** Copy validated input into private bounded staging, then verify and consume staged bytes. Treat manifests as untrusted. This improves consistency; it is not a sandbox against a compromised account.

## 12. What happens when the UI does not read progress?

**Answer:** Engine termination must not depend on UI delivery. Evidence: `docs/ARCHITECTURE.md:95`; `docs/DATA-MODEL.md:76`.

**Resolution:** Initial engine methods return typed terminal results directly. Optional progress is coalesced and nonblocking. Later durable job state is authoritative; notifications only prompt UI refresh. Do not add an event subsystem before there is a real caller.

## Technical resolutions for execution

Preserve the documented product scope rather than silently narrowing it:

- Represent all captured valid refs and symbolic/detached/unborn/absent HEAD states in the contract. Qualification belongs to runner and round-trip tests, not manifest-only work. Abort an operation explicitly if the installed Git cannot preserve a required state; never silently omit it.
- Represent SHA-1 and SHA-256 with format-specific object IDs. Capability-test each at execution time. Unavailable capabilities produce explicit unsupported results, not reinterpretation or a promise that every installed Git supports both.
- Permit valid extra packed objects under the initial format, but require object integrity and exact refs/HEAD. Do not describe this as minimal object coverage or removal of hidden historical data. A future stricter import policy needs separate review.
- Put parser/output/resource limits in named options with documented defaults and fail explicitly when exceeded. Limit errors are not successful snapshots. Publication capability is qualified per OS/filesystem and fails closed when unavailable.

These resolve technical implementation questions without changing existing coverage promises. A new product restriction, such as permanently dropping detached HEAD or limiting coverage to branches/tags, still requires operator approval. No such restriction is authorized by these plans.
