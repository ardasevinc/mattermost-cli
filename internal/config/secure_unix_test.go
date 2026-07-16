//go:build darwin || linux

package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestInitEnforcesModeDespiteRestrictiveUmask(t *testing.T) {
	home := filepath.Join(t.TempDir(), "fresh-home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	configDirectory := filepath.Join(home, ".config", "mattermost-cli")
	path := filepath.Join(configDirectory, "config.toml")
	previous := unix.Umask(0o777)
	t.Cleanup(func() { unix.Umask(previous) })
	created, err := Init(path)
	unix.Umask(previous)
	if err != nil || !created {
		t.Fatalf("Init() = (%v, %v)", created, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %#o, want 0600", got)
	}
	for _, directory := range []string{filepath.Join(home, ".config"), configDirectory} {
		info, err := os.Stat(directory)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o700 {
			t.Fatalf("directory %q mode = %#o, want 0700", directory, got)
		}
	}
}

func TestInitFallsBackWhenNoFollowDirectoryChmodIsUnsupportedUnderTrustedParent(t *testing.T) {
	original := configDirectoryFchmodat
	noFollowCalls, fallbackCalls := 0, 0
	configDirectoryFchmodat = func(dirfd int, path string, mode uint32, flags int) error {
		if flags == unix.AT_SYMLINK_NOFOLLOW {
			noFollowCalls++
			return unix.ENOTSUP
		}
		fallbackCalls++
		return original(dirfd, path, mode, flags)
	}
	t.Cleanup(func() { configDirectoryFchmodat = original })

	home := filepath.Join(t.TempDir(), "fresh-home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(home, ".config", "mattermost-cli")
	previous := unix.Umask(0o777)
	t.Cleanup(func() { unix.Umask(previous) })
	created, err := Init(filepath.Join(directory, "config.toml"))
	unix.Umask(previous)
	if err != nil || !created {
		t.Fatalf("Init() = (%v, %v)", created, err)
	}
	if noFollowCalls != 2 || fallbackCalls != 2 {
		t.Fatalf("chmod calls no-follow=%d fallback=%d, want 2/2", noFollowCalls, fallbackCalls)
	}
	for _, path := range []string{filepath.Join(home, ".config"), directory} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o700 {
			t.Fatalf("directory %q mode = %#o, want 0700", path, got)
		}
	}
}

func TestUnsupportedNoFollowChmodDoesNotFallbackUnderUntrustedParent(t *testing.T) {
	original := configDirectoryFchmodat
	fallbackCalls := 0
	configDirectoryFchmodat = func(dirfd int, path string, mode uint32, flags int) error {
		if flags == unix.AT_SYMLINK_NOFOLLOW {
			return unix.ENOTSUP
		}
		fallbackCalls++
		return original(dirfd, path, mode, flags)
	}
	t.Cleanup(func() { configDirectoryFchmodat = original })

	parentPath := t.TempDir()
	if err := os.Chmod(parentPath, 0o777); err != nil {
		t.Fatal(err)
	}
	parent, err := unix.Open(parentPath, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Close(parent) })
	if err := unix.Mkdirat(parent, "child", 0o700); err != nil {
		t.Fatal(err)
	}
	var expected unix.Stat_t
	if err := unix.Fstatat(parent, "child", &expected, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		t.Fatal(err)
	}

	_, unsafe, err := secureCreatedConfigDirectory(parent, "child", expected, uint32(os.Geteuid()), false)
	if !errors.Is(err, errUnsafePath) || unsafe != UnsafeUnsupported {
		t.Fatalf("secureCreatedConfigDirectory() unsafe=%q err=%v", unsafe, err)
	}
	if fallbackCalls != 0 {
		t.Fatalf("flags-free fallback called %d times under untrusted parent", fallbackCalls)
	}
}

func TestFallbackRejectsInjectedSwapEvenWithinTrustedBoundary(t *testing.T) {
	original := configDirectoryFchmodat
	configDirectoryFchmodat = func(dirfd int, path string, mode uint32, flags int) error {
		if flags == unix.AT_SYMLINK_NOFOLLOW {
			return unix.ENOTSUP
		}
		if err := unix.Renameat(dirfd, path, dirfd, "original"); err != nil {
			return err
		}
		if err := unix.Mkdirat(dirfd, path, 0o700); err != nil {
			return err
		}
		return original(dirfd, path, mode, flags)
	}
	t.Cleanup(func() { configDirectoryFchmodat = original })

	parentPath := t.TempDir()
	parent, err := unix.Open(parentPath, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Close(parent) })
	if err := unix.Mkdirat(parent, "child", 0o700); err != nil {
		t.Fatal(err)
	}
	var expected unix.Stat_t
	if err := unix.Fstatat(parent, "child", &expected, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		t.Fatal(err)
	}

	_, unsafe, err := secureCreatedConfigDirectory(parent, "child", expected, uint32(os.Geteuid()), true)
	if !errors.Is(err, errUnsafePath) || unsafe != UnsafeChanged {
		t.Fatalf("secureCreatedConfigDirectory() unsafe=%q err=%v, want changed", unsafe, err)
	}
}

func TestInitDoesNotFallbackForOtherDirectoryChmodErrors(t *testing.T) {
	original := configDirectoryFchmodat
	fallbackCalls := 0
	configDirectoryFchmodat = func(dirfd int, path string, mode uint32, flags int) error {
		if flags == unix.AT_SYMLINK_NOFOLLOW {
			return unix.EPERM
		}
		fallbackCalls++
		return original(dirfd, path, mode, flags)
	}
	t.Cleanup(func() { configDirectoryFchmodat = original })

	path := filepath.Join(t.TempDir(), "fresh-home", ".config", "mattermost-cli", "config.toml")
	created, err := Init(path)
	if err == nil || created {
		t.Fatalf("Init() = (%v, %v), want fail-closed chmod error", created, err)
	}
	if fallbackCalls != 0 {
		t.Fatalf("flags-free fallback called %d times for EPERM", fallbackCalls)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("config file survived failed init: %v", statErr)
	}
}

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
