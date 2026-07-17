package stagestore

import (
	"context"
	"errors"
	"os"
	"strconv"
)

const doctorRowLimit = 20

type MigrationStatus struct {
	Applied, Latest int
	Valid           bool
}

type DoctorReport struct {
	Exists, FilesystemSafe                             bool
	ApplicationID                                      int
	Integrity                                          []string
	ForeignKeyIssues                                   int
	ForeignKeyRows                                     []string
	Migrations                                         MigrationStatus
	JournalMode                                        string
	Synchronous, SecureDelete                          int
	ForeignKeys, TrustedSchema, QueryOnly, WALFallback bool
	IntegrityTruncated, ForeignKeyTruncated            bool
	PermissionModelLimitations                         []string
}

// Doctor inspects an existing store without creating, migrating, or changing it.
func Doctor(ctx context.Context, path string) (DoctorReport, error) {
	report := DoctorReport{PermissionModelLimitations: permissionModelLimitations()}
	if !platformSupported() {
		return report, errors.New("stage store: unsupported platform")
	}
	if err := validateDatabasePath(path); err != nil {
		return report, err
	}
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return report, nil
	} else if err != nil {
		return report, errors.New("stage store: database unavailable")
	}
	report.Exists = true
	if err := validateExisting(path); err != nil {
		return report, err
	}
	report.FilesystemSafe = true
	s, err := OpenReadOnly(ctx, path)
	if err != nil {
		return report, err
	}
	defer s.Close()
	if err := s.db.QueryRowContext(ctx, "PRAGMA application_id").Scan(&report.ApplicationID); err != nil {
		return report, localError(err)
	}
	report.JournalMode, report.WALFallback = s.journalMode, s.journalFallback
	var foreignKeys, trustedSchema, queryOnly int
	for query, target := range map[string]any{"PRAGMA synchronous": &report.Synchronous, "PRAGMA secure_delete": &report.SecureDelete, "PRAGMA foreign_keys": &foreignKeys, "PRAGMA trusted_schema": &trustedSchema, "PRAGMA query_only": &queryOnly} {
		if err := s.db.QueryRowContext(ctx, query).Scan(target); err != nil {
			return report, localError(err)
		}
	}
	report.ForeignKeys, report.TrustedSchema, report.QueryOnly = foreignKeys == 1, trustedSchema == 1, queryOnly == 1
	rows, err := s.db.QueryContext(ctx, "PRAGMA integrity_check("+strconv.Itoa(doctorRowLimit+1)+")")
	if err != nil {
		return report, localError(err)
	}
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			rows.Close()
			return report, localError(err)
		}
		if len(report.Integrity) == doctorRowLimit {
			report.IntegrityTruncated = true
			continue
		}
		report.Integrity = append(report.Integrity, value)
	}
	if err := rows.Close(); err != nil {
		return report, localError(err)
	}
	rows, err = s.db.QueryContext(ctx, "SELECT `table`, rowid, parent, fkid FROM pragma_foreign_key_check LIMIT "+strconv.Itoa(doctorRowLimit+1))
	if err != nil {
		return report, localError(err)
	}
	for rows.Next() {
		var table string
		var rowid, parent, fkid any
		if err := rows.Scan(&table, &rowid, &parent, &fkid); err != nil {
			rows.Close()
			return report, localError(err)
		}
		report.ForeignKeyIssues++
		if len(report.ForeignKeyRows) == doctorRowLimit {
			report.ForeignKeyTruncated = true
			continue
		}
		report.ForeignKeyRows = append(report.ForeignKeyRows, table)
	}
	if err := rows.Close(); err != nil {
		return report, localError(err)
	}
	report.Migrations = MigrationStatus{Applied: len(migrations), Latest: migrations[len(migrations)-1].version, Valid: true}
	return report, nil
}
