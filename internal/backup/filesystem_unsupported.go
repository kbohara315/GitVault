//go:build !linux

package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

type filesystemOps interface {
	publish(string, string) error
	writeSynced(string, []byte, os.FileMode) error
	syncDirectory(string) error
	makeTemp(string, string) (string, error)
	mkdir(string, os.FileMode) error
	removeAll(string) error
	copyBounded(context.Context, string, string, int64) error
	syncTree(string) error
}
type osFilesystem struct{}

func openRegularNoFollow(string) (*os.File, error) {
	return nil, errors.New("no-follow file access is not qualified on this platform")
}

func (osFilesystem) publish(string, string) error {
	return errors.New("no-replace publication is not qualified on this platform")
}
func (osFilesystem) writeSynced(p string, b []byte, m os.FileMode) error { return writeSynced(p, b, m) }
func (osFilesystem) syncDirectory(string) error {
	return errors.New("directory sync is not qualified on this platform")
}
func (osFilesystem) makeTemp(d, p string) (string, error) { return os.MkdirTemp(d, p) }
func (osFilesystem) mkdir(p string, m os.FileMode) error  { return os.Mkdir(p, m) }
func (osFilesystem) removeAll(p string) error             { return os.RemoveAll(p) }
func (osFilesystem) copyBounded(c context.Context, s, d string, m int64) error {
	return copyBounded(c, s, d, m)
}
func (osFilesystem) syncTree(string) error {
	return errors.New("tree sync is not qualified on this platform")
}
func canonicalDirectory(path string, create bool) (string, error) {
	if !filepath.IsAbs(path) || strings.ContainsAny(path, "\x00\r\n") {
		return "", ErrUnsafeRepository
	}
	p := filepath.Clean(path)
	if create {
		if err := os.MkdirAll(p, 0700); err != nil {
			return "", err
		}
	}
	r, e := filepath.EvalSymlinks(p)
	if e != nil || r != p {
		return "", ErrUnsafeRepository
	}
	s, e := os.Lstat(p)
	if e != nil || !s.IsDir() || s.Mode()&os.ModeSymlink != 0 {
		return "", ErrUnsafeRepository
	}
	return p, nil
}
func validateDirectoryTrust(string) error {
	return errors.New("directory trust policy is not qualified on this platform")
}
func (e *Engine) lock(ctx context.Context, exclusive bool) (func(), error) {
	return nil, errors.New("root locking is not qualified on this platform")
}
func (e *Engine) authorizeManagedMutation() error {
	return errors.New("managed mutation is not qualified on this platform")
}
func (e *Engine) ensureManagedDirectories(provider string) error {
	return errors.New("managed writes are not qualified on this platform")
}
func (e *Engine) ensureSnapshotParent(provider, repoID string) error {
	return errors.New("managed writes are not qualified on this platform")
}
func writeSynced(p string, b []byte, m os.FileMode) error {
	f, e := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, m)
	if e != nil {
		return e
	}
	_, e = f.Write(b)
	if e == nil {
		e = f.Sync()
	}
	c := f.Close()
	if e != nil {
		return e
	}
	return c
}
func syncDirectory(p string) error {
	return errors.New("directory sync is not qualified on this platform")
}
