package cli

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/spf13/cobra"

	"github.com/ardasevinc/mattermost-cli/internal/config"
	"github.com/ardasevinc/mattermost-cli/internal/output"
	"github.com/ardasevinc/mattermost-cli/internal/presentation"
	"github.com/ardasevinc/mattermost-cli/internal/stagestore"
)

type storeDoctorEnvelope struct {
	Schema string            `json:"schema"`
	Path   string            `json:"path"`
	Report storeDoctorReport `json:"report"`
}

type storeDoctorReport struct {
	Exists                     bool                 `json:"exists"`
	FilesystemSafe             *bool                `json:"filesystemSafe"`
	ApplicationID              *int                 `json:"applicationId"`
	Integrity                  []string             `json:"integrity"`
	IntegrityTruncated         *bool                `json:"integrityTruncated"`
	ForeignKeyIssues           *int                 `json:"foreignKeyIssues"`
	ForeignKeyRows             []string             `json:"foreignKeyRows"`
	ForeignKeyTruncated        *bool                `json:"foreignKeyTruncated"`
	Migrations                 storeMigrationStatus `json:"migrations"`
	JournalMode                *string              `json:"journalMode"`
	Synchronous                *int                 `json:"synchronous"`
	SecureDelete               *int                 `json:"secureDelete"`
	ForeignKeys                *bool                `json:"foreignKeys"`
	TrustedSchema              *bool                `json:"trustedSchema"`
	QueryOnly                  *bool                `json:"queryOnly"`
	WALFallback                *bool                `json:"walFallback"`
	PermissionModelLimitations []string             `json:"permissionModelLimitations"`
}

type storeMigrationStatus struct {
	Applied *int  `json:"applied"`
	Latest  int   `json:"latest"`
	Valid   *bool `json:"valid"`
}

type storeMigrationsEnvelope struct {
	Schema     string                     `json:"schema"`
	Latest     int                        `json:"latest"`
	Migrations []storeMigrationDescriptor `json:"migrations"`
}

type storeMigrationDescriptor struct {
	Version  int    `json:"version"`
	Name     string `json:"name"`
	Checksum string `json:"checksum"`
}

func newStoreCommand(state *rootState) *cobra.Command {
	command := &cobra.Command{Use: "store", Short: "Inspect local stage storage", Args: cobra.NoArgs,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error { return resolveStoreRedaction(state, cmd) },
		RunE: func(cmd *cobra.Command, _ []string) error {
			if state.flags.json {
				return invalidFailure("--json requires a store inspection subcommand")
			}
			return cmd.Help()
		}}
	command.AddCommand(&cobra.Command{
		Use: "doctor", Short: "Inspect local stage storage without changing it", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			paths, err := storePaths(state)
			if err != nil {
				return err
			}
			report, err := stagestore.Doctor(cmd.Context(), paths.DBPath)
			if err != nil {
				return readFailure(err)
			}
			if report.Exists && (!report.Migrations.Valid || len(report.Integrity) != 1 || report.Integrity[0] != "ok" || report.IntegrityTruncated || report.ForeignKeyIssues > 0) {
				state.setSemanticExit(6)
			}
			return writeStoreDoctor(state, paths.DBPath, report)
		},
	})
	command.AddCommand(&cobra.Command{
		Use: "migrations", Short: "List migrations compiled into mm", Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error { return writeStoreMigrations(state, stagestore.Migrations()) },
	})
	return command
}

func resolveStoreRedaction(state *rootState, cmd *cobra.Command) error {
	override, err := state.redactOption(cmd)
	if err != nil {
		return err
	}
	var file config.FileState
	home, homeErr := state.deps.homeDir()
	if homeErr == nil {
		if paths, pathErr := config.ResolvePaths(home, state.deps.lookupEnv); pathErr == nil {
			file = config.Load(paths)
			if file.Config.Token != "" && !slices.Contains(state.credentials, file.Config.Token) {
				state.releases = append(state.releases, presentation.ActiveCredentials.Register(file.Config.Token))
				state.credentials = append(state.credentials, file.Config.Token)
			}
			file.Config.Token = ""
			file.Config.URL = ""
		}
	}
	resolved := config.Resolve(config.Options{Redact: override}, state.deps.lookupEnv, file)
	state.disableHeuristics = !resolved.Redact
	return nil
}

func storePaths(state *rootState) (stagestore.Paths, error) {
	home, err := state.deps.homeDir()
	if err != nil {
		return stagestore.Paths{}, configFailure("could not resolve the home directory")
	}
	paths, err := stagestore.ResolvePaths(home, func(key string) (string, bool) { return state.deps.lookupEnv(key) })
	if err != nil {
		return stagestore.Paths{}, configFailure(err.Error())
	}
	return paths, nil
}

func safeStoreValue(state *rootState, value string) string {
	return presentation.SanitizeLabel(presentation.PreprocessWithOptions(value, presentation.Options{Credentials: state.credentials, DisableHeuristics: state.disableHeuristics}).Text)
}

