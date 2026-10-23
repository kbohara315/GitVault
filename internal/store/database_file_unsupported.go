//go:build !linux

package store

import (
	"errors"
	"os"
)

func validateDatabaseDirectory(string) error {
	return errors.New("database path trust is not qualified on this platform")
}
func openDatabaseFile(string) (*os.File, error) {
	return nil, errors.New("no-follow database access is not qualified on this platform")
}
