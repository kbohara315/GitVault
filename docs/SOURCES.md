# Primary sources

[DEPENDENCIES.md](DEPENDENCIES.md) contains the current version-specific integration research, selected technical direction, and remaining executable qualification gates. Source review does not establish runtime/platform support.

Reviewed for the initial scaffold on 2026-10-22. The runtime and CLI are pinned to **MyGo v0.6.2**. The public documentation can change; compare examples with the pinned source before using newer APIs.

## MyGo

[DEPENDENCIES.md](DEPENDENCIES.md) records the current version-specific integration research and candidate dependency requirements. These are sourced design inputs, not installed-dependency, native-runtime, or provider qualification evidence.

- [Getting started](https://mygo.egoist.dev/docs/getting-started): native template and tool commands.
- [Native UI](https://mygo.egoist.dev/docs/ui): Go views, layout, and controls.
- [Configuration](https://mygo.egoist.dev/docs/configuration): JSON config and `linux.webview`.
- [Native view template at v0.6.2](https://github.com/egoist/mygo/blob/v0.6.2/cmd/mygo/template/native/main.go.tmpl).
- [Headless test template at v0.6.2](https://github.com/egoist/mygo/blob/v0.6.2/cmd/mygo/template/native/main_test.go.tmpl).
- [Module requirements at v0.6.2](https://github.com/egoist/mygo/blob/v0.6.2/go.mod): Go 1.27.1 minimum.

The scaffold follows the native-template APIs directly. Performance claims require measurements from GitVault itself.

## Git and Go

- [git-bundle](https://git-scm.com/docs/git-bundle): bundle creation, prerequisites, and verification.
- [git-clone](https://git-scm.com/docs/git-clone): bare and mirror behavior.
- [git-fetch](https://git-scm.com/docs/git-fetch): explicit reference transfer.
- [git-fsck](https://git-scm.com/docs/git-fsck): Git object integrity checks.
- [Go module tools](https://go.dev/doc/modules/managing-dependencies#tools): pinned tool dependencies.
- [Go toolchains](https://go.dev/doc/toolchain): automatic toolchain selection.

## Inspiration

- [hiteshchoudhary/gitbackup](https://github.com/hiteshchoudhary/gitbackup): product inspiration for repository selection, local backups, and optional cloud storage.
