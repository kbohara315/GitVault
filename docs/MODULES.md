# Module composition and ownership

Status: target design. The implemented subset now includes the recovery, configuration, persistence, coordination, and native local-workflow modules; I05 is accepted for Linux-host implementation scope. The diagrams describe the full target composition, not an assertion that every service is integrated or platform-qualified. This specification uses module, interface, seam, adapter, depth, leverage, and locality deliberately.

## Full application shape

```text
                   main.go composition root
          concrete adapters + normalized startup values
                              |
   native UI ----+            v
   tray ---------+------> run coordinator <------ scheduler
                              |
        +---------------------+---------------------+
        |                     |                     |
        v                     v                     v
 repository discovery   recovery module        cloud copies
   GitHub adapter       internal/backup        S3/R2 adapter
        |                     |
        +---------- durable metadata ----------+
                              |
                     SQLite persistence adapter

 retention module -> preview / revalidate / authorized effects
       uses snapshot leases, deletion intent, local/cloud adapters

 OS secrets adapter -> job-scoped credential broker -> acquisition only
 local full restore -> recovery module, no account/database prerequisite
```

## Modules, introduced on demand

| Module | Owns | Does not own | Test surface |
| --- | --- | --- | --- |
| `internal/backup` | manifest, exact refs/HEAD, controlled Git, full bundles, private staging, verification, restore, publication outcomes | account discovery, SQLite, scheduler, cloud, user deletion policy | real temporary-repository create/verify/restore |
| `internal/app` | admission, immutable intent, durable run/task transitions, concurrency, cancellation, reconciliation | Git syntax, manifest parsing, UI state | commands/outcomes across fake clock and real/test adapters |
| `internal/config` | typed defaults, validation, normalization, schema migration, immutable revision | operational effects, credentials, weakening invariants | decode/validate/migrate; introduced with real consumers |
| `internal/store` | transactional metadata persistence, migrations, queries, revision checks, recovery/deletion intent | deciding whether artifact bytes are valid | temporary SQLite database reopen/rollback fixtures |
| `internal/github` | PAT validation, paginated discovery, stable identity mapping, rate-limit policy | selection permission, Git transfer | fake HTTP responses and cancellation |
| `internal/secrets` | opaque credential references/generations, bounded native-store adapter and broker lifecycle | ordinary settings, snapshots, logging secrets | in-memory adapter plus native qualification |
| `internal/cloud` | ready-file copy/retrieval, multipart lifecycle, remote receipts/read-back, scoped deletion adapter | snapshot creation, overall run success, authorization | fake S3 transport plus opt-in S3/R2 |
| `internal/retention` | bucketed eligibility, exact previews, revision revalidation, authorized effect sequencing | silent background permission, mirror cleanup | fixed inventories/clocks and partial-effect recovery |
| `internal/scheduler` | occurrence computation, durable claim request, catch-up offers | direct engine invocation or duplicate admission policy | injected time, revision/occurrence fixtures |
| `internal/desktop` | view state, validation presentation, main-thread application, user commands | durable truth, engine work in views | native headless tests and qualified desktop tests |
| `internal/platform` | data directories, tray/lifecycle integration where needed | application policy or generic utility collection | platform adapter qualification |

Keep process/filesystem variation private inside recovery until a second real caller justifies extraction. The native secret adapter is private to `internal/secrets` and is constructed at startup; its external interface contains no MyGo types. Avoid a shared `domain` package that merely mirrors types owned elsewhere. Return module-owned immutable values; map at application seams only when meanings differ.

## Intended interfaces, described by behavior

No public SDK or plugin interface is required. These are in-process module responsibilities, to be encoded in Go only as each task introduces a real caller.

- **Recovery:** create from an approved local or acquired source; inspect/verify an immutable snapshot; restore into a new destination. Return typed results with artifact identity, publication state, verification evidence, and warnings. Context cancellation never implies rollback.
- **Run coordinator:** admit an explicit intent, request cancellation, query durable status, and retry only an eligible failed stage. Admission must be atomic before asynchronous discovery. A second backup request receives a typed busy result rather than creating an unbounded queue.
- **Discovery adapter:** validate identity and list accessible repository metadata. It cannot select repositories or transfer Git data.
- **Persistence adapter:** transactional run/task/intent operations and typed queries. Never return the raw database handle to the UI. Do not keep transactions open during Git/network work.
- **Secret adapter:** store/read/delete opaque values with distinct not-found/unavailable/denied results. Do not put MyGo types into recovery. Bound outstanding blocking native calls; OS prompts are not cancellable just because the caller's context expired.
- **Cloud adapter:** copy a specified ready artifact, retrieve a specified published copy, verify read-back, and perform explicitly authorized key/version operations. No arbitrary S3 command surface.
- **Retention:** generate a pure preview from a recorded inventory/revision and execute only a still-valid authorized preview. Execution reports each completed and pending effect.

## Composition rules

Construct concrete dependencies in `main.go` or a narrowly named startup module. Pass them explicitly. Avoid package-global mutable registries, service locators, reflection containers, arbitrary subprocess callbacks, and configurable executable hooks.

Use a clock seam for schedule/retention tests; HTTP and persistence seams for deterministic failures; a native secret adapter and in-memory fake for tests. These have real variation. Do not create an interface for every private helper merely because mocks are possible. The interface is the test surface: tests must verify externally meaningful outcomes, not internal call counts alone.

## Ownership rules

- The coordinator owns admission and durable intent. Recovery independently owns filesystem/process safety so offline use remains safe without the app.
- Backup writers acquire a root identity lock and a mirror lock in a documented fixed order. Reject nested managed roots; compare canonical identity, not raw strings. Do not rely on one application mutex across processes.
- Root registration must coordinate overlapping-root validation among cooperating processes. Define and test the registry/lock protocol before enabling multiple managed roots; release one supports one active managed root per installation.
- A managed root is exclusively bound to one installation/database authority. Under its shared OS root lock, validate a versioned root marker containing root ID, installation ID, store ID, canonical application-data location, and binding generation. Reject a foreign or ambiguous binding; do not silently adopt a root or create a second lease/pin authority. All managed writes, reads, pins, leases, and retention use that same authority. Read-only standalone import/restore remains portable: take a shared root read lock while making a private copy when a managed-root marker is present, then restore independently. Release one offers no automatic ownership takeover; recovery/rebind is explicit and non-destructive.
- Snapshot readers take leases before app-managed retention; standalone restore copies input privately and requires no original metadata store.
- Retention never deletes leased/pinned inputs. Persist intent before the first deletion; never broaden the approved preview during retries.
- Terminal outcome recording is independent of UI notification success. Workers may coalesce progress; terminal query results remain durable.

## Native UI integration at the pinned version

MyGo v0.6.2 provides synchronous `mygo.RunOnMain(func())` and goroutine-safe `Window.Invalidate()`. Never hold engine locks across synchronous main-thread dispatch. Do not retain `ui.Context` after a view returns. Use one bounded bridge, not one goroutine per progress event. Apply immutable event batches on main, then redraw; query durable terminal truth when refresh notifications coalesce.

Shutdown must not block the main thread waiting on a worker waiting for `RunOnMain`. Prevent quit while cancellation/reaping proceeds asynchronously; guard the final quit so it is not intercepted indefinitely. Native secret prompts have a separate blocking lifecycle and require explicit pending-prompt status.

MyGo frame timers are not the scheduler. Use Go timers plus persisted occurrences, with resume callbacks only as hints. Tray creation alone does not override last-window-close behavior; qualify menu availability before offering hide-to-tray.
