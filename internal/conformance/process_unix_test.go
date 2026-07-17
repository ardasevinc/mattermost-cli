//go:build darwin || linux

package conformance

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRunTimeoutKillsDescendantProcess(t *testing.T) {
	pidFile := t.TempDir() + "/child.pid"
	timeout := 250
	scenario := processScenario(timeout, pidFile)

	err := Run(context.Background(), Command{
		Path: "/bin/sh",
	}, scenario)

	if err == nil || !strings.Contains(err.Error(), "scenario timeout") {
		t.Fatalf("Run() error = %v, want scenario timeout", err)
	}
	assertRecordedProcessGone(t, pidFile)
}

func TestRunCleansDescendantAfterLeaderExits(t *testing.T) {
	pidFile := t.TempDir() + "/child.pid"
	exitCode, stdout, stderr := 0, "", ""
	scenario := Scenario{
		Args: []string{"-c", `sleep 30 & child=$!; echo "$child" > "$PIDFILE"; exit 0`},
		Env:  map[string]string{"PIDFILE": pidFile},
		Expected: &ProcessExpected{
			ExitCode: &exitCode,
			Stdout:   &stdout,
			Stderr:   &stderr,
		},
	}

	_ = Run(context.Background(), Command{Path: "/bin/sh"}, scenario)

	assertRecordedProcessGone(t, pidFile)
}

func TestRunCancellationKillsDescendantProcess(t *testing.T) {
	pidFile := t.TempDir() + "/child.pid"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(pidFile); err == nil {
				cancel()
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	err := Run(ctx, Command{Path: "/bin/sh"}, processScenario(5_000, pidFile))

	if err == nil || !strings.Contains(err.Error(), "command interrupted") {
		t.Fatalf("Run() error = %v, want interruption", err)
	}
	assertRecordedProcessGone(t, pidFile)
}

func processScenario(timeout int, pidFile string) Scenario {
	exitCode, stdout, stderr := 0, "", ""
	return Scenario{
		Args:    []string{"-c", `sleep 30 & child=$!; echo "$child" > "$PIDFILE"; wait`},
		Timeout: &timeout,
		Env:     map[string]string{"PIDFILE": pidFile},
		Expected: &ProcessExpected{
			ExitCode: &exitCode,
			Stdout:   &stdout,
			Stderr:   &stderr,
		},
	}
}

func assertRecordedProcessGone(t *testing.T, pidFile string) {
	t.Helper()
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("read child pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("parse child pid: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		err = syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("descendant process %d remained alive after Run returned", pid)
}
