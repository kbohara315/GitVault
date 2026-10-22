# Platform dependency scope

The parent pinned `golang.org/x/sys v0.49.0` for actual process ownership and no-replace filesystem adapters in I02/I03. This is the same version required by the reviewed SQLite candidate and uses the Go ecosystem's BSD-licensed platform bindings. It does not replace the native stack, add cgo, or authorize generic shell calls. The exact Go/MyGo pins are retained. Actual APIs and native behavior must be tested before platform claims; cross-compilation is separate evidence.
