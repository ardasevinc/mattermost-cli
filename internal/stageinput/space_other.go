//go:build !darwin && !linux

package stageinput

import "errors"

func availableSpoolBytes(string) (uint64, error) {
	return 0, errors.New("spool filesystem accounting unsupported")
}
