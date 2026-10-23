package app

import (
	"context"
	"errors"
	"time"

	"gitvault/internal/backup"
	"gitvault/internal/config"
	"gitvault/internal/secrets"
	"gitvault/internal/store"
)

// CredentialStore is the application-owned seam to native secret storage. It
// exposes no MyGo types and is never used by a native view.
type CredentialStore interface {
	Store(context.Context, secrets.Handle, []byte) error
	ReadInteractive(context.Context, secrets.Handle) ([]byte, error)
	Delete(context.Context, secrets.Handle) error
	CloseLeases()
	Pending() bool
}

var ErrCredentialUnavailable = errors.New("native credential storage is unavailable")

const credentialCleanupTimeout = 5 * time.Second

// NewInteractiveGitHubCredentialBroker resolves only the immutable credential
// handle captured by an explicitly user-started acquisition. The caller must
// close the broker after its Git child is reaped. Scheduled/unattended work must
// use a separately authorized in-memory lease, never this prompt-capable path.
func NewInteractiveGitHubCredentialBroker(ctx context.Context, credentials CredentialStore, revision config.Revision, repository, privateDir, executable string) (*backup.CredentialBroker, error) {
	if ctx == nil || ctx.Err() != nil || credentials == nil || revision.ID() < 1 {
		return nil, ErrCredentialUnavailable
	}
	values := revision.Config()
	if values.GitHub.Credential == nil {
		return nil, ErrCredentialUnavailable
	}
	configured := *values.GitHub.Credential
	handle := secrets.Handle{Reference: configured.Reference, Generation: uint32(configured.Generation)}
	secret, err := credentials.ReadInteractive(ctx, handle)
	if err != nil {
		return nil, err
	}
	defer clear(secret)
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	return backup.NewCredentialBroker(backup.CredentialBrokerOptions{
		Context:           ctx,
		PrivateDir:        privateDir,
		AllowedHost:       "github.com",
		AllowedRepository: repository,
		Username:          "x-access-token",
		Secret:            secret,
		Executable:        executable,
	})
}

func (w *LocalWorkflow) saveGitHubToken(command Command) {
	if w.credentials == nil {
		w.setError("Native credential storage is unavailable in this build.")
		return
	}
	if w.activeRunID != "" || w.Snapshot().Busy {
		w.setError("Wait for the active operation to finish before changing the GitHub credential.")
		return
	}
	if !validGitHubToken(command.Secret) {
		w.setError("Enter a 1–2560 byte GitHub token using visible ASCII characters without whitespace.")
		return
	}
	current, err := w.store.CurrentConfig(w.ctx)
	if err != nil {
		w.setError("Saved settings could not be loaded. The GitHub credential was not changed.")
		return
	}
	if current.ID() != command.ExpectedRevision {
		w.setError("Settings changed elsewhere. Reload saved settings before changing the GitHub credential.")
		w.refreshDurableView(w.ctx)
		return
	}
	values := current.Config()
	generation := int64(1)
	if values.GitHub.Credential != nil {
		generation = values.GitHub.Credential.Generation + 1
	}
	if generation > 2147483647 {
		w.setError("The GitHub credential generation limit was reached. Remove the configured credential before continuing.")
		return
	}
	reference, err := backup.NewID()
	if err != nil {
		w.setError("A new secure credential reference could not be created.")
		return
	}
	handle := secrets.Handle{Reference: reference, Generation: uint32(generation)}
	newCredential := config.Credential{Reference: handle.Reference, Generation: int64(handle.Generation)}
	w.mutate(func(s *ViewState) {
		s.CredentialBusy = true
		s.CanManageCredential = false
		s.Status = "Saving the GitHub credential to OS secure storage…"
		s.Error = ""
	})

	if err = w.credentials.Store(w.ctx, handle, command.Secret); err != nil {
		w.finishCredentialOperation("The GitHub credential was not added to settings.", credentialErrorMessage(err))
		return
	}
	values.GitHub.Credential = &newCredential
	revision, err := w.store.SaveConfig(w.ctx, current.ID(), values)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			// A CAS conflict is known to precede commit, so this unique candidate
			// cannot be referenced by a committed revision. Other database errors
			// may have an uncertain commit result; never delete a possibly referenced
			// generation to simplify that error.
			cleanupCtx, cancel := context.WithTimeout(context.Background(), credentialCleanupTimeout)
			cleanupErr := w.credentials.Delete(cleanupCtx, handle)
			cancel()
			message := "The credential was stored, but a settings conflict prevented the revision from committing."
			if cleanupErr != nil {
				message += " Cleanup of this unreferenced credential could not be confirmed."
			}
			w.finishCredentialOperation(message, "Reload settings before trying again; no credential value was written to settings.")
		} else {
			w.finishCredentialOperation("The settings update could not be confirmed. The OS credential was retained to avoid deleting a possibly referenced generation.", "Reload saved settings before retrying.")
		}
		w.refreshDurableView(w.ctx)
		return
	}
	w.mutate(func(s *ViewState) {
		s.CredentialBusy = false
		s.Revision = revision.ID()
		s.CredentialConfigured = true
		s.Status = "GitHub credential saved in OS secure storage."
		s.Error = ""
	})
	w.refreshDurableView(w.ctx)
}

