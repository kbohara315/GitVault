# GitVault

A native Go desktop application for local-first Git repository backup and recovery.

**Status: implementation in progress; not ready to protect data.** I01–I06 are accepted for their documented Linux/amd64 implementation scope, including native Start/Cancel smoke for I05 and synthetic Secret Service round-trip evidence for I06. Native locked/pending-prompt behavior and other platforms remain unqualified; no real GitHub request or remote acquisition is wired. I07–I16 remain unimplemented. Broader filesystem/crash behavior remains unqualified. Cloud copies, schedules, retention, and restoration are not wired into this build. See [current task status](docs/implementation/TASKS.md).

## Technology

- MyGo **v0.6.2**, pinned in `go.mod` for both runtime and CLI.
- Native GPU-drawn UI written in Go.
- Planned backup format: full Git bundles created from local bare mirrors.
- SQLite stores typed configuration and durable local run/snapshot metadata. Credential settings contain only native-store references and generations; the PAT value is stored through MyGo's OS credential service. GitHub discovery/acquisition and remote/cloud settings remain unimplemented.
- Planned optional cloud storage: AWS S3 and Cloudflare R2.

MyGo draws its own controls. “Native” does not mean standard OS widget controls. Its pre-1.0 API and platform support need validation before release.

## Requirements

- Go **1.27.1 or later**. The pinned MyGo version requires it. An older Go installation with automatic toolchain selection can download the required toolchain.
- A graphical desktop for running the app. Native Linux UI needs GTK 3, not WebKitGTK.
- Git on `PATH` for local backups and integration tests. The native workflow runs Git in a worker, not in a view; GitHub discovery is not wired.
- Network access for the first Go dependency download.

## Quick start

```sh
go mod download
go tool mygo doctor
go test ./...
go tool mygo dev
```

`dev` opens the desktop app and watches Go sources. It does not start a web server. Tests run without an application window.

```sh
go vet ./...
go build ./...
go tool mygo build
```

`go build` checks compilation. `go tool mygo build` packages the app into `build/`. On Unix, `make dev`, `make check`, and `make build` are optional shortcuts. Use the Go commands directly on Windows.

## Repository layout

```text
main.go                    Native window and application lifecycle
internal/desktop/          Native UI state, views, and headless tests
mygo.json                  Native-only packaging configuration
go.mod / go.sum            Pinned Go runtime and CLI dependencies
docs/                      Product specification and implementation plans
.github/workflows/ci.yml   Formatting, module, test, vet, and compile checks
```

The planned engine packages are documented, not generated as empty placeholders.

## Documentation

Start at [docs/README.md](docs/README.md). Read [the product specification](docs/PRODUCT.md), [architecture](docs/ARCHITECTURE.md), and [implementation plan](docs/IMPLEMENTATION.md) before implementing backup features.

For an implementation agent, start with [AGENT-RUNBOOK.md](docs/AGENT-RUNBOOK.md), then the [full task specification](docs/implementation/TASKS.md) and [machine-readable DAG](docs/implementation/tasks.json). [Configuration](docs/CONFIGURATION.md) and [recovery semantics](docs/RECOVERY-SEMANTICS.md) define operational and safety contracts.

## Before publishing

- Replace the placeholder Go module path `gitvault` with the real repository module path; update internal imports.
- Replace `com.example.gitvault` with an identifier you control **before distributing builds**. It affects application identity and data locations.
- Add an original application icon and choose a project license. No project license has been selected by this scaffold.
- Check product-name and trademark availability. GitVault is a working name.
- Complete platform, accessibility, signing, and recovery tests. No production release workflow is included.

See [CONTRIBUTING.md](CONTRIBUTING.md) and [SECURITY.md](SECURITY.md).
