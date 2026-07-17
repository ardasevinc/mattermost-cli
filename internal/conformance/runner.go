package conformance

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	maxProcessOutput = 4 << 20
	processWaitDelay = 2 * time.Second
)

type Command struct {
	Path       string
	PrefixArgs []string
	Dir        string
}

func RunPair(ctx context.Context, oracle, candidate Command, pair PairScenario) error {
	if err := Run(ctx, oracle, pair.Oracle); err != nil {
		return fmt.Errorf("oracle: %w", err)
	}
	if err := Run(ctx, candidate, pair.Candidate); err != nil {
		return fmt.Errorf("candidate: %w", err)
	}
	return nil
}

func Run(ctx context.Context, command Command, scenario Scenario) error {
	if command.Path == "" {
		return fmt.Errorf("command path is required")
	}
	handler := newSequentialServer(scenario.HTTP, protectedCredentials(scenario)...)
	server := httptest.NewServer(handler)
	defer func() {
		server.CloseClientConnections()
		server.Close()
	}()

	tempHome, err := os.MkdirTemp("", "mm-conformance-home-")
	if err != nil {
		return fmt.Errorf("create isolated home: %w", err)
	}
	defer func() { _ = os.RemoveAll(tempHome) }()
	tempDir := tempHome + "/tmp"
	if err := os.MkdirAll(tempDir, 0o700); err != nil {
		return fmt.Errorf("create isolated temp: %w", err)
	}

	timeout := 15 * time.Second
	if scenario.Timeout != nil {
		timeout = time.Duration(*scenario.Timeout) * time.Millisecond
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := append(append([]string{}, command.PrefixArgs...), scenario.Args...)
	cmd := exec.CommandContext(runCtx, command.Path, args...) // #nosec G204 -- explicit operator-selected binary under test.
	configureProcessGroup(cmd)
	defer cleanupProcessGroup(cmd)
	cmd.WaitDelay = processWaitDelay
	cmd.Dir = command.Dir
	cmd.Stdin = strings.NewReader(scenario.Stdin)
	stdout := newLimitedBuffer(maxProcessOutput)
	stderr := newLimitedBuffer(maxProcessOutput)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Env, err = isolatedEnv(tempHome, tempDir, server.URL, scenario.Env)
	if err != nil {
		return err
	}

	runErr := cmd.Run()
	if ctx.Err() != nil {
		return fmt.Errorf("command interrupted: %w", ctx.Err())
	}
	if runCtx.Err() != nil {
		return fmt.Errorf("command exceeded scenario timeout: %w", runCtx.Err())
	}
	if stdout.Exceeded() || stderr.Exceeded() {
		return fmt.Errorf("command output exceeded %d bytes per stream", maxProcessOutput)
	}
	exitCode := 0
	if runErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(runErr, &exitErr) {
			return fmt.Errorf("run command: %w", runErr)
		}
		exitCode = exitErr.ExitCode()
	}

	if err := handler.verify(); err != nil {
		return err
	}
	want := scenario.Expected
	if exitCode != *want.ExitCode {
		return fmt.Errorf("exit code = %d, want %d; stderr %s", exitCode, *want.ExitCode, byteSummary(stderr.String()))
	}
	if got, expected := stdout.String(), expand(*want.Stdout, server.URL); got != expected {
		return fmt.Errorf("stdout mismatch: got %s, want %s", byteSummary(got), byteSummary(expected))
	}
	if got, expected := stderr.String(), expand(*want.Stderr, server.URL); got != expected {
		return fmt.Errorf("stderr mismatch: got %s, want %s", byteSummary(got), byteSummary(expected))
	}
	return nil
}

func protectedCredentials(scenario Scenario) []string {
	protected := []string{scenario.Env["MM_TOKEN"]}
	for _, exchange := range scenario.HTTP {
		for name, value := range exchange.Request.Headers {
			if !strings.EqualFold(name, "Authorization") {
				continue
			}
			protected = append(protected, value)
			if _, credential, found := strings.Cut(value, " "); found {
				protected = append(protected, credential)
			}
		}
	}
	return protected
}

func isolatedEnv(home, tempDir, serverURL string, scenarioEnv map[string]string) ([]string, error) {
	reserved := map[string]bool{
		"HOME": true, "XDG_CONFIG_HOME": true, "XDG_STATE_HOME": true, "MM_URL": true,
		"PATH": true, "TMPDIR": true, "TMP": true, "TEMP": true,
		"LANG": true, "LC_ALL": true, "TZ": true, "NO_COLOR": true, "TERM": true,
	}
	for name := range scenarioEnv {
		if reserved[name] {
			return nil, fmt.Errorf("scenario environment cannot override reserved variable %q", name)
		}
	}
	env := []string{
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + home + "/.config",
		"XDG_STATE_HOME=" + home + "/.local/state",
		"MM_URL=" + serverURL,
		"PATH=" + os.Getenv("PATH"),
		"TMPDIR=" + tempDir,
		"TMP=" + tempDir,
		"TEMP=" + tempDir,
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
		"TZ=UTC",
		"NO_COLOR=1",
		"TERM=dumb",
	}
	for name, value := range scenarioEnv {
		env = append(env, name+"="+expand(value, serverURL))
	}
	return env, nil
}

func expand(value, serverURL string) string {
	return strings.ReplaceAll(value, "${SERVER_URL}", serverURL)
}

func byteSummary(value string) string {
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%d bytes sha256:%x", len(value), sum)
}
