# Contributing

## Setup

Follow the [README](README.md). Use the Go version in `go.mod`. The runtime and MyGo CLI share a pinned module version; do not install an unpinned global CLI for normal development.

## Workflow

1. Read [the agent runbook](docs/AGENT-RUNBOOK.md), then select one unblocked task from the canonical [implementation plan](docs/IMPLEMENTATION.md). Historical `plans/001–003` are not current execution instructions.
2. Define the observable behavior and failure cases.
3. Implement the smallest complete slice with automated tests.
4. Update documentation and current-status statements.
5. Run formatting, tests, vet, and compile checks.

```sh
gofmt -w main.go internal
go test ./...
go vet ./...
go build ./...
```

Use descriptive errors. Wrap causes with `%w`. Pass `context.Context` as the first argument of blocking service methods. Keep interfaces at consumer boundaries and introduce them when a real implementation or test needs them.

Avoid package-global mutable job state. Distinguish stage progress from measured byte progress. Never label a snapshot “restorable” solely because upload succeeded.

## Dependencies and releases

Explain each new dependency and its platform, license, and security implications. Update MyGo runtime and tool together, then run headless tests and desktop smoke tests on all target operating systems. Do not commit credentials, real backups, signing material, or machine-specific paths.

No license is selected yet. Agree on licensing and contribution terms before accepting external contributions or distributing source as an open-source product.
