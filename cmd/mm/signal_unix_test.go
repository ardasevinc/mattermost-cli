//go:build darwin || linux

package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ardasevinc/mattermost-cli/internal/cli"
)

func TestClosedStdoutUsesStableExitClass(t *testing.T) {
	command := exec.Command(os.Args[0], "-test.run=TestMMBrokenPipeHelper")
	command.Env = append(os.Environ(), "MM_BROKEN_PIPE_HELPER=1")
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Close() }()
	command.Stdout = writer
	var stderr bytes.Buffer
	command.Stderr = &stderr

	err = command.Run()

	var exitError *exec.ExitError
	if !errors.As(err, &exitError) || exitError.ExitCode() != 3 {
		t.Fatalf("closed-stdout result = %v; stderr = %q, want exit 3", err, stderr.String())
	}
	if strings.Contains(stderr.String(), "broken pipe") {
		t.Fatalf("stderr reflected low-level output error: %q", stderr.String())
	}
}

func TestMMBrokenPipeHelper(t *testing.T) {
	if os.Getenv("MM_BROKEN_PIPE_HELPER") != "1" {
		return
	}
	handleBrokenPipe()
	os.Exit(cli.Execute(context.Background(), []string{"schema", "list"}, strings.NewReader(""), os.Stdout, os.Stderr))
}

func TestBrokenPipeHandlerIsNotInheritedByChild(t *testing.T) {
	handleBrokenPipe()
	defer signal.Stop(brokenPipeSignals)

	output, err := exec.Command("/bin/sh", "-c", `kill -PIPE $$; echo survived`).CombinedOutput()

	var exitError *exec.ExitError
	if !errors.As(err, &exitError) {
		t.Fatalf("child error = %v, output = %q; want SIGPIPE termination", err, output)
	}
	if strings.Contains(string(output), "survived") {
		t.Fatalf("child inherited handled SIGPIPE: %q", output)
	}
}

func TestCommandContextStopsWithParent(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	ctx, stop := commandContext(parent)
	cancel()
	defer stop()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("command context did not stop")
	}
}

func TestCommandContextRecordsSignalCause(t *testing.T) {
	ctx, stop := commandContext(context.Background())
	defer stop()
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
		if !errors.Is(context.Cause(ctx), cli.ErrSignalCancellation) {
			t.Fatalf("cause=%v", context.Cause(ctx))
		}
	case <-time.After(time.Second):
		t.Fatal("signal did not cancel command context")
	}
}
