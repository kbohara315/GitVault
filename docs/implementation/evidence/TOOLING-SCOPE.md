# Gate-evidence tooling scope

Implementation authorization includes the runbook's named-test gate checker. The parent reserves `internal/quality/**` and `cmd/gatecheck/**` for a standard-library-only developer tool that consumes actual `go test -json` events and rejects missing, skipped, or failed required tests. It does not auto-approve implementation/release or alter task acceptance. Its test seam is the named-gate evidence interface already specified by the runbook.

This work is independent of I01's owned recovery files. No additional runtime dependency, app feature, Git repository, credential access, or live service is introduced. Global formatting checks must include the added command source directory.
