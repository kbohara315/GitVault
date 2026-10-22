//go:build !linux

package backup

import (
	"errors"
	"testing"
)

func TestGitUnsupportedHost(t *testing.T) {
	if _, err := NewGitRunner(t.TempDir(), DefaultGitRunnerOptions()); !errors.Is(err, ErrGitUnsupported) {
		t.Fatalf("unqualified host enabled subprocess writes: %v", err)
	}
}
