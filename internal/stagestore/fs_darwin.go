//go:build darwin

package stagestore

import (
	"strings"

	"golang.org/x/sys/unix"
)

func localFilesystemAllowed(fd int) bool {
	var stat unix.Statfs_t
	if unix.Fstatfs(fd, &stat) != nil || stat.Flags&unix.MNT_LOCAL == 0 {
		return false
	}
	name := strings.TrimRight(string(stat.Fstypename[:]), "\x00")
	switch name {
	case "nfs", "smbfs", "webdav", "osxfuse", "macfuse", "fusefs", "afpfs", "autofs":
		return false
	default:
		return true
	}
}
