// Package secrets isolates credential storage and explicitly authorized memory leases.
// Cancellation cannot terminate an operating-system unlock prompt. Pending reports
// that outstanding call, and no further call is admitted until it actually returns.
package secrets

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"time"
)

const MaxValueBytes = 2560

var (
	ErrNotFound            = errors.New("credential not found")
	ErrUnsupported         = errors.New("credential store unsupported")
	ErrDenied              = errors.New("credential access denied")
	ErrUnavailable         = errors.New("credential store unavailable")
	ErrBusy                = errors.New("credential interaction pending")
	ErrInvalid             = errors.New("invalid credential request")
	ErrExists              = errors.New("credential generation already exists")
	ErrRevoked             = errors.New("credential generation revoked")
	ErrInteractionRequired = errors.New("credential interaction required")
)

// Handle identifies an immutable stored generation, never a credential value.
type Handle struct {
	Reference  string
	Generation uint32
}

var referencePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func (h Handle) Validate() error {
	if !referencePattern.MatchString(h.Reference) || h.Generation == 0 || h.Generation > 2147483647 {
		return ErrInvalid
	}
	return nil
}
func (h Handle) key() string { return fmt.Sprintf("%s.%d", h.Reference, h.Generation) }

// Backend is the blocking platform seam. Implementations must return classified
// errors, not raw platform diagnostics that could contain credential data.
type Backend interface {
	Set(string, []byte) error
	Get(string) ([]byte, error)
	Delete(string) error
}
type lease struct {
	value   []byte
	expires time.Time
}
type Adapter struct {
	backend    Backend
	gate       chan struct{}
	mu         sync.Mutex
	revoked    map[Handle]bool
	leases     map[Handle]lease
	leaseEpoch uint64
	now        func() time.Time
}

// New constructs an isolated adapter, normally with a MemoryBackend in tests.
func New(b Backend) *Adapter {
	return &Adapter{backend: b, gate: make(chan struct{}, 1), revoked: make(map[Handle]bool), leases: make(map[Handle]lease), now: time.Now}
}
func (a *Adapter) Pending() bool { return len(a.gate) != 0 }
func clean(err error) error {
	if err == nil {
		return nil
	}
	for _, e := range []error{ErrNotFound, ErrUnsupported, ErrDenied, ErrExists, ErrInvalid, ErrRevoked} {
		if errors.Is(err, e) {
			return e
		}
	}
	return ErrUnavailable
}
func wipe(v []byte) { clear(v) }
func (a *Adapter) call(ctx context.Context, fn func() ([]byte, error), rejected ...func()) ([]byte, error) {
	discard := func() {
		for _, f := range rejected {
			f()
		}
	}
	if err := ctx.Err(); err != nil {
		discard()
		return nil, err
	}
	select {
	case a.gate <- struct{}{}:
	default:
		discard()
		return nil, ErrBusy
	}
	type result struct {
		v   []byte
		err error
	}
	ch := make(chan result)
	go func() {
		if ctx.Err() != nil {
			discard()
			<-a.gate
			return
		}
		v, err := fn()
		<-a.gate
		if err != nil {
			wipe(v)
			v = nil
		}
		select {
		case ch <- result{v, clean(err)}:
		case <-ctx.Done():
			wipe(v)
		}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-ch:
		if err := ctx.Err(); err != nil {
			wipe(r.v)
			return nil, err
		}
		return r.v, r.err
	}
}
func (a *Adapter) isRevoked(h Handle) bool { a.mu.Lock(); defer a.mu.Unlock(); return a.revoked[h] }

// Store writes a new generation only. A timed-out write may have reached the OS;
// retrying the same generation returns ErrExists rather than replacing it.
func (a *Adapter) Store(ctx context.Context, h Handle, value []byte) error {
	if h.Validate() != nil || len(value) == 0 || len(value) > MaxValueBytes {
		return ErrInvalid
	}
	if a.isRevoked(h) {
		return ErrRevoked
	}
	v := append([]byte(nil), value...)
	_, err := a.call(ctx, func() ([]byte, error) {
		defer wipe(v)
		old, e := a.backend.Get(h.key())
		wipe(old)
		if e == nil {
			return nil, ErrExists
		}
		if !errors.Is(e, ErrNotFound) {
			return nil, e
		}
		if e := ctx.Err(); e != nil {
			return nil, e
		}
		if a.isRevoked(h) {
			return nil, ErrRevoked
		}
		return nil, a.backend.Set(h.key(), v)
	}, func() { wipe(v) })
	return err
}

