package stagestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	sqlite3 "modernc.org/sqlite"
)

const (
	applicationID = 0x4d4d5632
	busyMillis    = 5000
	driverBusyMS  = 25
)

var (
	ErrBusy             = errors.New("stage store: database busy")
	ErrIdentity         = errors.New("stage store: database identity mismatch")
	ErrMigration        = errors.New("stage store: migration state invalid")
	ErrUnsafeFilesystem = errors.New("stage store: unsafe filesystem")
	setJournalMode      = configureWAL
)

type Store struct {
	db              *sql.DB
	path            string
	journalMode     string
	journalFallback bool
	unlock          func()
	closeOnce       sync.Once
	closeErr        error
}

func Open(ctx context.Context, path string) (*Store, error) {
	if err := validateDatabasePath(path); err != nil {
		return nil, err
	}
	bootstrap, createdNow, unlock, err := prepareWritable(ctx, path)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", sqliteURI(path, false))
	if err != nil {
		unlock()
		return nil, localError(err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &Store{db: db, path: path, unlock: unlock}
	if err := s.initializeBounded(ctx, bootstrap); err != nil {
		_ = db.Close()
		if createdNow {
			removeFailedNewDatabase(path)
		}
		unlock()
		return nil, err
	}
	if err := s.verifyIdentityAndMigrations(ctx); err != nil {
		_ = db.Close()
		if createdNow {
			removeFailedNewDatabase(path)
		}
		unlock()
		return nil, err
	}
	if err := clearBootstrapPending(path); err != nil {
		_ = db.Close()
		unlock()
		return nil, err
	}
	if err := validateSidecars(path); err != nil {
		_ = db.Close()
		unlock()
		return nil, err
	}
	return s, nil
}

func (s *Store) initializeBounded(ctx context.Context, created bool) error {
	deadline := time.Now().Add(busyMillis * time.Millisecond)
	for {
		err := s.initialize(ctx, created)
		if !errors.Is(err, ErrBusy) || !time.Now().Before(deadline) {
			return err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func OpenReadOnly(ctx context.Context, path string) (*Store, error) {
	if err := validateDatabasePath(path); err != nil {
		return nil, err
	}
	unlock, err := lockExisting(ctx, path)
	if err != nil {
		return nil, err
	}
	failed := true
	defer func() {
		if failed {
			unlock()
		}
	}()
	if err := validateExisting(path); err != nil {
		return nil, err
	}
	if err := validateImmutableRead(path); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", sqliteURI(path, true))
	if err != nil {
		return nil, localError(err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &Store{db: db, path: path, unlock: unlock}
	var queryOnly int
	if err := db.QueryRowContext(ctx, "PRAGMA query_only").Scan(&queryOnly); err != nil || queryOnly != 1 {
		_ = db.Close()
		return nil, fmt.Errorf("stage store: read-only guard unavailable")
	}
	if err := s.verifyIdentityAndMigrations(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&s.journalMode); err != nil {
		_ = db.Close()
		return nil, localError(err)
	}
	s.journalMode = strings.ToLower(s.journalMode)
	if s.journalMode != "wal" && !isLocalRollbackJournal(s.journalMode) {
		_ = db.Close()
		return nil, errors.New("stage store: unsupported journal mode")
	}
	s.journalFallback = s.journalMode != "wal"
	if err := validateExisting(path); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := validateImmutableRead(path); err != nil {
		_ = db.Close()
		return nil, err
	}
	failed = false
	return s, nil
}

func (s *Store) Close() error {
	s.closeOnce.Do(func() {
		s.closeErr = s.db.Close()
		if s.unlock != nil {
			s.unlock()
		}
	})
	return s.closeErr
}

func sqliteURI(path string, readOnly bool) string {
	u := &url.URL{Scheme: "file", Path: path}
	q := u.Query()
	q.Set("_dqs", "0")
	if readOnly {
		q.Set("mode", "ro")
		q.Set("immutable", "1")
		q.Add("_pragma", "query_only(1)")
	} else {
		q.Set("mode", "rw")
		q.Set("_txlock", "immediate")
	}
	q.Add("_pragma", "busy_timeout("+strconv.Itoa(driverBusyMS)+")")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "trusted_schema(0)")
	q.Add("_pragma", "secure_delete(FAST)")
	q.Add("_pragma", "synchronous(FULL)")
	u.RawQuery = q.Encode()
	return u.String()
}

func (s *Store) initialize(ctx context.Context, created bool) error {
	for _, statement := range []string{
		"PRAGMA foreign_keys = ON", "PRAGMA trusted_schema = OFF",
		"PRAGMA secure_delete = FAST", "PRAGMA synchronous = FULL",
	} {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return localError(err)
		}
	}
	mode, err := setJournalMode(ctx, s.db)
	if err != nil {
		return localError(err)
	}
	s.journalMode = mode
	s.journalMode = strings.ToLower(s.journalMode)
	if s.journalMode != "wal" {
		if !isLocalRollbackJournal(s.journalMode) {
			return fmt.Errorf("stage store: unsupported journal mode")
		}
		s.journalFallback = true
	}
	return s.migrate(ctx, created)
}

func configureWAL(ctx context.Context, db *sql.DB) (string, error) {
	var mode string
	err := db.QueryRowContext(ctx, "PRAGMA journal_mode = WAL").Scan(&mode)
	return mode, err
}

func isLocalRollbackJournal(mode string) bool {
	return mode == "delete" || mode == "truncate" || mode == "persist"
}

func (s *Store) migrate(ctx context.Context, created bool) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return localError(err)
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return localError(err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	var id int
	if err := conn.QueryRowContext(ctx, "PRAGMA application_id").Scan(&id); err != nil {
		return localError(err)
	}
	if created {
		if id != 0 && id != applicationID {
			return ErrIdentity
		}
		if id == 0 {
			var schemaObjects, userVersion int
			if err := conn.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema").Scan(&schemaObjects); err != nil {
				return localError(err)
			}
			if err := conn.QueryRowContext(ctx, "PRAGMA user_version").Scan(&userVersion); err != nil {
				return localError(err)
			}
			if schemaObjects != 0 || userVersion != 0 {
				return ErrIdentity
			}
			if _, err := conn.ExecContext(ctx, fmt.Sprintf("PRAGMA application_id = %d", applicationID)); err != nil {
				return localError(err)
			}
		}
	} else if id != applicationID {
		return ErrIdentity
	}
	if _, err = conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
version INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE, checksum TEXT NOT NULL, applied_at TEXT NOT NULL
) STRICT`); err != nil {
		return localError(err)
	}
	rows, err := conn.QueryContext(ctx, "SELECT version, name, checksum FROM schema_migrations ORDER BY version")
	if err != nil {
		return localError(err)
	}
	type applied struct {
		version        int
		name, checksum string
	}
	var got []applied
	for rows.Next() {
		var a applied
		if err := rows.Scan(&a.version, &a.name, &a.checksum); err != nil {
			rows.Close()
			return localError(err)
		}
		got = append(got, a)
	}
	if err := rows.Close(); err != nil {
		return localError(err)
	}
	if len(got) > len(migrations) {
		return ErrMigration
	}
	for i, a := range got {
		m := migrations[i]
		if a.version != m.version || a.name != m.name || a.checksum != m.checksum() {
			return ErrMigration
		}
	}
	for _, m := range migrations[len(got):] {
		if _, err = conn.ExecContext(ctx, m.sql); err != nil {
			return fmt.Errorf("%w: apply failed", ErrMigration)
		}
		if _, err = conn.ExecContext(ctx, "INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(?,?,?,?)", m.version, m.name, m.checksum(), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return localError(err)
		}
	}
	if _, err = conn.ExecContext(ctx, "COMMIT"); err != nil {
		return localError(err)
	}
	committed = true
	return nil
}

func (s *Store) verifyIdentityAndMigrations(ctx context.Context) error {
	var id int
	if err := s.db.QueryRowContext(ctx, "PRAGMA application_id").Scan(&id); err != nil {
		return localError(err)
	}
	if id != applicationID {
		return ErrIdentity
	}
	rows, err := s.db.QueryContext(ctx, "SELECT version,name,checksum FROM schema_migrations ORDER BY version")
	if err != nil {
		return ErrMigration
	}
	defer rows.Close()
	i := 0
	for rows.Next() {
		if i >= len(migrations) {
			return ErrMigration
		}
		var version int
		var name, checksum string
		if err := rows.Scan(&version, &name, &checksum); err != nil {
			return localError(err)
		}
		m := migrations[i]
		if version != m.version || name != m.name || checksum != m.checksum() {
			return ErrMigration
		}
		i++
	}
	if i != len(migrations) || rows.Err() != nil {
		return ErrMigration
	}
	return nil
}

func localError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var sqliteErr *sqlite3.Error
	if errors.As(err, &sqliteErr) && (sqliteErr.Code()&0xff == 5 || sqliteErr.Code()&0xff == 6) {
		return ErrBusy
	}
	return fmt.Errorf("stage store: local database failure")
}
