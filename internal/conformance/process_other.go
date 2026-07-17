//go:build !darwin && !linux

package conformance

import "os/exec"

func configureProcessGroup(_ *exec.Cmd) {}

func cleanupProcessGroup(_ *exec.Cmd) {}
