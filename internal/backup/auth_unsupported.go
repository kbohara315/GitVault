//go:build !linux

package backup

import (
	"context"
	"io"
)

// Windows requires authenticated native named pipes; other Unix peer-process
// mechanisms need native qualification. Neither falls back to a token/file/TCP.
func newCredentialBrokerPlatform(CredentialBrokerOptions) (credentialBrokerPlatform, error) {
	return nil, ErrCredentialBrokerUnsupported
}

func credentialHelperExchange(context.Context, string, int, string, string, io.Writer) error {
	return ErrCredentialBrokerUnsupported
}
