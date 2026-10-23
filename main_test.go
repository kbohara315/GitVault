package main

import (
	"bytes"
	"testing"
)

type panicCredentialInput struct{}

func (panicCredentialInput) Read([]byte) (int, error) { panic("ordinary launch read helper input") }

func TestCredentialHelperDispatchIsIsolated(t *testing.T) {
	var stdout, stderr bytes.Buffer
	handled, code := dispatchCredentialHelper([]string{"ordinary-application-start"}, panicCredentialInput{}, &stdout, &stderr)
	if handled || code != 0 || stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatal("ordinary startup entered credential helper mode")
	}
	handled, code = dispatchCredentialHelper([]string{"--gitvault-credential-helper"}, panicCredentialInput{}, &stdout, &stderr)
	if !handled || code == 0 || stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatal("malformed helper invocation was not rejected silently before input")
	}
}
