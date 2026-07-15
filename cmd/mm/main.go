package main

import (
	"context"
	"os"

	"github.com/ardasevinc/mattermost-cli/internal/cli"
)

func main() {
	os.Exit(cli.Execute(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
