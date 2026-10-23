//go:build linux

package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"gitvault/internal/backup"
	"gitvault/internal/store"
	"golang.org/x/sys/unix"
)

const (
	managedRootMarkerName = ".gitvault-root.json"
	managedRunLockName    = ".gitvault-coordinator.lock"
	managedLockName       = ".gitvault.lock"
	managedMarkerLimit    = 64 << 10
)

type managedRootMarker struct {
	Version         int    `json:"version"`
	RootID          string `json:"root_id"`
	InstallationID  string `json:"installation_id"`
	StoreID         string `json:"store_id"`
	ApplicationData string `json:"application_data"`
	Generation      int64  `json:"generation"`
}

// BindManagedRoot explicitly binds a trusted local directory to this store.
// Existing markers are never adopted by another database or installation.
func BindManagedRoot(ctx context.Context, s *store.Store, rootPath string) (store.RootBinding, error) {
	if s == nil {
		return store.RootBinding{}, errors.New("store required")
	}
	root, fsID, err := canonicalManagedRoot(rootPath)
	if err != nil {
		return store.RootBinding{}, err
	}
	releaseRun, err := lockCoordinatorRun(ctx, root)
	if err != nil {
		return store.RootBinding{}, err
	}
	defer releaseRun()
	releaseRoot, err := lockManagedRoot(ctx, root)
	if err != nil {
		return store.RootBinding{}, err
	}
	defer releaseRoot()
	lockedRoot, lockedFSID, err := canonicalManagedRoot(root)
	if err != nil || lockedRoot != root || lockedFSID != fsID {
		return store.RootBinding{}, fmt.Errorf("%w: managed root identity changed while locking", store.ErrForeignRoot)
	}

	markerPath := filepath.Join(root, managedRootMarkerName)
	marker, err := readManagedRootMarker(markerPath)
	if err == nil {
		binding, checkErr := validateManagedRootLocked(ctx, s, root, fsID, marker)
		if checkErr != nil {
			return store.RootBinding{}, checkErr
		}
		if checkErr = syncManagedRootDirectory(root); checkErr != nil {
			return store.RootBinding{}, checkErr
		}
		return binding, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return store.RootBinding{}, err
	}
	managedData, dataErr := managedDataExists(root)
	if dataErr != nil {
		return store.RootBinding{}, dataErr
	}
	if managedData {
		return store.RootBinding{}, fmt.Errorf("%w: managed data exists without its authority marker", store.ErrForeignRoot)
	}

	identity := s.Identity()
	binding, activeErr := s.ActiveRoot(ctx)
	if activeErr == nil && binding.Path == root && binding.FilesystemID == fsID && binding.StoreID == identity.StoreID && binding.InstallationID == identity.InstallationID && binding.DataDir == identity.DataDir {
		// Complete a prior bind whose database commit succeeded before marker
		// publication. This is not adoption: the same database owns this exact
		// canonical path and filesystem identity already.
	} else {
		if activeErr != nil && !errors.Is(activeErr, store.ErrNotFound) {
			return store.RootBinding{}, activeErr
		}
		id, idErr := backup.NewID()
		if idErr != nil {
			return store.RootBinding{}, idErr
		}
		binding = store.RootBinding{ID: id, Path: root, FilesystemID: fsID, StoreID: identity.StoreID, InstallationID: identity.InstallationID, DataDir: identity.DataDir, Generation: 1, Active: true}
		if err = s.BindRoot(ctx, binding); err != nil {
			return store.RootBinding{}, err
		}
	}
	marker = managedRootMarker{Version: 1, RootID: binding.ID, InstallationID: identity.InstallationID, StoreID: identity.StoreID, ApplicationData: identity.DataDir, Generation: binding.Generation}
	if err = publishManagedRootMarker(root, marker); err != nil {
		return store.RootBinding{}, err
	}
	return binding, nil
}

func validateManagedRootLocked(ctx context.Context, s *store.Store, root, fsID string, marker managedRootMarker) (store.RootBinding, error) {
	identity := s.Identity()
	if marker.Version != 1 || marker.RootID == "" || marker.Generation < 1 || marker.InstallationID != identity.InstallationID || marker.StoreID != identity.StoreID || marker.ApplicationData != identity.DataDir {
		return store.RootBinding{}, fmt.Errorf("%w: marker identity differs", store.ErrForeignRoot)
	}
	binding, err := s.RootBinding(ctx, marker.RootID)
	if err != nil {
		return store.RootBinding{}, fmt.Errorf("%w: store binding is missing or inactive", store.ErrForeignRoot)
	}
	if binding.Path != root || binding.FilesystemID != fsID || binding.StoreID != identity.StoreID || binding.InstallationID != identity.InstallationID || binding.DataDir != identity.DataDir || binding.Generation != marker.Generation {
		return store.RootBinding{}, fmt.Errorf("%w: canonical root binding differs", store.ErrForeignRoot)
	}
	return binding, nil
}

