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

type stringList []string

func (values *stringList) String() string { return fmt.Sprint([]string(*values)) }
func (values *stringList) Set(value string) error {
	*values = append(*values, value)
	return nil
}

func main() {
	flags := flag.NewFlagSet("conformance", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	scenarioPath := flags.String("scenario", "", "path to a conformance scenario")
	pairPath := flags.String("pair", "", "path to a paired oracle/candidate scenario")
	cwd := flags.String("cwd", "", "working directory for the command under test")
	oraclePath := flags.String("oracle", "", "oracle executable for a paired scenario")
	candidatePath := flags.String("candidate", "", "candidate executable for a paired scenario")
	var oraclePrefix, candidatePrefix stringList
	flags.Var(&oraclePrefix, "oracle-prefix", "repeatable oracle prefix argument")
	flags.Var(&candidatePrefix, "candidate-prefix", "repeatable candidate prefix argument")
	if err := flags.Parse(os.Args[1:]); err != nil {
		os.Exit(2)
	}
	command := flags.Args()
	if (*scenarioPath == "") == (*pairPath == "") {
		_, _ = fmt.Fprintln(os.Stderr, "exactly one of --scenario or --pair is required")
		os.Exit(2)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if *scenarioPath != "" {
		if len(command) == 0 || *oraclePath != "" || *candidatePath != "" || len(oraclePrefix) != 0 || len(candidatePrefix) != 0 {
			_, _ = fmt.Fprintln(os.Stderr, "usage: conformance --scenario FILE [--cwd DIR] -- COMMAND [PREFIX_ARGS...]")
			os.Exit(2)
		}
		scenario, err := conformance.Load(*scenarioPath)
		if err == nil {
			err = conformance.Run(ctx, conformance.Command{
				Path:       command[0],
				PrefixArgs: command[1:],
				Dir:        *cwd,
			}, scenario)
		}
		finish(scenario.Name, err)
	}
	if len(command) != 0 || *oraclePath == "" || *candidatePath == "" {
		_, _ = fmt.Fprintln(os.Stderr, "usage: conformance --pair FILE [--cwd DIR] --oracle PATH [--oracle-prefix ARG...] --candidate PATH [--candidate-prefix ARG...]")
		os.Exit(2)
	}
	pair, err := conformance.LoadPair(*pairPath)
	if err == nil {
		err = conformance.RunPair(ctx,
			conformance.Command{Path: *oraclePath, PrefixArgs: oraclePrefix, Dir: *cwd},
			conformance.Command{Path: *candidatePath, PrefixArgs: candidatePrefix, Dir: *cwd},
			pair,
		)
	}
	finish(pair.Name, err)
}

func finish(name string, err error) {
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "conformance:", err)
		os.Exit(1)
	}
	_, _ = fmt.Fprintln(os.Stdout, "passed:", name)
}
