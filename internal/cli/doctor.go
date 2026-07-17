package cli

import (
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ardasevinc/mattermost-cli/v2/internal/config"
	"github.com/ardasevinc/mattermost-cli/v2/internal/doctor"
	"github.com/ardasevinc/mattermost-cli/v2/internal/output"
	"github.com/ardasevinc/mattermost-cli/v2/internal/presentation"
)

func newDoctorCommand(state *rootState) *cobra.Command {
	var redactOverride *bool
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check configuration, server health, and authentication",
		Args:  cobra.NoArgs,
		PreRunE: func(cmd *cobra.Command, _ []string) error {
			var err error
			redactOverride, err = state.redactOption(cmd)
			return err
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			resolved, warning := state.resolveForDoctor(redactOverride)
			if warning != "" {
				if state.flags.json {
					state.queueMachineWarning("warning: " + warning + "\n")
				} else if err := writeAll(state.streams.err, []byte("warning: "+warning+"\n")); err != nil {
					return err
				}
			}
			report := doctor.Run(cmd.Context(), resolved, func(baseURL, token string) (doctor.Transport, func(), error) {
				client, err := state.deps.newClient(baseURL, token)
				if err != nil {
					return nil, nil, err
				}
				return client, client.Close, nil
			})
			if !report.OK {
				state.setSemanticExit(3)
			}
			return writeDoctorReport(state, report)
		},
	}
}

func (s *rootState) resolveForDoctor(redactOverride *bool) (config.Resolved, string) {
	var file config.FileState
	paths, err := s.configPaths()
	if err != nil {
		file.Error = config.FileErrorRead
	} else {
		file = config.Load(paths)
	}
	if file.Config.Token != "" {
		s.releases = append(s.releases, presentation.ActiveCredentials.Register(file.Config.Token))
		s.credentials = append(s.credentials, file.Config.Token)
	}
	resolved := config.Resolve(config.Options{URL: s.flags.url, Token: s.flags.token, Redact: redactOverride}, s.deps.lookupEnv, file)
	s.disableHeuristics = !resolved.Redact
	return resolved, s.presentConfigLabel(file.Warning())
}

func writeDoctorReport(state *rootState, report doctor.Report) error {
	if state.flags.json {
		checks := make([]output.DoctorCheck, len(report.Checks))
		for index, check := range report.Checks {
			checks[index] = output.DoctorCheck{Name: check.Name, Status: string(check.Status), Message: check.Message, Details: check.Details}
		}
		document, err := output.NewDoctorEnvelope(report.OK, checks)
		if err != nil {
			return outputError{err: err}
		}
		if _, err := output.WriteMachineJSON(state.streams.out, document); err != nil {
			return outputError{err: err}
		}
		return nil
	}
	lines := make([]string, len(report.Checks))
	for index, check := range report.Checks {
		details := doctorDetails(check)
		lines[index] = formatDoctorStatus(check.Status) + " " + check.Name + ": " + check.Message + details
	}
	return writeAll(state.streams.out, []byte(strings.Join(lines, "\n")+"\n"))
}

func doctorDetails(check doctor.Check) string {
	if len(check.Details) == 0 {
		return ""
	}
	keys := []string{"urlSource", "tokenSource", "status", "databaseStatus", "filestoreStatus", "httpStatus", "id", "username"}
	values := make([]string, 0, len(check.Details))
	for _, key := range keys {
		if value, ok := check.Details[key]; ok {
			values = append(values, key+"="+presentation.SanitizeLabel(strings.TrimSpace(toString(value))))
		}
	}
	return " (" + strings.Join(values, ", ") + ")"
}

func toString(value any) string {
	switch value := value.(type) {
	case string:
		return value
	case config.Source:
		return string(value)
	case int:
		return strconv.Itoa(value)
	default:
		return "unknown"
	}
}

func formatDoctorStatus(status doctor.Status) string {
	value := string(status)
	return value + strings.Repeat(" ", 7-len(value))
}
