//go:build !linux

package backup

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"
)

func TestCredentialBrokerUnsupported(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal("test executable unavailable")
	}
	b, err := NewCredentialBroker(CredentialBrokerOptions{Context: context.Background(), PrivateDir: t.TempDir(), AllowedHost: "github.com", AllowedRepository: "fixture/private", Username: "x-access-token", Secret: []byte("synthetic-only"), Executable: exe})
	if b != nil || !errors.Is(err, ErrCredentialBrokerUnsupported) {
		t.Fatal("unqualified transport did not fail closed")
	}
	var out bytes.Buffer
	if !errors.Is(credentialHelperExchange(context.Background(), "unused", 1, "github.com", "fixture/private.git", &out), ErrCredentialBrokerUnsupported) || out.Len() != 0 {
		t.Fatal("unsupported helper transport produced credential output")
	}
}
