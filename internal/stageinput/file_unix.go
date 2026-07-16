//go:build darwin || linux

package stageinput

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

type fileIdentity struct {
	dev, ino  uint64
	mode      uint32
	nlink     uint64
	size      int64
	mtimeNsec int64
	ctimeNsec int64
}

func (a fileIdentity) sameFile(b fileIdentity) bool { return a.dev == b.dev && a.ino == b.ino }
func (a fileIdentity) stable(b fileIdentity) bool {
	return a.sameFile(b) && a.mode == b.mode && a.nlink == b.nlink && a.size == b.size && a.mtimeNsec == b.mtimeNsec && a.ctimeNsec == b.ctimeNsec
}

func openSecure(path string) (*os.File, fileIdentity, error) {
	if !filepath.IsAbs(path) {
		return nil, fileIdentity{}, ErrInvalid
	}
	parts := strings.Split(strings.TrimPrefix(filepath.Clean(path), string(filepath.Separator)), string(filepath.Separator))
	if len(parts) == 0 || parts[0] == "" {
		return nil, fileIdentity{}, ErrInvalid
	}
	parent, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fileIdentity{}, ErrUnsafeFile
	}
	for _, part := range parts[:len(parts)-1] {
		next, openErr := unix.Openat(parent, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
		_ = unix.Close(parent)
		if openErr != nil {
			return nil, fileIdentity{}, ErrUnsafeFile
		}
		parent = next
	}
	name := parts[len(parts)-1]
	fd, opened, openErr := platformOpenLeaf(parent, name)
	_ = unix.Close(parent)
	if openErr != nil {
		return nil, fileIdentity{}, ErrUnsafeFile
	}
	if err := unix.SetNonblock(fd, false); err != nil {
		_ = unix.Close(fd)
		return nil, fileIdentity{}, ErrUnsafeFile
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, fileIdentity{}, ErrUnsafeFile
	}
	return file, opened, nil
}

func fileIdentityOf(file *os.File) (fileIdentity, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return fileIdentity{}, err
	}
	return identityFromStat(&stat)
}

func identityFromStat(stat *unix.Stat_t) (fileIdentity, error) {
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 {
		return fileIdentity{}, errors.New("not regular")
	}
	return platformIdentity(stat), nil
}
