package secrets

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

type failingBackend struct{ err error }

func (b failingBackend) Get(string) ([]byte, error) { return nil, b.err }
func (b failingBackend) Set(string, []byte) error   { return b.err }
func (b failingBackend) Delete(string) error        { return b.err }
func TestSecretAdapterOutcomes(t *testing.T) {
	ctx := context.Background()
	h := Handle{"synthetic", 1}
	for _, e := range []error{ErrNotFound, ErrUnsupported, ErrDenied, ErrUnavailable} {
		a := New(failingBackend{e})
		if _, got := a.ReadInteractive(ctx, h); !errors.Is(got, e) {
			t.Fatalf("want %v got %v", e, got)
		}
	}
	a := New(failingBackend{errors.New("synthetic-credential-do-not-expose")})
	if _, e := a.ReadInteractive(ctx, h); e != ErrUnavailable {
		t.Fatal(e)
	}
	a = New(NewMemoryBackend())
	v := []byte("synthetic-only")
	if e := a.Store(ctx, h, v); e != nil {
		t.Fatal(e)
	}
	v[0] = 'x'
	got, e := a.ReadInteractive(ctx, h)
	if e != nil || string(got) != "synthetic-only" {
		t.Fatalf("roundtrip: %v", e)
	}
	got[0] = 'x'
	got, _ = a.ReadInteractive(ctx, h)
	if string(got) != "synthetic-only" {
		t.Fatal("mutable backend alias")
	}
	if e = a.Store(ctx, h, []byte("replacement")); e != ErrExists {
		t.Fatal(e)
	}
	if e = a.Delete(ctx, h); e != nil {
		t.Fatal(e)
	}
	if _, e = a.ReadInteractive(ctx, h); e != ErrRevoked {
		t.Fatal(e)
	}
	if _, e := NewNative("", false); e != ErrInvalid {
		t.Fatal("empty native namespace allowed")
	}
	if nativeError(errors.New("GDBus.Error:org.freedesktop.Secret.Error.IsLocked: synthetic")) != ErrDenied {
		t.Fatal("canonical Secret Service locked outcome was not classified as denied")
	}
	if nativeError(errors.New("localized backend diagnostic")) != ErrUnavailable {
		t.Fatal("unknown native diagnostic was exposed or guessed as denied")
	}
}

