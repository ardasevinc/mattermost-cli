package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ardasevinc/mattermost-cli/internal/buildinfo"
	mmSchema "github.com/ardasevinc/mattermost-cli/internal/schema"
)

type streams struct {
	in  io.Reader
	out io.Writer
	err io.Writer
}

func Execute(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer) int {
	cmd := newRoot(streams{in: in, out: out, err: errOut})
	cmd.SetArgs(args)
	if err := cmd.ExecuteContext(ctx); err != nil {
		message := err.Error()
		if token := os.Getenv("MM_TOKEN"); token != "" {
			message = strings.ReplaceAll(message, token, "[REDACTED:mattermost_credential]")
		}
		_, _ = fmt.Fprintf(errOut, "error: %s\n", message)
		var outputFailure outputError
		if errors.As(err, &outputFailure) {
			return 3
		}
		return 2
	}
	return 0
}

func newRoot(s streams) *cobra.Command {
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
				return err
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
				return err
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
				return err
			}
			if err := registry.Validate(args[0], s.in); err != nil {
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
