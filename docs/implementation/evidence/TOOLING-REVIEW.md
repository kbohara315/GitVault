# Developer gate-tool evidence

Host: Linux/amd64. Model for independent review: `openai/gpt-6.1-sol`.

Implemented `internal/quality` and `cmd/gatecheck` without runtime dependencies. The tool executes a task's named Go tests and requires actual run/pass JSON events, package success, and no skipped/failed required descendants. It does not infer scenario coverage or approve application/release work.

Observed checks:

- Initial `go test ./internal/quality -count=1` failed before implementation (no production Go files).
- Real temporary Go-module tests now prove successful events are accepted, missing test patterns rejected, and a skipped real subcase rejected.
- Independent review found a timeout bug: cancellation of `go` did not bound reading stdout held by descendants.
- Correction closes the read pipe on deadline and owns a Unix process group. `TestGateTimeoutClosesInheritedPipe` uses an actual temporary Go test spawning a descendant retaining stdout for 30 seconds; the CLI rejects it within the bounded timeout.
- `go test ./internal/quality ./cmd/gatecheck -count=1` passed; independent reviewer reran the timeout regression and accepted the Linux correction.
- CLI smoke ran the real existing native-view tests and reported `TestInitialView` and `TestNavigation` passed. No missing engine tests were marked passed.

Windows developer-tool cancellation remains runtime-unqualified; this is not acceptance of the application's separate process-tree runner. Source implementation tasks retain independent review gates.