type blockingBackend struct {
	started chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (b *blockingBackend) Get(string) ([]byte, error) {
	b.calls.Add(1)
	close(b.started)
	<-b.release
	return []byte("late-synthetic"), nil
}
func (b *blockingBackend) Set(string, []byte) error { return nil }
func (b *blockingBackend) Delete(string) error      { return nil }
func TestSecretPromptBoundedLifetime(t *testing.T) {
	b := &blockingBackend{started: make(chan struct{}), release: make(chan struct{})}
	a := New(b)
	h := Handle{"synthetic", 1}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.GrantLease(ctx, h, true, time.Now().Add(time.Minute)) }()
	<-b.started
	cancel()
	if e := <-done; !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	if !a.Pending() {
		t.Fatal("timed out native call no longer pending")
	}
	for i := 0; i < 100; i++ {
		if _, e := a.ReadInteractive(context.Background(), h); e != ErrBusy {
			t.Fatal(e)
		}
	}
	if b.calls.Load() != 1 {
		t.Fatal("unbounded native calls")
	}
	if _, e := a.ReadUnattended(context.Background(), h); e != ErrInteractionRequired {
		t.Fatal(e)
	}
	close(b.release)
	deadline := time.Now().Add(time.Second)
	for a.Pending() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if a.Pending() {
		t.Fatal("completed prompt still pending")
	}
	if _, e := a.ReadUnattended(context.Background(), h); e != ErrInteractionRequired {
		t.Fatal("late prompt established lease", e)
	}
}
func TestSecretRotationLeaseAndBounds(t *testing.T) {
	ctx := context.Background()
	a := New(NewMemoryBackend())
	now := time.Now()
	a.now = func() time.Time { return now }
	h1 := Handle{"rotation", 1}
	h2 := Handle{"rotation", 2}
	for h, v := range map[Handle]string{h1: "old-synthetic", h2: "new-synthetic"} {
		if e := a.Store(ctx, h, []byte(v)); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := a.ReadUnattended(ctx, h1); e != ErrInteractionRequired {
		t.Fatal(e)
	}
	if e := a.GrantLease(ctx, h1, false, now.Add(time.Minute)); e != ErrInvalid {
		t.Fatal(e)
	}
	if e := a.GrantLease(ctx, h1, true, now.Add(2*time.Hour)); e != ErrInvalid {
		t.Fatal(e)
	}
	if e := a.GrantLease(ctx, h1, true, now.Add(time.Minute)); e != nil {
		t.Fatal(e)
	}
	v, e := a.ReadUnattended(ctx, h1)
	if e != nil || string(v) != "old-synthetic" {
		t.Fatal(e)
	}
	v[0] = 'x'
	v, _ = a.ReadUnattended(ctx, h1)
	if string(v) != "old-synthetic" {
		t.Fatal("lease alias")
	}
	if _, e := a.ReadUnattended(ctx, h2); e != ErrInteractionRequired {
		t.Fatal("rotation switched identity")
	}
	now = now.Add(time.Minute)
	if _, e := a.ReadUnattended(ctx, h1); e != ErrInteractionRequired {
		t.Fatal("expired lease used")
	}
	if e := a.GrantLease(ctx, h2, true, now.Add(time.Minute)); e != nil {
		t.Fatal(e)
	}
	a.Revoke(h2)
	if _, e := a.ReadUnattended(ctx, h2); e != ErrRevoked {
		t.Fatal(e)
	}
	for _, h := range []Handle{{"", 1}, {"bad/key", 1}, {"ok", 0}, {"ok", 2147483648}} {
		if e := a.Store(ctx, h, []byte("x")); e != ErrInvalid {
			t.Fatal(e)
		}
	}
	h := Handle{"bounds", 1}
	if e := a.Store(ctx, h, bytes.Repeat([]byte("x"), MaxValueBytes+1)); e != ErrInvalid {
		t.Fatal(e)
	}
	if e := a.Store(ctx, h, bytes.Repeat([]byte("x"), MaxValueBytes)); e != nil {
		t.Fatal(e)
	}
	a.CloseLeases()
	if _, e := a.ReadUnattended(ctx, h); e != ErrInteractionRequired {
		t.Fatal(e)
	}
}

func TestSecretRevokesPendingLease(t *testing.T) {
	for _, closeSession := range []bool{false, true} {
		t.Run(fmt.Sprint(closeSession), func(t *testing.T) {
			b := &blockingBackend{started: make(chan struct{}), release: make(chan struct{})}
			a := New(b)
			h := Handle{"pending", 1}
			done := make(chan error, 1)
			go func() { done <- a.GrantLease(context.Background(), h, true, time.Now().Add(time.Minute)) }()
			<-b.started
			if closeSession {
				a.CloseLeases()
			} else {
				a.Revoke(h)
			}
			close(b.release)
			e := <-done
			if closeSession && e != ErrInteractionRequired || !closeSession && e != ErrRevoked {
				t.Fatal("stale lease accepted", e)
			}
			_, e = a.ReadUnattended(context.Background(), h)
			if closeSession && e != ErrInteractionRequired || !closeSession && e != ErrRevoked {
				t.Fatal(e)
			}
		})
	}
	// Even a backend that would fail if invoked is never touched unattended.
	a := New(forbiddenBackend{t})
	if _, e := a.ReadUnattended(context.Background(), Handle{"never-prompt", 1}); e != ErrInteractionRequired {
		t.Fatal(e)
	}
}

type forbiddenBackend struct{ t *testing.T }

func (b forbiddenBackend) Get(string) ([]byte, error) {
	b.t.Error("unattended backend access")
	return nil, ErrDenied
}
func (b forbiddenBackend) Set(string, []byte) error {
	b.t.Error("unattended backend access")
	return ErrDenied
}
func (b forbiddenBackend) Delete(string) error {
	b.t.Error("unattended backend access")
	return ErrDenied
}
