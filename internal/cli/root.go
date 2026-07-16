package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ardasevinc/mattermost-cli/internal/buildinfo"
	"github.com/ardasevinc/mattermost-cli/internal/presentation"
	mmSchema "github.com/ardasevinc/mattermost-cli/internal/schema"
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
	cmd.SetArgs(args)
	if err := cmd.ExecuteContext(ctx); err != nil {
		message := presentation.SanitizeLabel(presentation.Preprocess(err.Error(), state.credentials).Text)
		if writeErr := writeAll(errOut, []byte(fmt.Sprintf("error: %s\n", message))); writeErr != nil {
			return 3
		}
		if trackedOut.Failed() {
			return 3
		}
		return exitCode(err)
	}
	if trackedOut.Failed() {
		return 3
	}
	return 0
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
	cmd.PersistentFlags().StringVar(&state.flags.token, "token", "", "Mattermost personal access token")
	cmd.PersistentFlags().BoolVar(&state.flags.redact, "redact", true, "redact detected secrets")
	cmd.PersistentFlags().BoolVar(&state.flags.noRedact, "no-redact", false, "disable heuristic secret redaction")
	cmd.AddCommand(newSchemaCommand(s))
	return cmd
}

func newSchemaCommand(s streams) *cobra.Command {
	command := &cobra.Command{
		Use:   "schema",
		Short: "Inspect and validate machine schemas",
		Args:  cobra.NoArgs,
	}
	command.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List embedded schema identifiers",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			registry, err := mmSchema.Load()
			if err != nil {
				return readFailure(err)
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
				return readFailure(err)
			}
			data, err := registry.Show(args[0])
			if err != nil {
				return err
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
				return readFailure(err)
			}
			if err := registry.Validate(args[0], s.in); err != nil {
				if mmSchema.IsInputReadError(err) {
					return readFailure(err)
				}
				return err
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
	writer io.Writer
	failed bool
}

func (w *writeTracker) Write(data []byte) (int, error) {
	written, err := w.writer.Write(data)
	if err != nil || written != len(data) {
		w.failed = true
	}
	return written, err
}

func (w *writeTracker) Failed() bool { return w.failed }

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
