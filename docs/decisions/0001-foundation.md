# ADR 0001: native UI and full-bundle snapshots

Status: accepted for implementation. Date: 2026-10-22.

## Context

GitVault is a native Go desktop application for local-first repository backup and recovery. It needs portable snapshots, safe credential handling, and verified offline restoration.

## Decisions

1. Use MyGo v0.6.2 with its native Go UI and JSON configuration. Pin runtime and CLI together.
2. Keep a single process initially. Run blocking work in bounded goroutines and Git child processes. Separate the engine from UI packages so process isolation can be added later.
3. Use local bare mirrors as mutable caches, not retained backups.
4. Use full Git bundles with versioned manifests as the first snapshot format. Verify offline restoration before publishing ready snapshots.
5. Store operational metadata in SQLite when durable jobs are implemented. Store secrets through OS-backed credential APIs, not SQLite or fixed-key application encryption.
6. Route manual, scheduled, and tray starts through one job manager with cross-process root protection.
7. Make cloud copying optional. A failed upload must not invalidate a verified local snapshot or appear as full success.
8. Restore to a separate directory. Do not overwrite existing repositories; release one has no bypass switch.

## Consequences

Benefits: one implementation language; portable full snapshots; offline restoration using Git and the snapshot files; explicit failure states.

Costs: MyGo is pre-1.0; native platform behavior needs qualification. Full bundles duplicate history across snapshots. Git must be installed. Credential storage, process-tree cancellation, filesystem semantics, and packaging need platform-specific tests.

## Deferred decisions

SQLite driver, OS secret adapter, minimum Git version, project license, production identifier/module path, cloud SDK, update hosting/signing, client-side encryption, incremental snapshot format, and separate worker protocol.

Do not treat a deferred decision as an implemented guarantee. Record important future choices in separate ADR files.
