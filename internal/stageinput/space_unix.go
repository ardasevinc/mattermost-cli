//go:build darwin || linux

package stageinput

import (
	"errors"
	"math"

	"golang.org/x/sys/unix"
)

func availableSpoolBytes(directory string) (uint64, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(directory, &stat); err != nil || stat.Bsize <= 0 {
		return 0, errors.New("spool filesystem unavailable")
	}
	blockSize := uint64(stat.Bsize)
	availableBlocks := uint64(stat.Bavail)
	if availableBlocks > math.MaxUint64/blockSize {
		return math.MaxUint64, nil
	}
	return availableBlocks * blockSize, nil
}
