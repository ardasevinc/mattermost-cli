package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/ardasevinc/mattermost-cli/internal/buildinfo"
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
		_, _ = fmt.Fprintf(errOut, "error: %s\n", err)
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
	return cmd
}
