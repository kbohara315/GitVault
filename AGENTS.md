# Repository instructions

- Read `docs/README.md`, `docs/ARCHITECTURE.md`, and `docs/IMPLEMENTATION.md` first.
- Use `docs/AGENT-RUNBOOK.md` and `docs/implementation/TASKS.md` for execution. Preserve `CONTEXT.md` terminology. Historical numbered files under `plans/` and their original baseline are superseded, not completed work.
- Use MyGo native UI and Go for the application. Keep dependencies consistent with this stack.
- Keep the current implementation status honest. Update docs when a milestone is completed.
- Keep backup logic outside `internal/desktop`. Never run Git, network requests, or blocking disk operations in a view.
- Prefer full bundles first. Incremental bundle chains are deferred until full restoration is proven.
- Do not put credentials in URLs, arguments, settings, manifests, logs, or fixtures. Use synthetic data in tests.
- Use argument-vector subprocess calls, not shell command strings. Propagate contexts and bound output.
- Do not overwrite restore destinations or remove snapshots without explicit scope and dependency checks.
- UI state belongs to the UI thread. Workers publish immutable events through a bounded channel.
- Add behavioral tests with each engine slice. Use temporary repositories and directories, not a developer's repositories.
- Use `gofmt`, `go test ./...`, `go vet ./...`, and `go build ./...` before handing off.
- Pin dependencies and keep `go.sum`. Check MyGo documentation against the pinned release before adopting newer APIs.
- Do not add unused infrastructure packages or claim unimplemented services work.
- Capture typed configuration once per operation. Safety invariants are not configurable. Keep operation outcome, publication/durability, and cloud-copy result separate.
- Resolve AFK facts with evidence, but never impersonate a user's HITL decision or grant yourself credential, deletion, signing, or release authority. Bounded independent review cannot weaken acceptance gates.
