//go:build linux

package stageinput

import (
	"strconv"

	"golang.org/x/sys/unix"
)

func platformIdentity(stat *unix.Stat_t) fileIdentity {
	return fileIdentity{dev: uint64(stat.Dev), ino: stat.Ino, mode: uint32(stat.Mode), nlink: uint64(stat.Nlink), size: stat.Size,
		mtimeNsec: int64(stat.Mtim.Sec)*1e9 + int64(stat.Mtim.Nsec), ctimeNsec: int64(stat.Ctim.Sec)*1e9 + int64(stat.Ctim.Nsec)}
}

func platformOpenLeaf(parent int, name string) (int, fileIdentity, error) {
	pathfd, err := unix.Openat(parent, name, unix.O_PATH|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, fileIdentity{}, err
	}
	defer unix.Close(pathfd)
	var stat unix.Stat_t
	if err := unix.Fstat(pathfd, &stat); err != nil {
		return -1, fileIdentity{}, err
	}
	preflight, err := identityFromStat(&stat)
	if err != nil || !platformLocal(pathfd) {
		return -1, fileIdentity{}, ErrUnsafeFile
	}
	// procfs resolves the retained descriptor, never the mutable directory entry.
	fd, err := unix.Open("/proc/self/fd/"+strconv.Itoa(pathfd), unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
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
	if unix.Fstatfs(fd, &stat) != nil {
		return false
	}
	switch uint64(stat.Type) {
	case 0xEF53, 0x58465342, 0x9123683E, 0x01021994, 0x794C7630, 0x2FC12FC1:
		return true
	default:
		return false
	}
}