// ReadInteractive may initiate an OS prompt. Call only after explicit user action.
func (a *Adapter) ReadInteractive(ctx context.Context, h Handle) ([]byte, error) {
	if h.Validate() != nil {
		return nil, ErrInvalid
	}
	if a.isRevoked(h) {
		return nil, ErrRevoked
	}
	v, err := a.call(ctx, func() ([]byte, error) { return a.backend.Get(h.key()) })
	if err != nil {
		return nil, err
	}
	if len(v) == 0 || len(v) > MaxValueBytes {
		wipe(v)
		return nil, ErrInvalid
	}
	if a.isRevoked(h) {
		wipe(v)
		return nil, ErrRevoked
	}
	return v, nil
}

// GrantLease requires explicit permission and an expiry within one hour. It
// reads interactively once; subsequent unattended reads use memory only.
func (a *Adapter) GrantLease(ctx context.Context, h Handle, permission bool, expires time.Time) error {
	a.mu.Lock()
	epoch := a.leaseEpoch
	a.mu.Unlock()
	now := a.now()
	if !permission || !expires.After(now) || expires.Sub(now) > time.Hour {
		return ErrInvalid
	}
	v, err := a.ReadInteractive(ctx, h)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := ctx.Err(); err != nil {
		wipe(v)
		return err
	}
	if epoch != a.leaseEpoch {
		wipe(v)
		return ErrInteractionRequired
	}
	if a.revoked[h] {
		wipe(v)
		return ErrRevoked
	}
	if !expires.After(a.now()) {
		wipe(v)
		return ErrInteractionRequired
	}
	for key, l := range a.leases {
		if !l.expires.After(a.now()) {
			wipe(l.value)
			delete(a.leases, key)
		}
	}
	if _, exists := a.leases[h]; !exists && len(a.leases) >= 128 {
		wipe(v)
		return ErrBusy
	}
	wipe(a.leases[h].value)
	a.leases[h] = lease{v, expires}
	return nil
}

// ReadUnattended NEVER calls Backend, even after an earlier interactive read.
func (a *Adapter) ReadUnattended(ctx context.Context, h Handle) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if h.Validate() != nil {
		return nil, ErrInvalid
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.revoked[h] {
		return nil, ErrRevoked
	}
	l, ok := a.leases[h]
	if !ok || !l.expires.After(a.now()) {
		wipe(l.value)
		delete(a.leases, h)
		return nil, ErrInteractionRequired
	}
	return append([]byte(nil), l.value...), nil
}

// Revoke immediately invalidates memory access, including pending read results.
// It does not delete the OS generation; Delete requires separate explicit scope.
func (a *Adapter) Revoke(h Handle) error {
	if h.Validate() != nil {
		return ErrInvalid
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.revoked[h] = true
	wipe(a.leases[h].value)
	delete(a.leases, h)
	return nil
}
func (a *Adapter) Delete(ctx context.Context, h Handle) error {
	if err := a.Revoke(h); err != nil {
		return err
	}
	_, err := a.call(ctx, func() ([]byte, error) { return nil, a.backend.Delete(h.key()) })
	return err
}

// CloseLeases erases session memory without touching the native store.
func (a *Adapter) CloseLeases() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.leaseEpoch++
	for h, l := range a.leases {
		wipe(l.value)
		delete(a.leases, h)
	}
}

// MemoryBackend is an isolated, concurrency-safe fake; values are always copied.
type MemoryBackend struct {
	mu     sync.Mutex
	values map[string][]byte
}

func NewMemoryBackend() *MemoryBackend { return &MemoryBackend{values: make(map[string][]byte)} }
func (m *MemoryBackend) Set(k string, v []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(v) > MaxValueBytes {
		return ErrInvalid
	}
	wipe(m.values[k])
	m.values[k] = append([]byte(nil), v...)
	return nil
}
func (m *MemoryBackend) Get(k string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.values[k]
	if !ok {
		return nil, ErrNotFound
	}
	return append([]byte(nil), v...), nil
}
func (m *MemoryBackend) Delete(k string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	wipe(m.values[k])
	delete(m.values, k)
	return nil
}
