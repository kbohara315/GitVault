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

The Linux I06 slice implements and synthetic-tests a short-lived helper/broker scoped to one `github.com` repository and one authenticated Git process group. It uses a private Unix socket, checks peer UID/process group/executable, refuses helper operations other than `get`, and emits credentials only on the Git protocol pipe. I07 wires bounded HTTPS REST identity and repository discovery with same-origin pagination/redirect checks, response/page/repository bounds, sanitized failure classes, and partial-inventory reconciliation. I08 wires selected GitHub acquisition through controlled real Git over HTTPS, stable-ID disposable mirrors, and the existing full-bundle verification/publication path. The broker accepts only the scoped protocol fields and bounded Git 2.53 capability/auth-challenge metadata; it discards non-authorizing metadata and rejects unknown fields. Production acquisition remains restricted to canonical GitHub HTTPS; test-only origin injection supports a synthetic TLS smart-Git fixture without relaxing production host checks. Tests use synthetic HTTP/HTTPS servers, repositories, and credentials; no live GitHub request or real credential was used. Keep secrets out of helper filenames, arguments, on-disk helper scripts, URLs, and logs. Unattended runs must not invoke the prompt-capable credential path.

The REST client sends a bearer token only in the HTTPS `Authorization` header. It never follows a cross-origin redirect. Pagination links must remain on the repository endpoint, preserve the requested affiliation/page-size/sort scope, and advance one page at a time. The token is resolved from the credential generation captured by the explicit discovery command and cleared from the app-owned byte buffer when the request returns; `net/http` requires transient header strings that the app cannot erase. Errors omit response bodies, URLs, transport text, and credentials. Only a fully paginated result marks unseen prior inventory as inaccessible; partial results update observed metadata only and never clear saved stable-ID selections.

On Linux, the controlled Git runner registers an owned process group at a startup barrier, cancels the group, bounds output draining, and reaps the direct child before releasing managed mirror ownership. Synthetic process and HTTPS tests cover the Linux implementation. Other Unix and Windows process-tree behavior remains unqualified; a context that kills only the parent Git process can leave helpers running.

Use argument-vector execution with validated arguments. Never invoke a shell to interpolate repository names or URLs. GitHub acquisition permits only canonical HTTPS GitHub repositories; local/file and unexpected Git transport schemes are refused.

## Secret storage

I06 uses the pinned MyGo v0.6.2 platform credential API behind `internal/secrets`. The UI draft owns token bytes, masks the displayed value and text-input queries, and clears its owned byte buffer on replacement, submission, navigation, and shutdown. MyGo's native callbacks still deliver transient Go strings; the app does not retain those strings, but cannot zero copies made by the framework or OS. This is not a complete memory-erasure guarantee. The Linux/amd64 host passed a synthetic Set/Get/Delete round trip through its live Secret Service; missing, denied/locked, unavailable, and pending-call outcomes remain explicit. This does not qualify other Linux desktops, macOS, Windows, or all prompt/lock states. Do not fall back to plaintext automatically.

The selected technical direction is the existing MyGo v0.6.2 native Secrets adapter; [DEPENDENCIES.md](DEPENDENCIES.md) records primary-source evidence and qualification limits. Native calls may block for consent/unlock and have no context cancellation. Bound outstanding calls and reject stale results after cancellation; never spawn unbounded timeout goroutines or claim the OS prompt was terminated. Unattended jobs may not trigger unlock prompts: require a qualified noninteractive path or an explicitly authorized still-valid bounded session lease; otherwise report interaction required.

## Data confidentiality

Bundles are compressed Git packs, **not encrypted archives**. They can contain secrets removed from current files but still present in history. Local permissions and provider-side encryption do not provide client-side encryption. Warn before uploading private history. Plan authenticated client-side encryption as a separate feature with key backup and recovery requirements.

## Cloud credentials

Use least-privilege access to the selected bucket/prefix. Separate upload and deletion capability where possible. Validate custom S3-compatible endpoints before sending credentials. Do not permit arbitrary non-HTTPS endpoints in production. Do not log signed URLs or authorization headers.

## Release security

Choose a project license and private reporting channel. Sign platform packages and future updates. Keep private signing keys out of source control and runtime backups. Review dependency changes and provenance. A pre-1.0 framework requires explicit upgrade review and smoke testing.

## Required security tests

Use synthetic sentinel credentials. Search configuration, Git remotes, process argument capture, logs, manifests, snapshots, and test artifacts for the sentinel. Test malicious names, traversal, symlink substitution, oversized manifests, locked secret storage, unauthorized cloud access, and Git helper process cancellation.