func writeStoreDoctor(state *rootState, path string, report stagestore.DoctorReport) error {
	safe := func(values []string) []string {
		result := make([]string, len(values))
		for i, value := range values {
			result[i] = safeStoreValue(state, value)
		}
		return result
	}
	compiled := stagestore.Migrations()
	latest := compiled[len(compiled)-1].Version
	document := storeDoctorEnvelope{Schema: "mm/v2/store-doctor", Path: safeStoreValue(state, path), Report: storeDoctorReport{Exists: report.Exists,
		Migrations: storeMigrationStatus{Latest: latest}, PermissionModelLimitations: safe(report.PermissionModelLimitations)}}
	if report.Exists {
		document.Report.FilesystemSafe, document.Report.ApplicationID = pointer(report.FilesystemSafe), pointer(report.ApplicationID)
		document.Report.Integrity, document.Report.IntegrityTruncated = safe(report.Integrity), pointer(report.IntegrityTruncated)
		document.Report.ForeignKeyIssues, document.Report.ForeignKeyRows, document.Report.ForeignKeyTruncated = pointer(report.ForeignKeyIssues), safe(report.ForeignKeyRows), pointer(report.ForeignKeyTruncated)
		document.Report.Migrations.Applied, document.Report.Migrations.Valid = pointer(report.Migrations.Applied), pointer(report.Migrations.Valid)
		journal := safeStoreValue(state, report.JournalMode)
		document.Report.JournalMode = &journal
		document.Report.Synchronous, document.Report.SecureDelete = pointer(report.Synchronous), pointer(report.SecureDelete)
		document.Report.ForeignKeys, document.Report.TrustedSchema, document.Report.QueryOnly, document.Report.WALFallback = pointer(report.ForeignKeys), pointer(report.TrustedSchema), pointer(report.QueryOnly), pointer(report.WALFallback)
	}
	if state.flags.json {
		return writeStoreJSON(state, document)
	}
	lines := []string{"path: " + document.Path, "exists: " + strconv.FormatBool(document.Report.Exists), "compiled latest migration: " + strconv.Itoa(document.Report.Migrations.Latest)}
	if report.Exists {
		lines = append(lines, "filesystem safe: "+strconv.FormatBool(report.FilesystemSafe), "integrity: "+strings.Join(document.Report.Integrity, ", "),
			"integrity truncated: "+strconv.FormatBool(report.IntegrityTruncated), "foreign key issues: "+strconv.Itoa(report.ForeignKeyIssues),
			"foreign key rows truncated: "+strconv.FormatBool(report.ForeignKeyTruncated),
			"migrations: "+strconv.Itoa(report.Migrations.Applied)+"/"+strconv.Itoa(report.Migrations.Latest)+" valid="+strconv.FormatBool(report.Migrations.Valid),
			"journal mode: "+safeStoreValue(state, report.JournalMode)+" fallback="+strconv.FormatBool(report.WALFallback),
			"guards: foreign_keys="+strconv.FormatBool(report.ForeignKeys)+" trusted_schema="+strconv.FormatBool(report.TrustedSchema)+" query_only="+strconv.FormatBool(report.QueryOnly),
			"durability: synchronous="+strconv.Itoa(report.Synchronous)+" secure_delete="+strconv.Itoa(report.SecureDelete))
	} else {
		lines = append(lines, "store facts: not applicable (store absent)")
	}
	for _, limitation := range document.Report.PermissionModelLimitations {
		lines = append(lines, "limitation: "+limitation)
	}
	return writeAll(state.streams.out, []byte(strings.Join(lines, "\n")+"\n"))
}

func writeStoreMigrations(state *rootState, migrations []stagestore.MigrationInfo) error {
	if len(migrations) == 0 {
		return internalFailure(fmt.Errorf("compiled migration set is empty"))
	}
	document := storeMigrationsEnvelope{Schema: "mm/v2/store-migrations", Migrations: make([]storeMigrationDescriptor, len(migrations))}
	for i, item := range migrations {
		if item.Version != i+1 || (i > 0 && item.Version <= migrations[i-1].Version) {
			return internalFailure(fmt.Errorf("compiled migration order is invalid"))
		}
		if !safeMigrationName(item.Name) || !lowerHexDigest(item.Checksum) {
			return internalFailure(fmt.Errorf("compiled migration metadata is invalid"))
		}
		for _, credential := range state.credentials {
			if credential != "" && (strings.Contains(item.Name, credential) || strings.Contains(item.Checksum, credential)) {
				return internalFailure(fmt.Errorf("compiled migration metadata conflicts with an active credential"))
			}
		}
		document.Migrations[i] = storeMigrationDescriptor{Version: item.Version, Name: item.Name, Checksum: item.Checksum}
		document.Latest = item.Version
	}
	if state.flags.json {
		return writeStoreJSON(state, document)
	}
	lines := []string{"latest: " + strconv.Itoa(document.Latest)}
	for _, item := range document.Migrations {
		lines = append(lines, strconv.Itoa(item.Version)+" "+item.Name+" "+item.Checksum)
	}
	return writeAll(state.streams.out, []byte(strings.Join(lines, "\n")+"\n"))
}

func safeMigrationName(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r > unicode.MaxASCII || !(unicode.IsLower(r) || unicode.IsDigit(r) || r == '-') {
			return false
		}
	}
	return true
}

func lowerHexDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

func writeStoreJSON(state *rootState, document any) error {
	_, err := output.WriteBoundedJSON(state.streams.out, document)
	if err != nil {
		return outputError{err: err}
	}
	return nil
}
