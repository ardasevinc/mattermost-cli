//go:build darwin || linux

package stagestore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const (
	bootstrapLockFilename    = ".stages.bootstrap.lock"
	bootstrapPendingFilename = ".stages.bootstrap.pending"
	bootstrapPendingContent  = "mattermost-cli-v2-bootstrap\n"
)

var filesystemAllowed = localFilesystemAllowed
var stateDirectoryFchmodat = unix.Fchmodat
var pendingWrite = unix.Write

func prepareWritable(ctx context.Context, path string) (bool, bool, func(), error) {
	dirfd, name, err := walkParent(path, true)
	if err != nil {
		return false, false, nil, err
	}
	defer unix.Close(dirfd)
	if !filesystemAllowed(dirfd) {
		return false, false, nil, ErrUnsafeFilesystem
	}
	lockCreated := true
	lockfd, err := unix.Openat(dirfd, bootstrapLockFilename, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if errors.Is(err, unix.EEXIST) {
		lockCreated = false
		if recoverErr := recoverPrivateRegularAt(dirfd, bootstrapLockFilename, 0o600); recoverErr != nil {
			return false, false, nil, recoverErr
		}
		lockfd, err = unix.Openat(dirfd, bootstrapLockFilename, unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	}
	if err != nil {
		return false, false, nil, errors.New("stage store: bootstrap lock unavailable")
	}
	if lockCreated {
		if err := unix.Fchmod(lockfd, 0o600); err != nil {
			unix.Close(lockfd)
			return false, false, nil, errors.New("stage store: cannot secure bootstrap lock")
		}
	}
	if err := validateFD(lockfd, 0o600); err != nil {
		unix.Close(lockfd)
		return false, false, nil, err
	}
	if err := flockContext(ctx, lockfd); err != nil {
		unix.Close(lockfd)
		return false, false, nil, err
	}
	databaseExists, err := entryExistsAt(dirfd, name)
	if err != nil {
		unix.Close(lockfd)
		return false, false, nil, err
	}
	pendingExists, err := entryExistsAt(dirfd, bootstrapPendingFilename)
	if err != nil {
		unix.Close(lockfd)
		return false, false, nil, err
	}
	if pendingExists {
		if err := validatePendingAt(dirfd); err != nil {
			if databaseExists {
				unix.Close(lockfd)
				return false, false, nil, err
			}
			if unlinkErr := unix.Unlinkat(dirfd, bootstrapPendingFilename, 0); unlinkErr != nil {
				unix.Close(lockfd)
				return false, false, nil, errors.New("stage store: cannot replace bootstrap marker")
			}
			if syncErr := unix.Fsync(dirfd); syncErr != nil {
				unix.Close(lockfd)
				return false, false, nil, errors.New("stage store: cannot sync state directory")
			}
			pendingExists = false
		}
	}
	if !databaseExists && !pendingExists {
		if err := createPendingAt(dirfd); err != nil {
			unix.Close(lockfd)
			return false, false, nil, err
		}
		pendingExists = true
	}
	createdNow := false
	if !databaseExists {
		dbfd, err := unix.Openat(dirfd, name, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
		if err != nil {
			unix.Close(lockfd)
			return false, false, nil, errors.New("stage store: cannot create database")
		}
		createdNow = true
		if err := unix.Fchmod(dbfd, 0o600); err != nil {
			unix.Close(dbfd)
			unix.Close(lockfd)
			return false, false, nil, errors.New("stage store: cannot secure database")
		}
		unix.Close(dbfd)
	} else if pendingExists {
		if err := recoverPrivateRegularAt(dirfd, name, 0o600); err != nil {
			unix.Close(lockfd)
			return false, false, nil, err
		}
	}
	if err := validateAt(dirfd, name, 0o600); err != nil {
		unix.Close(lockfd)
		return false, false, nil, err
	}
	if err := validateSidecarsAt(dirfd, name); err != nil {
		unix.Close(lockfd)
		return false, false, nil, err
	}
	return pendingExists, createdNow, func() { _ = unix.Flock(lockfd, unix.LOCK_UN); _ = unix.Close(lockfd) }, nil
}

func entryExistsAt(dirfd int, name string) (bool, error) {
	var stat unix.Stat_t
	err := unix.Fstatat(dirfd, name, &stat, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return false, nil
	}
	if err != nil {
		return false, errors.New("stage store: cannot inspect private state")
	}
	return true, nil
}

func createPendingAt(dirfd int) error {
	fd, err := unix.Openat(dirfd, bootstrapPendingFilename, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return errors.New("stage store: cannot create bootstrap marker")
	}
	succeeded := false
	defer func() {
		_ = unix.Close(fd)
		if !succeeded {
			_ = unix.Unlinkat(dirfd, bootstrapPendingFilename, 0)
			_ = unix.Fsync(dirfd)
		}
	}()
	if err := unix.Fchmod(fd, 0o600); err != nil {
		return errors.New("stage store: cannot secure bootstrap marker")
	}
	remaining := []byte(bootstrapPendingContent)
	for len(remaining) > 0 {
		n, err := pendingWrite(fd, remaining)
		if err != nil || n <= 0 || n > len(remaining) {
			return errors.New("stage store: cannot write bootstrap marker")
		}
		remaining = remaining[n:]
	}
	if err := unix.Fsync(fd); err != nil {
		return errors.New("stage store: cannot sync bootstrap marker")
	}
	if err := unix.Fsync(dirfd); err != nil {
		return errors.New("stage store: cannot sync state directory")
	}
	succeeded = true
	return nil
}

func validatePendingAt(dirfd int) error {
	fd, err := unix.Openat(dirfd, bootstrapPendingFilename, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return errors.New("stage store: bootstrap marker unavailable")
	}
	defer unix.Close(fd)
	if err := validateFD(fd, 0o600); err != nil {
		return err
	}
	buf := make([]byte, len(bootstrapPendingContent)+1)
	n, err := unix.Read(fd, buf)
	if err != nil || string(buf[:n]) != bootstrapPendingContent {
		return errors.New("stage store: invalid bootstrap marker")
	}
	return nil
}

func walkParent(path string, create bool) (int, string, error) {
	if !filepath.IsAbs(path) {
		return -1, "", errors.New("stage store: database path must be absolute")
	}
	parts := strings.Split(strings.TrimPrefix(filepath.Clean(path), string(filepath.Separator)), string(filepath.Separator))
	if len(parts) < 2 || parts[len(parts)-1] == "" {
		return -1, "", errors.New("stage store: unsafe state path")
	}
	fd, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, "", errors.New("stage store: cannot inspect state path")
	}
	boundary := false
	for i, part := range parts[:len(parts)-1] {
		var entry unix.Stat_t
		err = unix.Fstatat(fd, part, &entry, unix.AT_SYMLINK_NOFOLLOW)
		created := false
		if errors.Is(err, unix.ENOENT) && create {
			err = unix.Mkdirat(fd, part, 0o700)
			if errors.Is(err, unix.EACCES) {
				if recoverErr := recoverPrivateDirectoryFD(fd); recoverErr == nil {
					err = unix.Mkdirat(fd, part, 0o700)
				}
			}
			if err != nil && !errors.Is(err, unix.EEXIST) {
				unix.Close(fd)
				return -1, "", errors.New("stage store: cannot create state directory")
			}
			err = unix.Fstatat(fd, part, &entry, unix.AT_SYMLINK_NOFOLLOW)
			created = true
		}
		if err != nil {
			unix.Close(fd)
			return -1, "", errors.New("stage store: state directory unavailable")
		}
		if created {
			entry, err = secureCreatedDirectory(fd, part, entry, boundary)
			if err != nil {
				unix.Close(fd)
				return -1, "", err
			}
		} else if create && i == len(parts)-2 && entry.Mode&unix.S_IFMT == unix.S_IFDIR && entry.Uid == uint32(os.Geteuid()) {
			entry, err = recoverPrivateDirectoryAt(fd, part, entry)
			if err != nil {
				unix.Close(fd)
				return -1, "", err
			}
		}
		isLink := entry.Mode&unix.S_IFMT == unix.S_IFLNK
		if isLink && !symlinkAllowedBeforeBoundary(entry.Uid, boundary) {
			unix.Close(fd)
			return -1, "", errors.New("stage store: unsafe state path")
		}
		flags := unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC
		if !isLink {
			flags |= unix.O_NOFOLLOW
		}
		next, openErr := unix.Openat(fd, part, flags, 0)
		if create && errors.Is(openErr, unix.EACCES) && !isLink && entry.Mode&unix.S_IFMT == unix.S_IFDIR && entry.Uid == uint32(os.Geteuid()) {
			if recovered, recoverErr := recoverPrivateDirectoryAt(fd, part, entry); recoverErr == nil {
				entry = recovered
				next, openErr = unix.Openat(fd, part, flags, 0)
			}
		}
		unix.Close(fd)
		if openErr != nil {
			return -1, "", errors.New("stage store: unsafe state path")
		}
		fd = next
		var opened unix.Stat_t
		if unix.Fstat(fd, &opened) != nil || opened.Mode&unix.S_IFMT != unix.S_IFDIR || (opened.Uid != 0 && opened.Uid != uint32(os.Geteuid())) {
			unix.Close(fd)
			return -1, "", errors.New("stage store: unsafe state path")
		}
		if created {
			if err := unix.Fchmod(fd, 0o700); err != nil {
				unix.Close(fd)
				return -1, "", errors.New("stage store: cannot secure state directory")
			}
			if err := unix.Fstat(fd, &opened); err != nil {
				unix.Close(fd)
				return -1, "", errors.New("stage store: cannot inspect state directory")
			}
		}
		if !isLink && (entry.Dev != opened.Dev || entry.Ino != opened.Ino) {
			unix.Close(fd)
			return -1, "", errors.New("stage store: state path changed")
		}
		if !ancestorPermissionsAllowed(opened.Uid, uint32(opened.Mode), boundary) {
			unix.Close(fd)
			return -1, "", errors.New("stage store: unsafe state path permissions")
		}
		if opened.Uid == uint32(os.Geteuid()) {
			boundary = true
			if opened.Mode&0o022 != 0 {
				unix.Close(fd)
				return -1, "", errors.New("stage store: unsafe state path permissions")
			}
		}
		if created && !hasExactMode(uint32(opened.Mode), unix.S_IFDIR, 0o700) {
			unix.Close(fd)
			return -1, "", errors.New("stage store: unsupported permission semantics")
		}
		if i == len(parts)-2 && (opened.Uid != uint32(os.Geteuid()) || !hasExactMode(uint32(opened.Mode), unix.S_IFDIR, 0o700)) {
			unix.Close(fd)
			return -1, "", errors.New("stage store: unsafe state directory")
		}
	}
	return fd, parts[len(parts)-1], nil
}

func secureCreatedDirectory(parent int, name string, expected unix.Stat_t, trustedParent bool) (unix.Stat_t, error) {
	euid := uint32(os.Geteuid())
	if expected.Mode&unix.S_IFMT != unix.S_IFDIR || expected.Uid != euid {
		return unix.Stat_t{}, errors.New("stage store: state directory changed")
	}
	err := stateDirectoryFchmodat(parent, name, 0o700, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.ENOTSUP) {
		if !trustedParent {
			return unix.Stat_t{}, errors.New("stage store: unsupported permission semantics")
		}
		var before unix.Stat_t
		if statErr := unix.Fstatat(parent, name, &before, unix.AT_SYMLINK_NOFOLLOW); statErr != nil || !sameDirectory(expected, before, euid) {
			return unix.Stat_t{}, errors.New("stage store: state directory changed")
		}
		err = stateDirectoryFchmodat(parent, name, 0o700, 0)
	}
	if err != nil {
		return unix.Stat_t{}, errors.New("stage store: cannot secure state directory")
	}
	var secured unix.Stat_t
	if err := unix.Fstatat(parent, name, &secured, unix.AT_SYMLINK_NOFOLLOW); err != nil || !sameDirectory(expected, secured, euid) || !hasExactMode(uint32(secured.Mode), unix.S_IFDIR, 0o700) {
		return unix.Stat_t{}, errors.New("stage store: state directory changed")
	}
	return secured, nil
}

func recoverPrivateDirectoryAt(parent int, name string, expected unix.Stat_t) (unix.Stat_t, error) {
	if hasExactMode(uint32(expected.Mode), unix.S_IFDIR, 0o700) {
		return expected, nil
	}
	if uint32(expected.Mode)&^uint32(unix.S_IFMT|0o700) != 0 {
		return unix.Stat_t{}, errors.New("stage store: unsafe state directory")
	}
	if err := unix.Fchmodat(parent, name, 0o700, 0); err != nil {
		return unix.Stat_t{}, errors.New("stage store: cannot recover state directory")
	}
	var recovered unix.Stat_t
	if err := unix.Fstatat(parent, name, &recovered, unix.AT_SYMLINK_NOFOLLOW); err != nil || !sameDirectory(expected, recovered, uint32(os.Geteuid())) || uint32(recovered.Mode)&0o777 != 0o700 {
		return unix.Stat_t{}, errors.New("stage store: state directory changed")
	}
	return recovered, nil
}

func recoverPrivateDirectoryFD(fd int) error {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return errors.New("stage store: cannot inspect state directory")
	}
	mode := uint32(stat.Mode)
	if mode&unix.S_IFMT != unix.S_IFDIR || stat.Uid != uint32(os.Geteuid()) || mode&^uint32(unix.S_IFMT|0o700) != 0 {
		return errors.New("stage store: unsafe state directory")
	}
	if err := unix.Fchmod(fd, 0o700); err != nil {
		return errors.New("stage store: cannot recover state directory")
	}
	return nil
}

func sameDirectory(expected, actual unix.Stat_t, euid uint32) bool {
	return expected.Dev == actual.Dev && expected.Ino == actual.Ino && actual.Mode&unix.S_IFMT == unix.S_IFDIR && actual.Uid == euid
}

func hasExactMode(mode, kind, permissions uint32) bool {
	return mode&unix.S_IFMT == kind && mode&0o777 == permissions && mode&^uint32(unix.S_IFMT|permissions) == 0
}

func symlinkAllowedBeforeBoundary(uid uint32, boundary bool) bool {
	return !boundary && uid == 0
}

func ancestorPermissionsAllowed(uid, mode uint32, boundary bool) bool {
	if boundary || uid != 0 || mode&0o022 == 0 {
		return true
	}
	return mode&unix.S_ISVTX != 0
}

func flockContext(ctx context.Context, fd int) error {
	deadline := time.Now().Add(busyMillis * time.Millisecond)
	for {
		err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) || !time.Now().Before(deadline) {
			return ErrBusy
		}
		notifyFlockContention()
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func lockExisting(ctx context.Context, path string) (func(), error) {
	dirfd, _, err := walkParent(path, false)
	if err != nil {
		return nil, err
	}
	defer unix.Close(dirfd)
	lockfd, err := unix.Openat(dirfd, bootstrapLockFilename, unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, errors.New("stage store: bootstrap lock unavailable")
	}
	if err := validateFD(lockfd, 0o600); err != nil {
		unix.Close(lockfd)
		return nil, err
	}
	if err := flockContext(ctx, lockfd); err != nil {
		unix.Close(lockfd)
		return nil, err
	}
	return func() { _ = unix.Flock(lockfd, unix.LOCK_UN); _ = unix.Close(lockfd) }, nil
}

func validateExisting(path string) error {
	dirfd, name, err := walkParent(path, false)
	if err != nil {
		return err
	}
	defer unix.Close(dirfd)
	if !filesystemAllowed(dirfd) {
		return ErrUnsafeFilesystem
	}
	if err := validateAt(dirfd, name, 0o600); err != nil {
		return err
	}
	return validateSidecarsAt(dirfd, name)
}

func validateAt(dirfd int, name string, mode uint32) error {
	fd, err := unix.Openat(dirfd, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return errors.New("stage store: private file unavailable")
	}
	defer unix.Close(fd)
	return validateFD(fd, mode)
}

func recoverPrivateRegularAt(dirfd int, name string, desired uint32) error {
	var stat unix.Stat_t
	if err := unix.Fstatat(dirfd, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return errors.New("stage store: private file unavailable")
	}
	mode := uint32(stat.Mode)
	if mode&unix.S_IFMT != unix.S_IFREG || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 || mode&^uint32(unix.S_IFMT|desired) != 0 {
		return errors.New("stage store: unsafe private file")
	}
	if mode&0o777 == desired {
		return nil
	}
	if err := unix.Fchmodat(dirfd, name, desired, 0); err != nil {
		return errors.New("stage store: cannot recover private file")
	}
	return validateAt(dirfd, name, desired)
}

func validateFD(fd int, mode uint32) error {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return errors.New("stage store: cannot inspect private file")
	}
	if !hasExactMode(uint32(stat.Mode), unix.S_IFREG, mode) || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
		return errors.New("stage store: unsafe private file")
	}
	return nil
}

