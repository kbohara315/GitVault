package secrets

import (
	"errors"
	"io/fs"
	"strings"

	"github.com/egoist/mygo"
)

// All native adapters share one admission slot, even across namespaces.
var nativeGate = make(chan struct{}, 1)

type nativeBackend struct{ service mygo.SecretsModule }

// NewNative requires an explicit application namespace and separates development
// and production. Construction alone does not access the native store.
func NewNative(namespace string, development bool) (*Adapter, error) {
	if namespace == "" || len(namespace) > 240 || strings.ContainsAny(namespace, "\x00\r\n") {
		return nil, ErrInvalid
	}
	suffix := ".production"
	if development {
		suffix = ".development"
	}
	a := New(nativeBackend{mygo.Secrets.Service(namespace + suffix)})
	a.gate = nativeGate
	return a, nil
}
func nativeError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, mygo.ErrSecretNotFound) {
		return ErrNotFound
	}
	if errors.Is(err, errors.ErrUnsupported) {
		return ErrUnsupported
	}
	if errors.Is(err, fs.ErrPermission) {
		return ErrDenied
	}
	// MyGo v0.6.2 preserves these Security.framework statuses only in
	// formatted text. Unknown/localized errors remain unavailable, never
	// guessed as not-found and never exposed to callers.
	for _, code := range []string{"(OSStatus -128)", "(OSStatus -25293)", "(OSStatus -25308)"} {
		if strings.Contains(err.Error(), code) {
			return ErrDenied
		}
	}
	return ErrUnavailable
}
func (n nativeBackend) Set(k string, v []byte) error { return nativeError(n.service.Set(k, v)) }
func (n nativeBackend) Get(k string) ([]byte, error) {
	v, e := n.service.Get(k)
	return v, nativeError(e)
}
func (n nativeBackend) Delete(k string) error { return nativeError(n.service.Delete(k)) }
