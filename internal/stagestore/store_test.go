//go:build darwin || linux

package stagestore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func testPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "state", "mattermost-cli", DatabaseFilename)
}

func TestResolvePaths(t *testing.T) {
	paths, err := ResolvePaths("/home/test", func(key string) (string, bool) { return "/state root/?x", key == "XDG_STATE_HOME" })
	if err != nil || paths.DBPath != "/state root/?x/mattermost-cli/stages.sqlite3" {
		t.Fatalf("paths=%#v err=%v", paths, err)
	}
	paths, err = ResolvePaths("/home/test", func(string) (string, bool) { return "relative", true })
	if err != nil || paths.StateDir != "/home/test/.local/state/mattermost-cli" {
		t.Fatalf("fallback=%#v err=%v", paths, err)
	}
}

func TestCanonicalDatabasePathOnly(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{
		filepath.Join(root, "other.sqlite3"),
		filepath.Join(root, "nested", "..", DatabaseFilename),
		"relative/" + DatabaseFilename,
	} {
		if _, err := Open(context.Background(), path); err == nil {
			t.Fatalf("accepted path %q", path)
		}
	}
}

func TestOpenInitializesAndReopens(t *testing.T) {
	ctx, path := context.Background(), testPath(t)
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	var id, foreignKeys, trusted, secureDelete, synchronous int
	var journal string
	checks := []struct {
		query  string
		target any
	}{{"PRAGMA application_id", &id}, {"PRAGMA foreign_keys", &foreignKeys}, {"PRAGMA trusted_schema", &trusted}, {"PRAGMA secure_delete", &secureDelete}, {"PRAGMA synchronous", &synchronous}, {"PRAGMA journal_mode", &journal}}
	for _, check := range checks {
		if err := s.db.QueryRow(check.query).Scan(check.target); err != nil {
			t.Fatal(err)
		}
	}
	if id != applicationID || foreignKeys != 1 || trusted != 0 || secureDelete != 2 || synchronous != 2 || journal == "" {
		t.Fatalf("id=%x fk=%d trusted=%d delete=%d sync=%d journal=%s", id, foreignKeys, trusted, secureDelete, synchronous, journal)
	}
	s.db.SetConnMaxLifetime(time.Nanosecond)
	time.Sleep(time.Millisecond)
	if err := s.db.QueryRow("PRAGMA secure_delete").Scan(&secureDelete); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow("PRAGMA synchronous").Scan(&synchronous); err != nil {
		t.Fatal(err)
	}
	if secureDelete != 2 || synchronous != 2 {
		t.Fatalf("replacement connection delete=%d sync=%d", secureDelete, synchronous)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		path string
		mode os.FileMode
	}{{filepath.Dir(path), 0o700}, {path, 0o600}} {
		info, err := os.Stat(item.path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != item.mode {
			t.Fatalf("%s mode=%o", item.path, info.Mode().Perm())
		}
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var count int
	if err := s.db.QueryRow("SELECT count(*) FROM schema_migrations").Scan(&count); err != nil || count != len(migrations) {
		t.Fatalf("count=%d err=%v", count, err)
	}
}

func TestReadOnlyAbsentDoesNotCreate(t *testing.T) {
	path := testPath(t)
	if _, err := OpenReadOnly(context.Background(), path); err == nil {
		t.Fatal("expected error")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat=%v", err)
	}
	report, err := Doctor(context.Background(), path)
	if err != nil || report.Exists {
		t.Fatalf("report=%#v err=%v", report, err)
	}
}

func createPendingMarkerForTest(t *testing.T, path string) {
	t.Helper()
	dirfd, _, err := walkParent(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(dirfd)
	if err := createPendingAt(dirfd); err != nil {
		t.Fatal(err)
	}
}

func TestAdoptsNonzeroBootstrapResidueWithMarker(t *testing.T) {
	path := testPath(t)
	createPendingMarkerForTest(t, path)
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", sqliteURI(path, false))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("VACUUM"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() == 0 {
		t.Fatal("VACUUM left an empty database file")
	}
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var id int
	if err := s.db.QueryRow("PRAGMA application_id").Scan(&id); err != nil || id != applicationID {
		t.Fatalf("id=%x err=%v", id, err)
	}
}

func TestRejectsMarkerBackedForeignSchema(t *testing.T) {
	path := testPath(t)
	createPendingMarkerForTest(t, path)
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", sqliteURI(path, false))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE foreign_schema(value TEXT)"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), path); !errors.Is(err, ErrIdentity) {
		t.Fatalf("error=%v", err)
	}
}

