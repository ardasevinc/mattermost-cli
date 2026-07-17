package cli

import (
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ardasevinc/mattermost-cli/v2/internal/config"
	"github.com/ardasevinc/mattermost-cli/v2/internal/output"
	"github.com/ardasevinc/mattermost-cli/v2/internal/presentation"
)

type configFlags struct {
	path bool
	init bool
}

func newConfigCommand(state *rootState) *cobra.Command {
	var flags configFlags
	command := &cobra.Command{
		Use:   "config",
		Short: "Inspect or initialize configuration",
		Args:  cobra.NoArgs,
		PreRunE: func(cmd *cobra.Command, _ []string) error {
			if flags.path && flags.init {
				return invalidFailure("--path and --init cannot be used together")
			}
			_, err := state.redactOption(cmd)
			return err
		},
		RunE: func(_ *cobra.Command, _ []string) error {
			paths, err := state.configPaths()
			if err != nil {
				return err
			}
			if flags.path {
				status := state.presentConfigStatus(output.ConfigEnvelope{
					Schema: "mm/v2/config", Action: "path", SelectedPath: paths.ConfigPath,
				})
				return writeConfigStatus(state, status)
			}

			action := "status"
			var created *bool
			if flags.init {
				action = "init"
				value, initErr := config.Init(paths.ConfigPath)
				if initErr != nil {
					return configFailure(initErr.Error())
				}
				created = &value
			}
			file := config.Load(paths)
			status := state.presentConfigStatus(configMachineStatus(action, file, created))
			if file.Error != "" || file.Unsafe != "" || file.WritableByOthers || (file.InsecurePermissions && file.Config.Token != "") {
				state.setSemanticExit(3)
			}
			return writeConfigStatus(state, status)
		},
	}
	command.Flags().BoolVar(&flags.path, "path", false, "print the selected v2 config path")
	command.Flags().BoolVar(&flags.init, "init", false, "create a secure config template without overwriting")
	return command
}

func (s *rootState) configPaths() (config.Paths, error) {
	home, err := s.deps.homeDir()
	if err != nil {
		return config.Paths{}, configFailure("could not resolve the home directory")
	}
	paths, err := config.ResolvePaths(home, s.deps.lookupEnv)
	if err != nil {
		return config.Paths{}, configFailure(err.Error())
	}
	return paths, nil
}

func configMachineStatus(action string, file config.FileState, created *bool) output.ConfigEnvelope {
	migration := string(file.Migration)
	if migration == "" {
		migration = string(config.MigrationNone)
	}
	readStatus, parseStatus, permissions := "ok", "ok", "secure"
	if !file.Exists {
		readStatus, parseStatus, permissions = "missing", "not_attempted", "not_applicable"
	} else if file.Error == config.FileErrorRead {
		readStatus, parseStatus = "error", "not_attempted"
		permissions = "unknown"
	} else if file.Error == config.FileErrorParse {
		parseStatus = "error"
	}
	if file.InsecurePermissions {
		permissions = "insecure"
	}
	var stageTTLSeconds, stagePruneAfterSeconds *int64
	if file.Error != config.FileErrorParse && file.Error != config.FileErrorRead {
		stageTTLSeconds = pointer(file.Config.StageTTLSeconds)
		stagePruneAfterSeconds = pointer(file.Config.StagePruneAfterSeconds)
	}
	var readPath *string
	if file.Exists {
		readPathValue := file.ReadPath
		readPath = &readPathValue
	}
	var unsafeReason *string
	if file.Unsafe != "" {
		value := string(file.Unsafe)
		unsafeReason = &value
	}
	var warning *string
	if value := file.Warning(); value != "" {
		warning = &value
	}
	return output.ConfigEnvelope{
		Schema: "mm/v2/config", Action: action, SelectedPath: file.SelectedPath, ReadPath: readPath,
		Migration: pointer(migration), Exists: pointer(file.Exists), URLConfigured: pointer(file.Config.URL != ""),
		TokenConfigured: pointer(file.Config.Token != ""), StageTTLSeconds: stageTTLSeconds,
		StagePruneAfterSeconds: stagePruneAfterSeconds, Permissions: pointer(permissions), ReadStatus: pointer(readStatus),
		ParseStatus: pointer(parseStatus), UnsafeReason: unsafeReason, Created: created, Warning: warning,
	}
}

func (s *rootState) presentConfigStatus(status output.ConfigEnvelope) output.ConfigEnvelope {
	status.SelectedPath = s.presentConfigLabel(status.SelectedPath)
	if status.ReadPath != nil {
		value := s.presentConfigLabel(*status.ReadPath)
		status.ReadPath = &value
	}
	if status.Warning != nil {
		value := s.presentConfigLabel(*status.Warning)
		status.Warning = &value
	}
	return status
}

func (s *rootState) presentConfigLabel(value string) string {
	processed := presentation.PreprocessWithOptions(value, presentation.Options{
		Credentials: s.credentials, DisableHeuristics: s.disableHeuristics,
	})
	return presentation.SanitizeLabel(processed.Text)
}

func writeConfigStatus(state *rootState, status output.ConfigEnvelope) error {
	if state.flags.json {
		if _, err := output.WriteMachineJSON(state.streams.out, status); err != nil {
			return outputError{err: err}
		}
		return nil
	}
	return writeConfigHuman(state, status)
}

func writeConfigHuman(state *rootState, status output.ConfigEnvelope) error {
	if status.Action == "path" {
		return writeAll(state.streams.out, []byte(status.SelectedPath+"\n"))
	}
	if status.Warning != nil {
		if err := writeAll(state.streams.err, []byte("warning: "+*status.Warning+"\n")); err != nil {
			return err
		}
	}
	if status.Action == "init" {
		verb := "Config file already exists: "
		if status.Created != nil && *status.Created {
			verb = "Created config file: "
		}
		return writeAll(state.streams.out, []byte(verb+status.SelectedPath+"\n"))
	}
	readPath := "none"
	if status.ReadPath != nil {
		readPath = *status.ReadPath
	}
	unsafe := "none"
	if status.UnsafeReason != nil {
		unsafe = *status.UnsafeReason
	}
	lines := []string{
		"Selected config path: " + status.SelectedPath,
		"Effective read path: " + readPath,
		"Migration: " + valueOr(status.Migration, "none"),
		"Exists: " + yesNo(valueOr(status.Exists, false)),
		"URL configured: " + yesNo(valueOr(status.URLConfigured, false)),
		"Token configured: " + yesNo(valueOr(status.TokenConfigured, false)),
		"Stage TTL seconds: " + optionalInt64(status.StageTTLSeconds),
		"Stage prune after seconds: " + optionalInt64(status.StagePruneAfterSeconds),
		"Permissions: " + valueOr(status.Permissions, "not_applicable"),
		"Read status: " + valueOr(status.ReadStatus, "not_attempted"),
		"Parse status: " + valueOr(status.ParseStatus, "not_attempted"),
		"Unsafe reason: " + unsafe,
	}
	return writeAll(state.streams.out, []byte(strings.Join(lines, "\n")+"\n"))
}

func pointer[T any](value T) *T { return &value }

func valueOr[T any](value *T, fallback T) T {
	if value != nil {
		return *value
	}
	return fallback
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

func optionalInt64(value *int64) string {
	if value == nil {
		return "unknown"
	}
	return strconv.FormatInt(*value, 10)
}
