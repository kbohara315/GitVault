//go:build linux

package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
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

func (osFilesystem) publish(src, dst string) error {
	return unix.Renameat2(unix.AT_FDCWD, src, unix.AT_FDCWD, dst, unix.RENAME_NOREPLACE)
}

func (osFilesystem) writeSynced(p string, b []byte, m os.FileMode) error { return writeSynced(p, b, m) }
func (osFilesystem) syncDirectory(p string) error                        { return syncDirectory(p) }
func (osFilesystem) makeTemp(d, p string) (string, error)                { return os.MkdirTemp(d, p) }
func (osFilesystem) mkdir(p string, m os.FileMode) error                 { return os.Mkdir(p, m) }
func (osFilesystem) removeAll(p string) error                            { return os.RemoveAll(p) }
func (osFilesystem) copyBounded(c context.Context, s, d string, m int64) error {
	return copyBounded(c, s, d, m)
}
func (osFilesystem) syncTree(p string) error { return syncTree(p) }

func openRegularNoFollow(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = f.Close()
		if err != nil {
			return nil, err
		}
		return nil, ErrUnsafeRepository
	}
	return f, nil
}

func canonicalDirectory(path string, create bool) (string, error) {
	if !filepath.IsAbs(path) || strings.ContainsAny(path, "\x00\r\n") {
		return "", ErrUnsafeRepository
	}
	p := filepath.Clean(path)
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil || resolved != p {
		return "", ErrUnsafeRepository
	}
	st, err := os.Lstat(p)
	if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return "", ErrUnsafeRepository
	}
	return p, nil
}

// validateDirectoryTrust checks every ancestor because a writable ancestor can
// rename a non-writable child directory. Same-UID code execution is outside the
// desktop threat model. Sticky ancestors are accepted only when owned by this
// user or root, whose namespace authority is trusted by the OS.
func validateDirectoryTrust(path string) error {
	path = filepath.Clean(path)
	var target unix.Stat_t
	if err := unix.Lstat(path, &target); err != nil {
		return err
	}
	if target.Mode&unix.S_IFMT != unix.S_IFDIR || target.Uid != uint32(os.Geteuid()) || target.Mode&0022 != 0 {
		return ErrUnsafeRepository
	}
	childPath := path
	for parentPath := filepath.Dir(childPath); parentPath != childPath; parentPath = filepath.Dir(parentPath) {
		var parent, child unix.Stat_t
		if err := unix.Lstat(parentPath, &parent); err != nil {
			return err
		}
		if parent.Mode&unix.S_IFMT != unix.S_IFDIR {
			return ErrUnsafeRepository
		}
		if err := unix.Lstat(childPath, &child); err != nil {
			return err
		}
		if child.Mode&unix.S_IFMT != unix.S_IFDIR {
			return ErrUnsafeRepository
		}
		if parent.Mode&0022 != 0 {
			if parent.Mode&01000 == 0 || child.Uid != uint32(os.Geteuid()) || (parent.Uid != uint32(os.Geteuid()) && parent.Uid != 0) {
				return ErrUnsafeRepository
			}
		}
		childPath = parentPath
	}
	return nil
}

func (e *Engine) ensureManagedDirectories(provider string) error {
	for _, p := range []string{filepath.Join(e.root, "mirrors"), filepath.Join(e.root, "mirrors", provider), filepath.Join(e.root, "staging"), filepath.Join(e.root, "snapshots"), filepath.Join(e.root, "snapshots", provider)} {
		if err := ensurePrivateDir(p); err != nil {
			return err
		}
	}
	return nil
}
func (e *Engine) ensureSnapshotParent(provider, repoID string) error {
	for _, p := range []string{filepath.Join(e.root, "snapshots", provider), filepath.Join(e.root, "snapshots", provider, repoID)} {
		if err := ensurePrivateDir(p); err != nil {
			return err
		}
	}
	return nil
}
func ensurePrivateDir(p string) error {
	st, err := os.Lstat(p)
	if err == nil {
		if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 || st.Mode().Perm()&0077 != 0 || !ownedByCurrentUser(st) {
			return ErrUnsafeRepository
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	if err = os.Mkdir(p, 0700); err != nil && !os.IsExist(err) {
		return err
	}
	st, err = os.Lstat(p)
	if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 || st.Mode().Perm()&0077 != 0 || !ownedByCurrentUser(st) {
		return ErrUnsafeRepository
	}
	return nil
}

func ownedByCurrentUser(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Uid == uint32(os.Geteuid())
}

func (e *Engine) lock(ctx context.Context, exclusive bool) (func(), error) {
	path := filepath.Join(e.root, ".gitvault.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	for {
		op := unix.LOCK_SH | unix.LOCK_NB
		if exclusive {
			op = unix.LOCK_EX | unix.LOCK_NB
		}
		err = unix.Flock(int(f.Fd()), op)
		if err == nil {
			return func() { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN); _ = f.Close() }, nil
		}
		if err != unix.EWOULDBLOCK && err != unix.EAGAIN {
			f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

type rootMarker struct {
	Version         int    `json:"version"`
	RootID          string `json:"root_id"`
	InstallationID  string `json:"installation_id"`
	StoreID         string `json:"store_id"`
	ApplicationData string `json:"application_data"`
	Generation      int64  `json:"generation"`
}

func (e *Engine) authorizeManagedMutation() error {
	p := filepath.Join(e.root, ".gitvault-root.json")
	f, err := openRegularNoFollow(p)
	if os.IsNotExist(err) {
		if e.options.InstallationID != "" || e.options.StoreID != "" {
			return fmt.Errorf("managed root marker binding must be provisioned by coordinator")
		}
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Size() > 64<<10 {
		return fmt.Errorf("managed root marker exceeds limit")
	}
	b, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	if err != nil {
		return err
	}
	if len(b) > 64<<10 {
		return fmt.Errorf("managed root marker exceeds limit")
	}
	var m rootMarker
	if err = json.Unmarshal(b, &m); err != nil {
		return fmt.Errorf("invalid managed root marker: %w", err)
	}
	if m.Version != 1 || m.RootID == "" || m.Generation < 1 || e.options.InstallationID == "" || m.InstallationID != e.options.InstallationID || m.StoreID != e.options.StoreID || m.ApplicationData != e.options.ApplicationData {
		return fmt.Errorf("foreign managed root binding")
	}
	return nil
}

func writeSynced(path string, b []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, werr := f.Write(b)
	if werr == nil {
		werr = f.Sync()
	}
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	return cerr
}
func syncDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

var _ = context.Canceled
