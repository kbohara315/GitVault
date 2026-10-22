# Threat model and security design

**Status: required controls for implementation, not implemented guarantees.**

Read this with [RECOVERY-SEMANTICS.md](RECOVERY-SEMANTICS.md) and [CONFIGURATION.md](CONFIGURATION.md). Verification, no-replace publication, controlled execution, TLS validation, and deletion protection have no user-configurable bypass switches. Credentials are resolved through the selected native adapter, never ordinary configuration.

## Assets and trust boundaries

Assets: GitHub tokens, cloud credentials, private Git history, backup availability, restored data, and signing keys.

Boundaries: UI to engine; engine to Git child processes; remote metadata to local paths; snapshots to restore destinations; local files to cloud storage; downloaded updates to installed application.

Assume remote names, URLs, error text, bundle files, and manifests are untrusted. An attacker with code execution as the current OS user is outside the protection offered by a normal desktop credential store.

## Required controls

| Threat | Control |
| --- | --- |
| Token leaks into mirror or bundle | Never embed credentials in a remote URL; inspect stored remotes and test artifacts |
| Token leaks through command line | Use an isolated credential broker/helper; never a secret-bearing argument or shell string |
| Existing Git config runs helpers | Controlled Git environment; disable unwanted global/system configuration and hooks |
| Path traversal or symlink escape | Stable generated IDs, containment checks, and no-follow/exclusive destination operations |
| Concurrent writes | OS root lock, one run owner, and per-mirror serialization |
| Broken or incomplete snapshots | Temporary writes, verification, no-replace publication, restart reconciliation |
| Malicious bundle or manifest | Bounds, schema validation, safe refspecs, isolated Git processes, resource limits |
| Snapshot and checksum replaced together | Do not claim authentication; later add trusted signatures or authenticated encryption |
| Cloud transport interception | HTTPS and normal certificate verification; no insecure bypass setting |
| Destructive retention | Exact-ID preview, pins, leases, last-good-copy policy, and scoped delete |
| Secret-bearing logs | Structured redaction and bounded output; no environment dumps |

## Git authentication design gate

Choose and test a credential delivery strategy before private-repository support. A short-lived helper/broker can supply credentials only for the expected host and job. Keep secrets out of helper filenames, arguments, on-disk helper scripts, and logs. Refuse unexpected redirects/hosts according to a documented policy. Suppress interactive prompts for unattended runs.

On Unix and Windows, implement child-process cancellation for the whole owned process tree. A context that kills only the parent Git process can leave helpers running. Test this explicitly.

Use argument-vector execution, e.g. `exec.CommandContext`, with validated arguments. Never invoke a shell to interpolate repository names or URLs. Disallow local/file or unexpected Git transport schemes for GitHub discovery.

## Secret storage

Evaluate platform credential APIs or a maintained cross-platform adapter. Treat a locked, unavailable, or missing secret store as an explicit error. Do not fall back to plaintext automatically. Document Linux session-service requirements after selecting the adapter. Clearing a Go byte slice is best-effort; do not promise complete memory erasure.

The selected technical direction is the existing MyGo v0.6.2 native Secrets adapter; [DEPENDENCIES.md](DEPENDENCIES.md) records primary-source evidence and qualification limits. Native calls may block for consent/unlock and have no context cancellation. Bound outstanding calls and reject stale results after cancellation; never spawn unbounded timeout goroutines or claim the OS prompt was terminated. Unattended jobs may not trigger unlock prompts: require a qualified noninteractive path or an explicitly authorized still-valid bounded session lease; otherwise report interaction required.

## Data confidentiality

Bundles are compressed Git packs, **not encrypted archives**. They can contain secrets removed from current files but still present in history. Local permissions and provider-side encryption do not provide client-side encryption. Warn before uploading private history. Plan authenticated client-side encryption as a separate feature with key backup and recovery requirements.

## Cloud credentials

Use least-privilege access to the selected bucket/prefix. Separate upload and deletion capability where possible. Validate custom S3-compatible endpoints before sending credentials. Do not permit arbitrary non-HTTPS endpoints in production. Do not log signed URLs or authorization headers.

## Release security

Choose a project license and private reporting channel. Sign platform packages and future updates. Keep private signing keys out of source control and runtime backups. Review dependency changes and provenance. A pre-1.0 framework requires explicit upgrade review and smoke testing.

## Required security tests

Use synthetic sentinel credentials. Search configuration, Git remotes, process argument capture, logs, manifests, snapshots, and test artifacts for the sentinel. Test malicious names, traversal, symlink substitution, oversized manifests, locked secret storage, unauthorized cloud access, and Git helper process cancellation.