func (w *LocalWorkflow) removeGitHubToken(command Command) {
	if w.credentials == nil {
		w.setError("Native credential storage is unavailable in this build.")
		return
	}
	if w.activeRunID != "" || w.Snapshot().Busy {
		w.setError("Wait for the active operation to finish before removing the GitHub credential.")
		return
	}
	current, err := w.store.CurrentConfig(w.ctx)
	if err != nil {
		w.setError("Saved settings could not be loaded. The GitHub credential was not changed.")
		return
	}
	if current.ID() != command.ExpectedRevision {
		w.setError("Settings changed elsewhere. Reload saved settings before removing the GitHub credential.")
		w.refreshDurableView(w.ctx)
		return
	}
	values := current.Config()
	if values.GitHub.Credential == nil {
		w.setError("No GitHub credential is configured.")
		return
	}
	configured := *values.GitHub.Credential
	handle := secrets.Handle{Reference: configured.Reference, Generation: uint32(configured.Generation)}
	values.GitHub.Credential = nil
	revision, err := w.store.SaveConfig(w.ctx, current.ID(), values)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			w.setError("Settings changed elsewhere. Reload saved settings before removing the GitHub credential.")
		} else {
			w.setError("The settings reference could not be cleared. The native credential was not deleted.")
		}
		w.refreshDurableView(w.ctx)
		return
	}
	w.mutate(func(s *ViewState) {
		s.CredentialBusy = true
		s.Revision = revision.ID()
		s.CredentialConfigured = false
		s.CanManageCredential = false
		s.Status = "Settings reference cleared; removing the configured GitHub credential from OS secure storage…"
		s.Error = ""
	})
	if err = w.credentials.Delete(w.ctx, handle); err != nil {
		w.finishCredentialOperation("The settings reference was cleared, but OS credential removal was not confirmed.", "The token may remain in OS secure storage, but GitVault will no longer use it. Replace it or remove the item from the OS secure store manually.")
		w.refreshDurableView(w.ctx)
		return
	}
	w.mutate(func(s *ViewState) {
		s.CredentialBusy = false
		s.CredentialConfigured = false
		s.Status = "Configured GitHub credential removed from OS secure storage."
		s.Error = ""
	})
	w.refreshDurableView(w.ctx)
}

func (w *LocalWorkflow) finishCredentialOperation(status, message string) {
	w.mutate(func(s *ViewState) {
		s.CredentialBusy = false
		s.Status = status
		s.Error = message
	})
	w.recomputeControls()
}

func validGitHubToken(value []byte) bool {
	if len(value) == 0 || len(value) > secrets.MaxValueBytes {
		return false
	}
	for _, b := range value {
		if b < 33 || b > 126 {
			return false
		}
	}
	return true
}

func credentialErrorMessage(err error) string {
	switch {
	case errors.Is(err, secrets.ErrDenied):
		return "Access to OS credential storage was denied. Unlock or approve the secure store, then retry."
	case errors.Is(err, secrets.ErrUnsupported):
		return "No supported OS credential store is available. No plaintext fallback was used."
	case errors.Is(err, secrets.ErrBusy), errors.Is(err, ErrCredentialOperationPending):
		return "A prior OS credential prompt is still pending. GitVault cannot cancel that prompt; wait for it to finish before retrying."
	case errors.Is(err, secrets.ErrUnavailable):
		return "OS credential storage is unavailable. Check the platform credential service and retry."
	case errors.Is(err, secrets.ErrInvalid):
		return "The credential request was invalid or exceeded the supported size."
	case errors.Is(err, secrets.ErrExists):
		return "This credential generation already exists. It was not overwritten."
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "The credential operation ended while an OS prompt may still be pending. Its late result will not be used."
	case errors.Is(err, secrets.ErrNotFound):
		return "The configured credential was not found in OS secure storage."
	default:
		return "OS credential storage did not complete. No token value was included."
	}
}