func readManagedRootMarker(path string) (managedRootMarker, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return managedRootMarker{}, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return managedRootMarker{}, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || !ownedByCurrentUser(info) || info.Size() > managedMarkerLimit {
		return managedRootMarker{}, errors.New("managed root marker is unsafe or exceeds its limit")
	}
	b, err := io.ReadAll(io.LimitReader(f, managedMarkerLimit+1))
	if err != nil {
		return managedRootMarker{}, err
	}
	if len(b) > managedMarkerLimit {
		return managedRootMarker{}, errors.New("managed root marker exceeds its limit")
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	var marker managedRootMarker
	if err = dec.Decode(&marker); err != nil {
		return managedRootMarker{}, fmt.Errorf("invalid managed root marker: %w", err)
	}
	if err = dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		return managedRootMarker{}, errors.New("managed root marker has trailing data")
	}
	return marker, nil
}

func publishManagedRootMarker(root string, marker managedRootMarker) error {
	b, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	id, err := backup.NewID()
	if err != nil {
		return err
	}
	tmp := filepath.Join(root, ".gitvault-root-"+id+".partial")
	fd, err := unix.Open(tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), tmp)
	ownedTemp := true
	defer func() {
		if ownedTemp {
			_ = os.Remove(tmp)
		}
	}()
	n, writeErr := f.Write(b)
	if writeErr == nil && n != len(b) {
		writeErr = io.ErrShortWrite
	}
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	if err = unix.Renameat2(unix.AT_FDCWD, tmp, unix.AT_FDCWD, filepath.Join(root, managedRootMarkerName), unix.RENAME_NOREPLACE); err != nil {
		return err
	}
	ownedTemp = false // publication is the authority marker's visibility commit.
	return syncManagedRootDirectory(root)
}

func syncManagedRootDirectory(root string) error {
	dir, err := os.Open(root)
	if err != nil {
		return err
	}
	err = dir.Sync()
	closeErr := dir.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func managedDataExists(root string) (bool, error) {
	remaining := 10000
	rootEntries, err := readManagedDirectory(root, &remaining)
	if err != nil {
		return false, err
	}
	for _, entry := range rootEntries {
		if strings.HasPrefix(entry.Name(), ".gitvault-root-") && strings.HasSuffix(entry.Name(), ".partial") {
			return true, nil
		}
	}
	for _, name := range []string{"mirrors", "staging", "snapshots"} {
		path := filepath.Join(root, name)
		info, statErr := os.Lstat(path)
		if os.IsNotExist(statErr) {
			continue
		}
		if statErr != nil {
			return false, statErr
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return true, nil
		}
		entries, readErr := readManagedDirectory(path, &remaining)
		if readErr != nil {
			return false, readErr
		}
		if len(entries) != 0 {
			return true, nil
		}
	}
	return false, nil
}

func canonicalManagedRoot(path string) (string, string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, "\x00\r\n") {
		return "", "", errors.New("managed root must be a clean absolute path")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return "", "", errors.New("managed root must not use symlink aliases")
	}
	if err = validateManagedRootTrust(path); err != nil {
		return "", "", err
	}
	var st unix.Stat_t
	if err = unix.Stat(path, &st); err != nil {
		return "", "", err
	}
	return path, fmt.Sprintf("dev:%d:ino:%d", uint64(st.Dev), st.Ino), nil
}

func validateManagedRootTrust(path string) error {
	var target unix.Stat_t
	if err := unix.Lstat(path, &target); err != nil {
		return err
	}
	if target.Mode&unix.S_IFMT != unix.S_IFDIR || target.Uid != uint32(os.Geteuid()) || target.Mode&0022 != 0 {
		return errors.New("managed root ownership or permissions are unsafe")
	}
	childPath := path
	for parentPath := filepath.Dir(childPath); parentPath != childPath; parentPath = filepath.Dir(parentPath) {
		var parent, child unix.Stat_t
		if err := unix.Lstat(parentPath, &parent); err != nil {
			return err
		}
		if parent.Mode&unix.S_IFMT != unix.S_IFDIR {
			return errors.New("managed root ancestor is not a directory")
		}
		if err := unix.Lstat(childPath, &child); err != nil {
			return err
		}
		if child.Mode&unix.S_IFMT != unix.S_IFDIR {
			return errors.New("managed root path contains a non-directory ancestor")
		}
		if parent.Mode&0022 != 0 && (parent.Mode&01000 == 0 || child.Uid != uint32(os.Geteuid()) || (parent.Uid != uint32(os.Geteuid()) && parent.Uid != 0)) {
			return errors.New("managed root has a replaceable ancestor")
		}
		childPath = parentPath
	}
	return nil
}

func lockManagedRoot(ctx context.Context, root string) (func(), error) {
	return lockFile(ctx, filepath.Join(root, managedLockName))
}

func lockCoordinatorRun(ctx context.Context, root string) (func(), error) {
	return lockFile(ctx, filepath.Join(root, managedRunLockName))
}

func lockFile(ctx context.Context, path string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || !ownedByCurrentUser(info) {
		_ = f.Close()
		if err != nil {
			return nil, err
		}
		return nil, errors.New("managed lock file is unsafe")
	}
	if err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		if err == unix.EWOULDBLOCK || err == unix.EAGAIN {
			return nil, &store.BusyError{}
		}
		return nil, err
	}
	return func() { _ = unix.Flock(fd, unix.LOCK_UN); _ = f.Close() }, nil
}

func ownedByCurrentUser(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Uid == uint32(os.Geteuid())
}