func validateSidecars(path string) error {
	dirfd, name, err := walkParent(path, false)
	if err != nil {
		return err
	}
	defer unix.Close(dirfd)
	return validateSidecarsAt(dirfd, name)
}

func validateImmutableRead(path string) error {
	dirfd, name, err := walkParent(path, false)
	if err != nil {
		return err
	}
	defer unix.Close(dirfd)
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		var stat unix.Stat_t
		err := unix.Fstatat(dirfd, name+suffix, &stat, unix.AT_SYMLINK_NOFOLLOW)
		if err == nil {
			return errors.New("stage store: active journal prevents immutable inspection")
		}
		if !errors.Is(err, unix.ENOENT) {
			return errors.New("stage store: cannot inspect database sidecar")
		}
	}
	return nil
}

func validateSidecarsAt(dirfd int, name string) error {
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		var stat unix.Stat_t
		err := unix.Fstatat(dirfd, name+suffix, &stat, unix.AT_SYMLINK_NOFOLLOW)
		if errors.Is(err, unix.ENOENT) {
			continue
		}
		if err != nil {
			return errors.New("stage store: cannot inspect database sidecar")
		}
		if err := validateAt(dirfd, name+suffix, 0o600); err != nil {
			return errors.New("stage store: unsafe database sidecar")
		}
	}
	return nil
}

