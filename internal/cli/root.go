package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ardasevinc/mattermost-cli/v2/internal/buildinfo"
	"github.com/ardasevinc/mattermost-cli/v2/internal/output"
	"github.com/ardasevinc/mattermost-cli/v2/internal/presentation"
	mmSchema "github.com/ardasevinc/mattermost-cli/v2/internal/schema"
)

type streams struct {
	in  io.Reader
	out io.Writer
	err io.Writer
}

func Execute(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) int {
	trackedOut := &writeTracker{writer: out}
	s := streams{in: in, out: trackedOut, err: errOut}
	deps := defaultDependencies(out)
	credentials := append([]string{os.Getenv("MM_TOKEN")}, earlyTokens(args)...)
	credentials = append(credentials, bestEffortFileToken(deps))
	state := &rootState{streams: s, deps: deps, credentials: credentials}
	for _, credential := range credentials {
		state.releases = append(state.releases, presentation.ActiveCredentials.Register(credential))
	}
	defer state.releaseCredentials()
	defer state.close()
	cmd := newRootWithState(state)
	state.flags.json = earlyStructuredMachineMode(args)
	cmd.SetArgs(args)
	if err := cmd.ExecuteContext(ctx); err != nil {
		var corrupted watchOutputFailure
		if errors.As(err, &corrupted) {
			return 3
		}
		var terminal watchTerminalFailure
		if errors.As(err, &terminal) {
			return 3
		}
		message := presentation.SanitizeLabel(presentation.Preprocess(err.Error(), state.credentials).Text)
		code := exitCode(err)
		if state.flags.json && trackedOut.BytesWritten() > 0 {
			if code >= 4 {
				return code
			}
			return 3
		}
		if state.flags.json {
			document := output.ErrorEnvelope{Schema: "mm/v2/error", Code: machineErrorCode(err), Message: message, ExitCode: code, StageRef: machineStageRef(err), Recovery: machineRecovery(err)}
			if _, writeErr := output.WriteMachineJSON(errOut, document); writeErr != nil {
				if code >= 4 {
					return code
				}
				return 3
			}
		} else if writeErr := writeAll(errOut, []byte(fmt.Sprintf("error: %s\n", message))); writeErr != nil {
			if code >= 4 {
				return code
			}
			return 3
		}
		if trackedOut.Failed() {
			if code >= 4 {
				return code
			}
			return 3
		}
		return code
	}
	if trackedOut.Failed() {
		return 3
	}
	if state.flags.json {
		if err := state.flushMachineWarnings(); err != nil {
			return 3
		}
	}
	return state.semanticExitCode()
}

func earlyStructuredMachineMode(args []string) bool {
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if arg == "--" {
			return false
		}
		if arg == "--url" || arg == "--token" || arg == "-t" {
			index++
			continue
		}
		if strings.HasPrefix(arg, "--url=") || strings.HasPrefix(arg, "--token=") || strings.HasPrefix(arg, "-t") || strings.HasPrefix(arg, "-") {
			continue
		}
		if arg != "apply" {
			return false
		}
		for _, applyArg := range args[index+1:] {
			if applyArg == "--" {
				return false
			}
			if applyArg == "--from-json" {
				return true
			}
			if strings.HasPrefix(applyArg, "--from-json=") {
				value := strings.TrimPrefix(applyArg, "--from-json=")
				switch value {
				case "0", "f", "F", "false", "FALSE", "False":
					return false
				default:
					return true
				}
			}
		}
		return false
	}
	return false
}

func machineErrorCode(err error) string {
	var applyFailure applyCommandFailure
	if errors.As(err, &applyFailure) {
		return applyFailure.code
	}
	var outputFailure outputError
	if errors.As(err, &outputFailure) {
		return "internal"
	}
	var classified classifiedError
	if errors.As(err, &classified) {
		return classified.code
	}
	var operation operationFailure
	if errors.As(err, &operation) {
		return operation.code
	}
	var local localStateFailure
	if errors.As(err, &local) {
		return "state_conflict"
	}
	return "invalid_invocation"
}

func machineRecovery(err error) string {
	var applyFailure applyCommandFailure
	if errors.As(err, &applyFailure) {
		return applyFailure.recovery
	}
	return "none"
}

func machineStageRef(err error) string {
	var applyFailure applyCommandFailure
	if errors.As(err, &applyFailure) {
		return applyFailure.stageRef
	}
	return ""
}

func newRoot(s streams) *cobra.Command {
	return newRootWithState(&rootState{streams: s, deps: defaultDependencies(s.out)})
}