func TestStaleBootstrapMarkerAfterCommitHeals(t *testing.T) {
	path := testPath(t)
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	createPendingMarkerForTest(t, path)
	s, err = Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), bootstrapPendingFilename)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("marker stat=%v", err)
	}
}

func TestLinuxFilesystemTypeWhitelist(t *testing.T) {
	for name, kind := range map[string]uint64{
		"ext": 0xEF53, "bcachefs": 0xCA451A4E, "nilfs2": 0x3434, "ubifs": 0x24051905,
	} {
		if !linuxFilesystemTypeAllowed(kind) {
			t.Fatalf("%s filesystem rejected", name)
		}
	}
	if linuxFilesystemTypeAllowed(0x6969) {
		t.Fatal("NFS filesystem accepted")
	}
	if linuxFilesystemTypeAllowed(0xDEADBEEF) {
		t.Fatal("unknown filesystem accepted")
	}
}

func TestCreatedDirectoryNoFollowFallbackRequiresTrustedParent(t *testing.T) {
	root := t.TempDir()
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	if err := unix.Mkdirat(fd, "child", 0o700); err != nil {
		t.Fatal(err)
	}
	var expected unix.Stat_t
	if err := unix.Fstatat(fd, "child", &expected, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		t.Fatal(err)
	}
	old := stateDirectoryFchmodat
	stateDirectoryFchmodat = func(parent int, name string, mode uint32, flags int) error {
		if flags != 0 {
			return unix.ENOTSUP
		}
		return unix.Fchmodat(parent, name, mode, flags)
	}
	t.Cleanup(func() { stateDirectoryFchmodat = old })
	if _, err := secureCreatedDirectory(fd, "child", expected, false); err == nil {
		t.Fatal("unsafe fallback accepted")
	}
	if _, err := secureCreatedDirectory(fd, "child", expected, true); err != nil {
		t.Fatal(err)
	}
}

func TestPendingMarkerShortWriteCompletesExactly(t *testing.T) {
	old := pendingWrite
	first := true
	pendingWrite = func(fd int, value []byte) (int, error) {
		if first {
			first = false
			return unix.Write(fd, value[:len(value)/2])
		}
		return unix.Write(fd, value)
	}
	t.Cleanup(func() { pendingWrite = old })
	s, err := Open(context.Background(), testPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
}

func TestPartialPendingMarkerWithoutDatabaseRecovers(t *testing.T) {
	path := testPath(t)
	dirfd, _, err := walkParent(path, true)
	if err != nil {
		t.Fatal(err)
	}
	unix.Close(dirfd)
	marker := filepath.Join(filepath.Dir(path), bootstrapPendingFilename)
	if err := os.WriteFile(marker, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
}

func TestPartialPendingMarkerWithDatabaseRejects(t *testing.T) {
	path := testPath(t)
	dirfd, _, err := walkParent(path, true)
	if err != nil {
		t.Fatal(err)
	}
	unix.Close(dirfd)
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), bootstrapPendingFilename), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), path); err == nil {
		t.Fatal("accepted partial marker beside database")
	}
}

func TestSymlinkAdmissionBeforeBoundary(t *testing.T) {
	if !symlinkAllowedBeforeBoundary(0, false) {
		t.Fatal("root-owned pre-boundary symlink rejected")
	}
	if symlinkAllowedBeforeBoundary(uint32(os.Geteuid()), false) && os.Geteuid() != 0 {
		t.Fatal("user-owned pre-boundary symlink accepted")
	}
	if symlinkAllowedBeforeBoundary(0, true) {
		t.Fatal("root-owned symlink accepted after boundary")
	}
	if symlinkAllowedBeforeBoundary(12345, false) {
		t.Fatal("foreign-owned pre-boundary symlink accepted")
	}
}

