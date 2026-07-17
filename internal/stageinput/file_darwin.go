//go:build darwin

package stageinput

import "golang.org/x/sys/unix"

func platformIdentity(stat *unix.Stat_t) fileIdentity {
	return fileIdentity{dev: uint64(stat.Dev), ino: stat.Ino, mode: uint32(stat.Mode), nlink: uint64(stat.Nlink), size: stat.Size,
		mtimeNsec: int64(stat.Mtim.Sec)*1e9 + int64(stat.Mtim.Nsec), ctimeNsec: int64(stat.Ctim.Sec)*1e9 + int64(stat.Ctim.Nsec)}
}

// Darwin has no O_PATH. Refuse attacker-writable parents, then compare the
// no-follow pathname observation with the opened descriptor before reading.
func platformOpenLeaf(parent int, name string) (int, fileIdentity, error) {
	var directory unix.Stat_t
	if err := unix.Fstat(parent, &directory); err != nil || directory.Uid != uint32(unix.Geteuid()) || uint32(directory.Mode)&0o022 != 0 {
		return -1, fileIdentity{}, ErrUnsafeFile
	}
	var stat unix.Stat_t
	if err := unix.Fstatat(parent, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return -1, fileIdentity{}, err
	}
	preflight, err := identityFromStat(&stat)
	if err != nil {
		return -1, fileIdentity{}, err
	}
	fd, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return -1, fileIdentity{}, err
	}
	if err := unix.Fstat(fd, &stat); err != nil {
		unix.Close(fd)
		return -1, fileIdentity{}, err
	}
	opened, err := identityFromStat(&stat)
	if err != nil || !preflight.stable(opened) || !platformLocal(fd) {
		unix.Close(fd)
		return -1, fileIdentity{}, ErrUnsafeFile
	}
	return fd, opened, nil
}

func platformLocal(fd int) bool {
	var stat unix.Statfs_t
	return unix.Fstatfs(fd, &stat) == nil && stat.Flags&unix.MNT_LOCAL != 0
}
