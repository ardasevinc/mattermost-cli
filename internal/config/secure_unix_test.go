//go:build darwin || linux

package config

import (
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestLoadRejectsFIFOWithoutBlocking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan FileState, 1)
	go func() {
		done <- Load(Paths{ConfigPath: path, LegacyPath: path})
	}()

	select {
	case state := <-done:
		if state.Error != FileErrorRead || state.Unsafe != UnsafeType {
			t.Fatalf("Load() FIFO state = %+v, want unsafe type", state)
		}
	case <-time.After(time.Second):
		t.Fatal("Load() blocked opening FIFO")
	}
	created, err := Init(path)
	if err == nil || created {
		t.Fatalf("Init() FIFO = (%v, %v), want unsafe-path failure", created, err)
	}
}
