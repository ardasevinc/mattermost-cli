//go:build !darwin && !linux

package main

import (
	"context"
	"os"
	"os/signal"
	"sync"

	"github.com/ardasevinc/mattermost-cli/v2/internal/cli"
)

func handleBrokenPipe() {}
func commandContext(parent context.Context) (context.Context, func()) {
	ctx, cancel := context.WithCancelCause(parent)
	notifications := make(chan os.Signal, 1)
	stopped := make(chan struct{})
	signal.Notify(notifications, os.Interrupt)
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
