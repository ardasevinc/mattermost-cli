//go:build linux

package stagestore

import "golang.org/x/sys/unix"

func localFilesystemAllowed(fd int) bool {
	var stat unix.Statfs_t
	if unix.Fstatfs(fd, &stat) != nil {
		return false
	}
	return linuxFilesystemTypeAllowed(uint64(stat.Type))
}
