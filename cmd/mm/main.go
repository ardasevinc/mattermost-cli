package main

import (
	"context"
	"os"

	"github.com/ardasevinc/mattermost-cli/v2/internal/cli"
)

func main() {
	handleBrokenPipe()
	ctx, stop := commandContext(context.Background())
	defer stop()
	os.Exit(cli.Execute(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
