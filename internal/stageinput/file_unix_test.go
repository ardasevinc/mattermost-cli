//go:build darwin || linux

package stageinput

import "testing"

func TestDurableFileBindingCoversCompleteStableIdentity(t *testing.T) {
	base := fileIdentity{dev: 1, ino: 2, mode: 0o100600, nlink: 1, size: 3, mtimeNsec: 4, ctimeNsec: 5}
	mutations := []fileIdentity{
		{dev: 9, ino: 2, mode: 0o100600, nlink: 1, size: 3, mtimeNsec: 4, ctimeNsec: 5},
		{dev: 1, ino: 9, mode: 0o100600, nlink: 1, size: 3, mtimeNsec: 4, ctimeNsec: 5},
		{dev: 1, ino: 2, mode: 0o100400, nlink: 1, size: 3, mtimeNsec: 4, ctimeNsec: 5},
		{dev: 1, ino: 2, mode: 0o100600, nlink: 2, size: 3, mtimeNsec: 4, ctimeNsec: 5},
		{dev: 1, ino: 2, mode: 0o100600, nlink: 1, size: 9, mtimeNsec: 4, ctimeNsec: 5},
		{dev: 1, ino: 2, mode: 0o100600, nlink: 1, size: 3, mtimeNsec: 9, ctimeNsec: 5},
		{dev: 1, ino: 2, mode: 0o100600, nlink: 1, size: 3, mtimeNsec: 4, ctimeNsec: 9},
	}
	for i, changed := range mutations {
		if base.binding() == changed.binding() {
			t.Fatalf("mutation %d did not change durable binding", i)
		}
	}
}
