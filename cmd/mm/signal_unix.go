//go:build darwin || linux

package main

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/ardasevinc/mattermost-cli/v2/internal/cli"
)

var brokenPipeSignals = make(chan os.Signal, 1)

func handleBrokenPipe() {
	signal.Notify(brokenPipeSignals, syscall.SIGPIPE)
}

func commandContext(parent context.Context) (context.Context, func()) {
	return causedSignalContext(parent, os.Interrupt, syscall.SIGTERM)
}
func causedSignalContext(parent context.Context, signals ...os.Signal) (context.Context, func()) {
	ctx, cancel := context.WithCancelCause(parent)
	notifications := make(chan os.Signal, 1)
	stopped := make(chan struct{})
	signal.Notify(notifications, signals...)
	var once sync.Once
	go func() {
		select {
		case <-parent.Done():
			cancel(context.Cause(parent))
		case <-notifications:
			cancel(cli.ErrSignalCancellation)
		case <-stopped:
			cancel(context.Canceled)
		}
	}()
	return ctx, func() { once.Do(func() { signal.Stop(notifications); close(stopped) }) }
}
