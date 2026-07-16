//go:build darwin || linux

package stageinput

import "golang.org/x/sys/unix"

func makeFIFO(path string) error { return unix.Mkfifo(path, 0o600) }