func removeFailedNewDatabase(path string) {
	dirfd, name, err := walkParent(path, false)
	if err != nil {
		return
	}
	defer unix.Close(dirfd)
	if validateAt(dirfd, name, 0o600) != nil {
		return
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal", ""} {
		_ = unix.Unlinkat(dirfd, name+suffix, 0)
	}
}

func clearBootstrapPending(path string) error {
	dirfd, _, err := walkParent(path, false)
	if err != nil {
		return err
	}
	defer unix.Close(dirfd)
	exists, err := entryExistsAt(dirfd, bootstrapPendingFilename)
	if err != nil || !exists {
		return err
	}
	if err := validatePendingAt(dirfd); err != nil {
		return err
	}
	if err := unix.Unlinkat(dirfd, bootstrapPendingFilename, 0); err != nil {
		return errors.New("stage store: cannot clear bootstrap marker")
	}
	if err := unix.Fsync(dirfd); err != nil {
		return errors.New("stage store: cannot sync state directory")
	}
	return nil
}

// SQLite ultimately resolves the validated absolute path itself. Descriptor-relative
// checks before and after open close accidental races, but cannot defend against a
// malicious same-UID process continuously swapping entries. Same-UID hostile
// processes and extended ACLs unavailable through x/sys are outside this boundary.
func platformSupported() bool { return true }

func permissionModelLimitations() []string {
	return []string{
		"same-UID path replacement is outside the threat boundary",
		"extended ACL verification is unavailable without cgo",
	}
}
