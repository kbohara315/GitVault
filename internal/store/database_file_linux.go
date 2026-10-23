//go:build linux

package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func validateDatabaseDirectory(path string) error {
	fd, err := openTrustedDirectory(path)
	if err != nil {
		return err
	}
	return unix.Close(fd)
}

func openDatabaseFile(path string) (*os.File, error) {
	dirFD, err := openTrustedDirectory(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer unix.Close(dirFD)
	name := filepath.Base(path)
	fd, err := unix.Openat(dirFD, name, unix.O_CREAT|unix.O_EXCL|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if errors.Is(err, unix.EEXIST) {
		fd, err = unix.Openat(dirFD, name, unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	}
	if err != nil {
		return nil, fmt.Errorf("open database without following symlinks: %w", err)
	}
	var st unix.Stat_t
	if err = unix.Fstat(fd, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG {
		_ = unix.Close(fd)
		if err != nil {
			return nil, err
		}
		return nil, errors.New("database must be a regular non-symlink file")
	}
	if err = unix.Fchmod(fd, 0600); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

func openTrustedDirectory(path string) (int, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return -1, errors.New("database directory must be canonical and absolute")
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	components := splitPath(path)
	if len(components) == 0 {
		_ = unix.Close(fd)
		return -1, errors.New("filesystem root cannot hold the application database")
	}
	for i, part := range components {
		next, openErr := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if openErr != nil {
			_ = unix.Close(fd)
			return -1, openErr
		}
		var parent, child unix.Stat_t
		if err = unix.Fstat(fd, &parent); err == nil {
			err = unix.Fstat(next, &child)
		}
		if err != nil {
			_ = unix.Close(fd)
			_ = unix.Close(next)
			return -1, err
		}
		if child.Mode&unix.S_IFMT != unix.S_IFDIR {
			_ = unix.Close(fd)
			_ = unix.Close(next)
			return -1, errors.New("database path component is not a directory")
		}
		if i == len(components)-1 {
			if child.Uid != uint32(os.Geteuid()) || child.Mode&0022 != 0 {
				_ = unix.Close(fd)
				_ = unix.Close(next)
				return -1, errors.New("database directory must be user-owned and private")
			}
		}
		if parent.Mode&0022 != 0 && (parent.Mode&01000 == 0 || child.Uid != uint32(os.Geteuid()) || (parent.Uid != uint32(os.Geteuid()) && parent.Uid != 0)) {
			_ = unix.Close(fd)
			_ = unix.Close(next)
			return -1, errors.New("database path has a replaceable writable ancestor")
		}
		_ = unix.Close(fd)
		fd = next
	}
	return fd, nil
}

func splitPath(path string) []string {
	rel := filepath.ToSlash(filepath.Clean(path))[1:]
	if rel == "" {
		return nil
	}
	parts := []string{}
	for _, p := range filepathSlashSplit(rel) {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return parts
}

func filepathSlashSplit(path string) []string {
	var parts []string
	for path != "" {
		i := 0
		for i < len(path) && path[i] != '/' {
			i++
		}
		if i > 0 {
			parts = append(parts, path[:i])
		}
		if i == len(path) {
			break
		}
		path = path[i+1:]
	}
	return parts
}
