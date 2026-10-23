//go:build !linux

package app

import (
	"context"
	"errors"

	"gitvault/internal/store"
)

var ErrManagedPlatformUnsupported = errors.New("managed application coordination is not qualified on this platform")

const managedRootMarkerName = ".gitvault-root.json"

func BindManagedRoot(context.Context, *store.Store, string) (store.RootBinding, error) {
	return store.RootBinding{}, ErrManagedPlatformUnsupported
}

func validateManagedRootLocked(context.Context, *store.Store, string, string, managedRootMarker) (store.RootBinding, error) {
	return store.RootBinding{}, ErrManagedPlatformUnsupported
}

func canonicalManagedRoot(string) (string, string, error) {
	return "", "", ErrManagedPlatformUnsupported
}
func readManagedRootMarker(string) (managedRootMarker, error) {
	return managedRootMarker{}, ErrManagedPlatformUnsupported
}

func lockManagedRoot(context.Context, string) (func(), error) {
	return nil, ErrManagedPlatformUnsupported
}
func lockCoordinatorRun(context.Context, string) (func(), error) {
	return nil, ErrManagedPlatformUnsupported
}

type managedRootMarker struct{}