func newRootWithState(state *rootState) *cobra.Command {
	s := state.streams
	cmd := &cobra.Command{
		Use:           "mm",
		Short:         "Mattermost CLI for agents and humans",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       buildinfo.Version + " (" + buildinfo.Commit + ")",
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
		CompletionOptions: cobra.CompletionOptions{
			DisableDefaultCmd: true,
		},
	}
	cmd.SetIn(s.in)
	cmd.SetOut(s.out)
	cmd.SetErr(s.err)
	cmd.PersistentFlags().StringVar(&state.flags.url, "url", "", "Mattermost server URL")
	cmd.PersistentFlags().StringVarP(&state.flags.token, "token", "t", "", "Mattermost personal access token")
	cmd.PersistentFlags().BoolVar(&state.flags.redact, "redact", true, "redact detected secrets")
	cmd.PersistentFlags().BoolVar(&state.flags.noRedact, "no-redact", false, "disable heuristic secret redaction")
	cmd.PersistentFlags().BoolVar(&state.flags.json, "json", false, "output a versioned JSON document")
	cmd.PersistentFlags().BoolVar(&state.flags.noColor, "no-color", false, "disable colored output")
	cmd.PersistentFlags().BoolVarP(&state.flags.relative, "relative", "r", false, "show relative times")
	cmd.PersistentFlags().BoolVar(&state.flags.noRelative, "no-relative", false, "show absolute times")
	cmd.PersistentFlags().BoolVar(&state.flags.threads, "threads", true, "show visible thread structure")
	cmd.PersistentFlags().BoolVar(&state.flags.noThreads, "no-threads", false, "return selected seed posts only")
	cmd.AddCommand(newSchemaCommand(state))
	cmd.AddCommand(newStoreCommand(state))
	cmd.AddCommand(newStageCommand(state))
	cmd.AddCommand(newApplyCommand(state))
	cmd.AddCommand(newConfigCommand(state))
	cmd.AddCommand(newDoctorCommand(state))
	cmd.AddCommand(newWhoAmICommand(state))
	cmd.AddCommand(newTeamsCommand(state))
	cmd.AddCommand(newUsersCommand(state))
	cmd.AddCommand(newChannelsCommand(state))
	cmd.AddCommand(newChannelCommand(state))
	cmd.AddCommand(newDMsCommand(state))
	cmd.AddCommand(newGroupDMsCommand(state))
	cmd.AddCommand(newThreadCommand(state))
	cmd.AddCommand(newSearchCommand(state))
	cmd.AddCommand(newMentionsCommand(state))
	cmd.AddCommand(newUnreadCommand(state))
	cmd.AddCommand(newWatchCommand(state))
	return cmd
}

func newSchemaCommand(state *rootState) *cobra.Command {
	s := state.streams
	command := &cobra.Command{
		Use:   "schema",
		Short: "Inspect and validate machine schemas",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
		PersistentPreRunE: func(_ *cobra.Command, _ []string) error {
			if state.flags.json {
				return invalidFailure("--json is not supported by schema inspection commands")
			}
			return nil
		},
	}
	command.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List embedded schema identifiers",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			registry, err := mmSchema.Load()
			if err != nil {
				return internalFailure(err)
			}
			return writeAll(s.out, []byte(strings.Join(registry.IDs(), "\n")+"\n"))
		},
	})
	command.AddCommand(&cobra.Command{
		Use:   "show <schema-id>",
		Short: "Print one embedded JSON Schema",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			registry, err := mmSchema.Load()
			if err != nil {
				return internalFailure(err)
			}
			data, err := registry.Show(args[0])
			if err != nil {
				return invalidFailure(err.Error())
			}
			if err := writeAll(s.out, data); err != nil {
				return err
			}
			if len(data) == 0 || data[len(data)-1] != '\n' {
				err = writeAll(s.out, []byte("\n"))
			}
			return err
		},
	})
	command.AddCommand(&cobra.Command{
		Use:   "validate <schema-id>",
		Short: "Validate one JSON document from stdin",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			registry, err := mmSchema.Load()
			if err != nil {
				return internalFailure(err)
			}
			if err := registry.Validate(args[0], s.in); err != nil {
				if mmSchema.IsInputReadError(err) {
					return readFailure(err)
				}
				return invalidFailure(err.Error())
			}
			return writeAll(s.out, []byte("valid: "+args[0]+"\n"))
		},
	})
	return command
}

type outputError struct {
	err error
}

type writeTracker struct {
	writer  io.Writer
	failed  bool
	written int64
}

func (w *writeTracker) Write(data []byte) (int, error) {
	written, err := w.writer.Write(data)
	w.written += int64(written)
	if err != nil || written != len(data) {
		w.failed = true
	}
	return written, err
}

func (w *writeTracker) Failed() bool        { return w.failed }
func (w *writeTracker) BytesWritten() int64 { return w.written }

func (e outputError) Error() string {
	return "write output failed"
}

func (e outputError) Unwrap() error {
	return e.err
}

func writeAll(output io.Writer, data []byte) error {
	written, err := io.Copy(output, strings.NewReader(string(data)))
	if err != nil {
		return outputError{err: err}
	}
	if written != int64(len(data)) {
		return outputError{err: io.ErrShortWrite}
	}
	return nil
}