func TestRootOwnedAncestorPermissions(t *testing.T) {
	if !ancestorPermissionsAllowed(0, uint32(unix.S_IFDIR|0o755), false) {
		t.Fatal("ordinary root-owned ancestor rejected")
	}
	if !ancestorPermissionsAllowed(0, uint32(unix.S_IFDIR|unix.S_ISVTX|0o777), false) {
		t.Fatal("sticky root-owned ancestor rejected")
	}
	if ancestorPermissionsAllowed(0, uint32(unix.S_IFDIR|0o777), false) {
		t.Fatal("non-sticky world-writable root-owned ancestor accepted")
	}
	if ancestorPermissionsAllowed(0, uint32(unix.S_IFDIR|0o775), false) {
		t.Fatal("non-sticky group-writable root-owned ancestor accepted")
	}
}

func TestRejectsNonEmptyZeroIdentityDatabase(t *testing.T) {
	path := testPath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", sqliteURI(path, false))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE foreign_database(value TEXT)"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), path); !errors.Is(err, ErrIdentity) {
		t.Fatalf("error=%v", err)
	}
}

func TestRestrictiveUmaskStillCreatesExactModes(t *testing.T) {
	path := testPath(t)
	old := unix.Umask(0o777)
	t.Cleanup(func() { unix.Umask(old) })
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, item := range []struct {
		path string
		mode os.FileMode
	}{{filepath.Dir(path), 0o700}, {path, 0o600}, {filepath.Join(filepath.Dir(path), bootstrapLockFilename), 0o600}} {
		info, err := os.Stat(item.path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != item.mode {
			t.Fatalf("%s mode=%o", item.path, info.Mode().Perm())
		}
	}
}

func TestRecoversPrivateCreationResidue(t *testing.T) {
	t.Run("state directory", func(t *testing.T) {
		path := testPath(t)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Dir(path), 0); err != nil {
			t.Fatal(err)
		}
		s, err := Open(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
	})

	t.Run("intermediate directory", func(t *testing.T) {
		root := t.TempDir()
		intermediate := filepath.Join(root, "state")
		if err := os.Mkdir(intermediate, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(intermediate, 0); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(intermediate, "mattermost-cli", DatabaseFilename)
		s, err := Open(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
	})

	t.Run("bootstrap lock", func(t *testing.T) {
		path := testPath(t)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		lock := filepath.Join(filepath.Dir(path), bootstrapLockFilename)
		if err := os.WriteFile(lock, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(lock, 0); err != nil {
			t.Fatal(err)
		}
		s, err := Open(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
	})

	t.Run("marker-backed database", func(t *testing.T) {
		path := testPath(t)
		createPendingMarkerForTest(t, path)
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0); err != nil {
			t.Fatal(err)
		}
		s, err := Open(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
	})
}

func TestReadOnlyDoesNotRecoverDirectoryMode(t *testing.T) {
	path := testPath(t)
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(path), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Dir(path), 0o700) })
	if _, err := OpenReadOnly(context.Background(), path); err == nil {
		t.Fatal("read-only open accepted noncanonical directory mode")
	}
	info, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o500 {
		t.Fatalf("read-only open mutated directory mode to %o", info.Mode().Perm())
	}
}

func TestRejectsSpecialBitsDuringPrivateFileRecovery(t *testing.T) {
	path := testPath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(filepath.Dir(path), bootstrapLockFilename)
	if err := os.WriteFile(lock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := unix.Chmod(lock, unix.S_ISUID|0o600); err != nil {
		t.Skipf("setuid mode unavailable: %v", err)
	}
	if _, err := Open(context.Background(), path); err == nil {
		t.Fatal("accepted setuid bootstrap lock")
	}
}

func TestExactModeRejectsSpecialBits(t *testing.T) {
	if !hasExactMode(unix.S_IFREG|0o600, unix.S_IFREG, 0o600) {
		t.Fatal("exact regular mode rejected")
	}
	if hasExactMode(unix.S_IFREG|unix.S_ISUID|0o600, unix.S_IFREG, 0o600) {
		t.Fatal("setuid regular mode accepted")
	}
	if hasExactMode(unix.S_IFDIR|unix.S_ISGID|0o700, unix.S_IFDIR, 0o700) {
		t.Fatal("setgid directory mode accepted")
	}
	if hasExactMode(unix.S_IFDIR|unix.S_ISVTX|0o700, unix.S_IFDIR, 0o700) {
		t.Fatal("sticky directory mode accepted")
	}
}

func TestRejectsIdentityAndMigrationDrift(t *testing.T) {
	ctx, path := context.Background(), testPath(t)
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("PRAGMA application_id=42"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err := Open(ctx, path); !errors.Is(err, ErrIdentity) {
		t.Fatalf("identity=%v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("UPDATE schema_migrations SET checksum='wrong'"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err := OpenReadOnly(ctx, path); !errors.Is(err, ErrMigration) {
		t.Fatalf("migration=%v", err)
	}
}

func TestMigrationFailureIsAtomicAndUnknownIsRejected(t *testing.T) {
	ctx, path := context.Background(), testPath(t)
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	original := migrations
	t.Cleanup(func() { migrations = original })
	migrations = append(append([]migration{}, migrations...), migration{version: len(migrations) + 1, name: "broken", sql: "CREATE TABLE should_rollback(x); SELECT no_such_function();"})
	if _, err := Open(ctx, path); !errors.Is(err, ErrMigration) {
		t.Fatalf("failure=%v", err)
	}
	migrations = original
	s, err = OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='should_rollback'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial table count=%d err=%v", count, err)
	}
	s.Close()
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.db.Exec("INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(?,'future','x','x')", len(migrations)+1)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err := OpenReadOnly(ctx, path); !errors.Is(err, ErrMigration) {
		t.Fatalf("unknown=%v", err)
	}
}

func TestRejectsUnsafeFiles(t *testing.T) {
	ctx := context.Background()
	t.Run("mode", func(t *testing.T) {
		path := testPath(t)
		s, err := Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		s.Close()
		os.Chmod(path, 0o644)
		if _, err := Open(ctx, path); err == nil {
			t.Fatal("accepted mode")
		}
	})
	t.Run("hardlink", func(t *testing.T) {
		path := testPath(t)
		s, err := Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		s.Close()
		if err := os.Link(path, path+".link"); err != nil {
			t.Skip(err)
		}
		if _, err := Open(ctx, path); err == nil {
			t.Fatal("accepted hardlink")
		}
	})
	t.Run("symlink", func(t *testing.T) {
		root, real := t.TempDir(), filepath.Join(t.TempDir(), "real")
		if err := os.Mkdir(real, 0o700); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(root, "link")
		if err := os.Symlink(real, link); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(ctx, filepath.Join(link, DatabaseFilename)); err == nil {
			t.Fatal("accepted symlink")
		}
	})
	t.Run("sidecar mode", func(t *testing.T) {
		path := testPath(t)
		s, err := Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		s.Close()
		if err := os.WriteFile(path+"-journal", nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(ctx, path); err == nil {
			t.Fatal("accepted unsafe sidecar")
		}
	})
	t.Run("bootstrap lock mode", func(t *testing.T) {
		path := testPath(t)
		s, err := Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(filepath.Dir(path), bootstrapLockFilename), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(ctx, path); err == nil {
			t.Fatal("accepted unsafe bootstrap lock")
		}
	})
}

func TestNetworkFilesystemSeam(t *testing.T) {
	old := filesystemAllowed
	filesystemAllowed = func(int) bool { return false }
	t.Cleanup(func() { filesystemAllowed = old })
	if _, err := Open(context.Background(), testPath(t)); !errors.Is(err, ErrUnsafeFilesystem) {
		t.Fatalf("error=%v", err)
	}
}

func TestRollbackJournalFallbackSeam(t *testing.T) {
	old := setJournalMode
	setJournalMode = func(context.Context, *sql.DB) (string, error) { return "delete", nil }
	t.Cleanup(func() { setJournalMode = old })
	s, err := Open(context.Background(), testPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if !s.journalFallback || s.journalMode != "delete" {
		t.Fatalf("mode=%s fallback=%v", s.journalMode, s.journalFallback)
	}
}

func TestBootstrapLockHonorsCancellation(t *testing.T) {
	path := testPath(t)
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	f, err := os.OpenFile(filepath.Join(filepath.Dir(path), bootstrapLockFilename), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	defer unix.Flock(int(f.Fd()), unix.LOCK_UN)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	_, err = Open(ctx, path)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error=%v", err)
	}
}

func TestSQLiteBusyHonorsCancellation(t *testing.T) {
	path := testPath(t)
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	blocker, err := sql.Open("sqlite", sqliteURI(path, false))
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()
	if _, err := blocker.Exec("BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer blocker.Exec("ROLLBACK")
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = Open(ctx, path)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error=%v", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("cancellation took %s", elapsed)
	}
}

type fileSnapshot struct {
	exists  bool
	mode    os.FileMode
	size    int64
	modTime time.Time
	digest  [32]byte
}

func snapshotStoreFiles(t *testing.T, path string) map[string]fileSnapshot {
	t.Helper()
	result := make(map[string]fileSnapshot)
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		name := path + suffix
		info, err := os.Stat(name)
		if errors.Is(err, os.ErrNotExist) {
			result[suffix] = fileSnapshot{}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		content, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		result[suffix] = fileSnapshot{exists: true, mode: info.Mode(), size: info.Size(), modTime: info.ModTime(), digest: sha256.Sum256(content)}
	}
	return result
}

func TestReadOnlyAndDoctorDoNotMutateStoreFiles(t *testing.T) {
	ctx, path := context.Background(), testPath(t)
	writer, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.db.Exec("CREATE TABLE readonly_probe(value TEXT)"); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.db.Exec("INSERT INTO readonly_probe VALUES('visible-in-wal')"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	before := snapshotStoreFiles(t, path)
	ro, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	var value string
	if err := ro.db.QueryRow("SELECT value FROM readonly_probe").Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != "visible-in-wal" {
		t.Fatalf("value=%q", value)
	}
	if err := ro.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Doctor(ctx, path); err != nil {
		t.Fatal(err)
	}
	after := snapshotStoreFiles(t, path)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("store files changed during read-only inspection: before=%#v after=%#v", before, after)
	}
}

func TestReadOnlyRejectsActiveWALWithoutMutation(t *testing.T) {
	ctx, path := context.Background(), testPath(t)
	writer, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.db.Exec("CREATE TABLE active_wal(value TEXT)"); err != nil {
		t.Fatal(err)
	}
	before := snapshotStoreFiles(t, path)
	waitCtx, cancel := context.WithTimeout(ctx, 25*time.Millisecond)
	defer cancel()
	if _, err := OpenReadOnly(waitCtx, path); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error=%v", err)
	}
	after := snapshotStoreFiles(t, path)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("active WAL changed during rejected read-only open")
	}
}

func TestCurrentRevisionMustBeCurrent(t *testing.T) {
	path := testPath(t)
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(`INSERT INTO stages(id,created_at,updated_at,operation,server_url,user_id,lifecycle,recovery,current_revision) VALUES('s','x','x','create_post','https://x','u','open','none',1)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(`INSERT INTO stage_revisions(stage_id,revision,state,created_at,semantic_digest,destination_json,plan_json) VALUES('s',1,'superseded','x',zeroblob(32),'{}','{}')`)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err == nil {
		t.Fatal("accepted superseded current revision")
	}
}

func TestDoctorReportsForeignKeyViolation(t *testing.T) {
	ctx, path := context.Background(), testPath(t)
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	db, err := sql.Open("sqlite", sqliteURI(path, false))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`PRAGMA foreign_keys=OFF;
INSERT INTO stages(id,created_at,updated_at,operation,server_url,user_id,lifecycle,recovery,current_revision) VALUES('s','x','x','create_post','https://x','u','open','none',1);
INSERT INTO stage_revisions(stage_id,revision,state,created_at,semantic_digest,destination_json,plan_json) VALUES('s',1,'current','x',zeroblob(32),'{}','{}');
INSERT INTO request_replays(server_url,user_id,request_id,request_schema,semantic_digest,stage_id,revision,created_at) VALUES('https://x','u','r','v',zeroblob(32),'s',2,'x')`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	report, err := Doctor(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if report.ForeignKeyIssues != 1 || len(report.ForeignKeyRows) != 1 {
		t.Fatalf("report=%#v", report)
	}
}

func TestDoctorRejectsCorruptDatabase(t *testing.T) {
	path := testPath(t)
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt(make([]byte, 32), 100); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Doctor(context.Background(), path); err == nil {
		t.Fatal("doctor accepted corrupt database")
	}
}

func TestConcurrentOpenAndDoctor(t *testing.T) {
	path := testPath(t)
	const n = 4
	var wg sync.WaitGroup
	wg.Add(n)
	errs := make(chan error, n)
	for range n {
		go func() {
			defer wg.Done()
			s, err := Open(context.Background(), path)
			if err == nil {
				err = s.Close()
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	report, err := Doctor(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Exists || !report.FilesystemSafe || report.ApplicationID != applicationID || !report.QueryOnly || len(report.Integrity) == 0 || !report.Migrations.Valid {
		t.Fatalf("report=%#v", report)
	}
}
