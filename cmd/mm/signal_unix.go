//go:build darwin || linux

package main

import (
	"os"
	"os/signal"
	"syscall"
)

var brokenPipeSignals = make(chan os.Signal, 1)

func handleBrokenPipe() {
	signal.Notify(brokenPipeSignals, syscall.SIGPIPE)
}
