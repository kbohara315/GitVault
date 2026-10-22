# Credential adapter seam

Construct one session adapter with `NewNative(namespace, development)` at startup. Construction does not access native secrets. The explicit namespace must remain stable and be release-approved; `.development` and `.production` are separate services. Tests use `New(NewMemoryBackend())`, not native storage.

Public API (no MyGo types):

```go
type Handle struct { Reference string; Generation uint32 }
func (Handle) Validate() error
func NewNative(namespace string, development bool) (*Adapter, error)
func New(Backend) *Adapter
func (*Adapter) Store(context.Context, Handle, []byte) error
func (*Adapter) ReadInteractive(context.Context, Handle) ([]byte, error)
func (*Adapter) GrantLease(context.Context, Handle, bool, time.Time) error
func (*Adapter) ReadUnattended(context.Context, Handle) ([]byte, error)
func (*Adapter) Revoke(Handle) error
func (*Adapter) Delete(context.Context, Handle) error
func (*Adapter) CloseLeases()
func (*Adapter) Pending() bool
```

`Store` refuses replacement of an existing generation. Rotation creates a new generation; the coordinator must publish its settings revision only after successful storage, retain old generations for accepted work, and persist revocation/generation history itself. Revocation is session-local. Reuse one adapter per application session; constructing another adapter does not import in-memory leases or revocations. All native instances nevertheless share one outstanding-call slot.

The blocking injected `Backend` has `Set(string, []byte) error`, `Get(string) ([]byte,error)`, and `Delete(string) error`. It must use the package's classified errors and copy input values before returning. `MemoryBackend` does this and is intended only for synthetic tests, not a plaintext production fallback.

Every value is 1–2,560 bytes. Lease creation requires explicit permission and an expiry within one hour; at most 128 generations are cached. Access checks expiry, copies returned bytes, and never calls native code unattended. Revocation/session close wipes owned lease buffers and invalidates pending grants. Go cannot guarantee physical erasure of every runtime copy; callers own and should clear returned byte slices. Expired entries are removed lazily on access/grant or session close.

An OS prompt cannot be cancelled. Context cancellation discards late results but `Pending()` stays true until the backend actually returns; further operations return `ErrBusy`. A timed-out write can have taken effect, so the coordinator must not infer rollback or publish a credential change from a timeout. Deletion revokes memory access before attempting native deletion and can be retried explicitly if busy.

Not-found, unsupported, denied, unavailable, busy, revoked, invalid, exists and interaction-required are distinct sentinel errors. Native errors are sanitized: typed permission errors and known macOS denial statuses become denied; MyGo's untyped/unknown platform errors remain unavailable rather than relying on localized message guesses. Native denied/locked/prompt behavior on each release OS remains unqualified. No native calls run in default tests. Job-scoped Git credential broker and UI/store integration are intentionally not implemented here.
