package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/ardasevinc/mattermost-cli/internal/conformance"
)

func main() {
	flags := flag.NewFlagSet("conformance", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	scenarioPath := flags.String("scenario", "", "path to a conformance scenario")
	cwd := flags.String("cwd", "", "working directory for the command under test")
	if err := flags.Parse(os.Args[1:]); err != nil {
		os.Exit(2)
	}
	command := flags.Args()
	if *scenarioPath == "" || len(command) == 0 {
		_, _ = fmt.Fprintln(os.Stderr, "usage: conformance --scenario FILE [--cwd DIR] -- COMMAND [PREFIX_ARGS...]")
		os.Exit(2)
	}
	scenario, err := conformance.Load(*scenarioPath)
	if err == nil {
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		err = conformance.Run(ctx, conformance.Command{
			Path:       command[0],
			PrefixArgs: command[1:],
			Dir:        *cwd,
		}, scenario)
	}
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "conformance:", err)
		os.Exit(1)
	}
	_, _ = fmt.Fprintln(os.Stdout, "passed:", scenario.Name)
}
