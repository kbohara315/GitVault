# Native controls and bounded presentation bridge

Implemented native local/selected backup, credential, repository-selection, history, restore, settings, cloud, retention, and schedule controls behind `app.Backend`. `main.go` still starts without a real backend; these controls are not yet an integrated application feature.

OpenCode DeepSeek review found a reproducible pinned-MyGo checkbox-key panic and a stale managed restore identity after editing the path. Parent reproduced the panic, added nonempty-row/restore-edit regressions, and corrected both. Input keys now live on rows; snapshot/path changes clear stale destination/identity. External empty-ID paths intentionally mean portable import, pending common backend validation. Settings drafts carry their expected revision; reload is explicit.

GPT Luna 6 independently rechecked all reviewed files and ran scoped race tests and vet, returning **ACCEPT — scoped UI module only**. The bounded event mailbox copies slices, strips secret draft fields, never waits for UI delivery, and coalesces full snapshots. The UI-owned bridge applies configured bounded batches without synchronous worker-to-main calls. Actual headless tests cover command identity, busy actions, token clearing, errors, rendering nonempty repositories, input changes, immutable/coalesced progress, and worker termination while the UI stops draining.

No native graphical smoke, durable coordinator, common restore backend, tray, or completed I05 integration is implied.
