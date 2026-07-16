//go:build darwin || linux

package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

var errUnsafePath = errors.New("unsafe config path")

func openConfigFile(path string) (*os.File, os.FileInfo, UnsafeReason, error) {
	directory, name, unsafe, err := walkConfigParent(path, false)
	if err != nil {
		return nil, nil, unsafe, err
	}
	defer func() { _ = unix.Close(directory) }()
	fd, err := unix.Openat(directory, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		unsafe, classified := classifyOpenError(err)
		return nil, nil, unsafe, classified
	}
	file := os.NewFile(uintptr(fd), "mattermost-cli-config")
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = file.Close()
		return nil, nil, "", err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, "", err
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, nil, UnsafeType, errUnsafePath
	}
	if stat.Uid != uint32(os.Geteuid()) {
		_ = file.Close()
		return nil, nil, UnsafeOwnership, errUnsafePath
	}
	return file, info, "", nil
}

func createConfigFile(path string) (*os.File, error) {
	directory, name, _, err := walkConfigParent(path, true)
	if err != nil {
		return nil, err
	}
	defer func() { _ = unix.Close(directory) }()
	fd, err := unix.Openat(directory, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), "mattermost-cli-config"), nil
}

func walkConfigParent(path string, create bool) (int, string, UnsafeReason, error) {
	if !filepath.IsAbs(path) {
		return -1, "", UnsafeType, errUnsafePath
	}
	clean := filepath.Clean(path)
	parts := strings.Split(strings.TrimPrefix(clean, string(filepath.Separator)), string(filepath.Separator))
	if len(parts) == 0 || parts[len(parts)-1] == "" || parts[len(parts)-1] == "." {
		return -1, "", UnsafeType, errUnsafePath
	}
	current, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, "", "", err
	}
	boundary := false
	for _, part := range parts[:len(parts)-1] {
		next, nextBoundary, unsafe, err := openConfigDirectory(current, part, boundary, create)
		_ = unix.Close(current)
		if err != nil {
			return -1, "", unsafe, err
		}
		current = next
		boundary = nextBoundary
	}
	return current, parts[len(parts)-1], "", nil
}

func openConfigDirectory(parent int, name string, boundary, create bool) (int, bool, UnsafeReason, error) {
	var entry unix.Stat_t
	err := unix.Fstatat(parent, name, &entry, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) && create {
		if err := unix.Mkdirat(parent, name, 0o700); err != nil && !errors.Is(err, unix.EEXIST) {
			return -1, boundary, "", err
		}
		err = unix.Fstatat(parent, name, &entry, unix.AT_SYMLINK_NOFOLLOW)
	}
	if err != nil {
		return -1, boundary, "", err
	}
	isSymlink := entry.Mode&unix.S_IFMT == unix.S_IFLNK
	currentUser := uint32(os.Geteuid())
	if isSymlink && (boundary || (currentUser != 0 && entry.Uid == currentUser)) {
		return -1, boundary, UnsafeType, errUnsafePath
	}
	flags := unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC
	if !isSymlink {
		flags |= unix.O_NOFOLLOW
	}
	fd, err := unix.Openat(parent, name, flags, 0)
	if err != nil {
		unsafe, classified := classifyOpenError(err)
		return -1, boundary, unsafe, classified
	}
	var opened unix.Stat_t
	if err := unix.Fstat(fd, &opened); err != nil {
		_ = unix.Close(fd)
		return -1, boundary, "", err
	}
	if opened.Mode&unix.S_IFMT != unix.S_IFDIR {
		_ = unix.Close(fd)
		return -1, boundary, UnsafeType, errUnsafePath
	}
	if opened.Uid != 0 && opened.Uid != currentUser {
		_ = unix.Close(fd)
		return -1, boundary, UnsafeOwnership, errUnsafePath
	}
	nextBoundary := boundary || (opened.Uid == currentUser && (currentUser != 0 || opened.Mode&0o077 == 0))
	if nextBoundary && opened.Mode&0o022 != 0 {
		_ = unix.Close(fd)
		return -1, boundary, UnsafeOwnership, errUnsafePath
	}
	return fd, nextBoundary, "", nil
}

func classifyOpenError(err error) (UnsafeReason, error) {
	if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
		return UnsafeType, errUnsafePath
	}
	return "", err
}
