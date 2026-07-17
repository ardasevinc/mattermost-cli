package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/ardasevinc/mattermost-cli/v2/internal/api"
	"github.com/ardasevinc/mattermost-cli/v2/internal/config"
	"github.com/ardasevinc/mattermost-cli/v2/internal/mattermost"
	"github.com/ardasevinc/mattermost-cli/v2/internal/presentation"
)

func TestRuntimeResolvesCLIEnvFilePrecedenceAndNormalizesURL(t *testing.T) {
	home := t.TempDir()
	writeRuntimeConfig(t, home, `url = "https://file.example/base/"
token = "file-token"
redact = false
`)
	env := map[string]string{"MM_URL": "https://env.example/path/", "MM_TOKEN": "env-token"}
	state, command, captured := runtimeProbe(t, home, env, false)
	command.SetArgs([]string{"--url", "https://CLI.example:443/chat/", "--token", "cli-token", "--redact", "probe"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	defer state.close()
	defer state.releaseCredentials()
	if captured.runtime.Config.URL != "https://cli.example/chat" || captured.runtime.Config.Token != "cli-token" {
		t.Fatalf("resolved config = %+v", captured.runtime.Config)
	}
	if !captured.runtime.Config.Redact || captured.runtime.Config.RedactSource != config.SourceCLI {
		t.Fatalf("redact = %v/%s, want true/cli", captured.runtime.Config.Redact, captured.runtime.Config.RedactSource)
	}
	if captured.runtime.Users == nil || captured.runtime.Teams == nil || captured.runtime.Channels == nil || captured.runtime.Posts == nil {
		t.Fatal("runtime did not construct all Mattermost services")
	}
}

func TestRuntimeUsesMacIndependentXDGPath(t *testing.T) {
	home := t.TempDir()
	xdg := filepath.Join(t.TempDir(), "config")
	path := filepath.Join(xdg, "mattermost-cli", "config.toml")
	writeFile(t, path, "url = \"https://xdg.example\"\ntoken = \"xdg-token\"\n", 0o600)
	state, command, captured := runtimeProbe(t, home, map[string]string{"XDG_CONFIG_HOME": xdg}, false)
	defer state.close()
	defer state.releaseCredentials()
	command.SetArgs([]string{"probe"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if captured.runtime.Config.File.ReadPath != path {
		t.Fatalf("ReadPath = %q, want %q", captured.runtime.Config.File.ReadPath, path)
	}
}

func TestRuntimeRejectsWritableTokenlessConfigBeforeUsingEnvironmentCredential(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".config", "mattermost-cli", "config.toml")
	writeFile(t, path, `url = "https://attacker-controlled.example"`, 0o620)
	if err := os.Chmod(path, 0o620); err != nil {
		t.Fatal(err)
	}
	state, command, captured := runtimeProbe(t, home, map[string]string{"MM_TOKEN": "environment-token"}, false)
	defer state.close()
	defer state.releaseCredentials()
	command.SetArgs([]string{"probe"})
	err := command.Execute()
	if err == nil || exitCode(err) != 3 || !strings.Contains(err.Error(), "must not be writable by other users") || captured.runtime != nil {
		t.Fatalf("runtime=%v err=%v", captured.runtime, err)
	}
}

func TestReadDisplayAcceptsNegativeBooleanFlags(t *testing.T) {
	state, command, _ := runtimeProbe(t, t.TempDir(), map[string]string{"MM_URL": "https://example.com", "MM_TOKEN": "token"}, false)
	defer state.close()
	defer state.releaseCredentials()

	command.SetArgs([]string{"--no-threads", "probe"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	display, err := state.readDisplay(command)
	if err != nil || display.threads {
		t.Fatalf("readDisplay = %+v, %v", display, err)
	}
}

func TestReadDisplayPreservesAgentRelativeDefault(t *testing.T) {
	env := map[string]string{"MM_URL": "https://example.com", "MM_TOKEN": "token", "CODEX_CI": "1"}
	state, command, _ := runtimeProbe(t, t.TempDir(), env, false)
	defer state.close()
	defer state.releaseCredentials()
	command.SetArgs([]string{"probe"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	display, err := state.readDisplay(command)
	if err != nil || !display.relative {
		t.Fatalf("readDisplay = %+v, %v", display, err)
	}
}

func TestRuntimeRejectsUnsafeParseAndInsecureTokenConfig(t *testing.T) {
	tests := []struct {
		name, body, want string
		mode             os.FileMode
	}{
		{name: "parse", body: "broken = [", mode: 0o600, want: "could not parse"},
		{name: "insecure token", body: "token = \"private-token\"", mode: 0o644, want: "must not be accessible"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			writeFile(t, filepath.Join(home, ".config", "mattermost-cli", "config.toml"), test.body, test.mode)
			state, command, _ := runtimeProbe(t, home, nil, false)
			defer state.close()
			defer state.releaseCredentials()
			command.SetArgs([]string{"probe"})
			err := command.Execute()
			if err == nil || !strings.Contains(err.Error(), test.want) || exitCode(err) != 3 {
				t.Fatalf("Execute() error = %v, exit=%d", err, exitCode(err))
			}
			if strings.Contains(err.Error(), "private-token") {
				t.Fatalf("error reflected token: %q", err)
			}
		})
	}
}

func TestRuntimeRejectsUnsafeConfigPathBeforeClientCreation(t *testing.T) {
	home := t.TempDir()
	directory := filepath.Join(home, ".config", "mattermost-cli")
	writeFile(t, filepath.Join(directory, "target.toml"), "url = \"https://example.com\"\ntoken = \"unsafe-token\"\n", 0o600)
	if err := os.Symlink(filepath.Join(directory, "target.toml"), filepath.Join(directory, "config.toml")); err != nil {
		t.Fatal(err)
	}
	state, command, _ := runtimeProbe(t, home, nil, false)
	created := false
	state.deps.newClient = func(string, string) (*api.Client, error) {
		created = true
		return nil, errors.New("must not run")
	}
	defer state.close()
	defer state.releaseCredentials()
	command.SetArgs([]string{"probe"})
	err := command.Execute()
	if err == nil || exitCode(err) != 3 || created {
		t.Fatalf("Execute() error=%v exit=%d clientCreated=%v", err, exitCode(err), created)
	}
}

func TestRuntimeMissingConfigIsNormalWhenCLIHasCredentials(t *testing.T) {
	state, command, captured := runtimeProbe(t, t.TempDir(), nil, false)
	defer state.close()
	defer state.releaseCredentials()
	command.SetArgs([]string{"--url", "https://example.com", "--token", "token", "probe"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if captured.runtime.Config.File.Exists {
		t.Fatal("missing config reported as existing")
	}
}

func TestRuntimeMigrationWarningOnceAndTTYInjection(t *testing.T) {
	home := t.TempDir()
	xdg := filepath.Join(t.TempDir(), "xdg")
	writeRuntimeConfig(t, home, "url = \"https://legacy.example\"\ntoken = \"legacy-token\"\n")
	state, command, captured := runtimeProbe(t, home, map[string]string{"XDG_CONFIG_HOME": xdg}, true)
	defer state.close()
	defer state.releaseCredentials()
	command.SetArgs([]string{"probe"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if _, err := state.runtimeFor(command); err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(state.streams.err.(*bytes.Buffer).String(), "warning:"); got != 1 {
		t.Fatalf("warning count = %d, want 1", got)
	}
	if !captured.runtime.StdoutTTY {
		t.Fatal("injected stdout TTY capability was not retained")
	}
}

func TestMachineRuntimeDefersMigrationWarningUntilSuccessfulCompletion(t *testing.T) {
	home := t.TempDir()
	xdg := filepath.Join(t.TempDir(), "xdg")
	writeRuntimeConfig(t, home, "url = \"https://legacy.example\"\ntoken = \"legacy-token\"\n")
	state, command, _ := runtimeProbe(t, home, map[string]string{"XDG_CONFIG_HOME": xdg}, false)
	defer state.close()
	defer state.releaseCredentials()
	command.SetArgs([]string{"--json", "probe"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if got := state.streams.err.(*bytes.Buffer).String(); got != "" {
		t.Fatalf("machine stderr before completion = %q", got)
	}
	if err := state.flushMachineWarnings(); err != nil {
		t.Fatal(err)
	}
	if got := state.streams.err.(*bytes.Buffer).String(); !strings.Contains(got, "warning:") || !strings.Contains(got, ".config/mattermost-cli") {
		t.Fatalf("machine completion warning = %q", got)
	}
}

func TestRuntimeCloseReleasesClientAndFileCredential(t *testing.T) {
	home := t.TempDir()
	const token = "file-owned-token"
	writeRuntimeConfig(t, home, "url = \"https://example.com\"\ntoken = \""+token+"\"\n")
	state, command, captured := runtimeProbe(t, home, nil, false)
	command.SetArgs([]string{"probe"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if !contains(presentation.ActiveCredentials.Values(), token) {
		t.Fatal("resolved file credential was not active during command lifetime")
	}
	client := captured.runtime.Client
	state.close()
	state.releaseCredentials()
	if contains(presentation.ActiveCredentials.Values(), token) {
		t.Fatal("resolved file credential remained active after execution cleanup")
	}
	if err := client.Get(context.Background(), "/api/v4/users/me", new(any)); !errors.Is(err, api.ErrClientClosed) {
		t.Fatalf("closed client Get() error = %v", err)
	}
}

func TestExitClassesAndSchemaIsolation(t *testing.T) {
	if exitCode(invalidFailure("bad input")) != 2 || exitCode(configFailure("bad config")) != 3 || exitCode(outputError{err: errors.New("write")}) != 3 {
		t.Fatal("stable exit class mapping changed")
	}
	setTestHome(t, "relative-home-must-not-be-read")
	var stdout, stderr bytes.Buffer
	if code := Execute(context.Background(), []string{"schema", "list"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("schema required runtime config: exit=%d stderr=%q", code, stderr.String())
	}
}

func TestCLIFlagTokenIsNeverReflected(t *testing.T) {
	const token = "cli-only-super-secret"
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"--token", token, "not-a-command"}, strings.NewReader(""), &stdout, &stderr)
	if code != 2 || strings.Contains(stderr.String(), token) {
		t.Fatalf("exit=%d stderr reflected CLI token: %q", code, stderr.String())
	}
}

func TestFileOnlyTokenMasksPreParseErrorsWithoutMakingSchemaDependOnConfig(t *testing.T) {
	home := t.TempDir()
	const token = "file-only-opaque-token"
	writeRuntimeConfig(t, home, "token = \""+token+"\"\n")
	setTestHome(t, home)
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{token}, strings.NewReader(""), &stdout, &stderr)
	if code != 2 || strings.Contains(stderr.String(), token) || !strings.Contains(stderr.String(), "mattermost_credential") {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	writeFile(t, filepath.Join(home, ".config", "mattermost-cli", "config.toml"), "broken = [", 0o600)
	stdout.Reset()
	stderr.Reset()
	if code := Execute(context.Background(), []string{"schema", "list"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("schema exit=%d stderr=%q", code, stderr.String())
	}
}

func TestAllRepeatedCLITokenFormsAreDiscoveredAndMasked(t *testing.T) {
	const first = "first-opaque-decoy"
	const second = "second-opaque-final"
	if got := earlyTokens([]string{"--token", first, "--token=" + second}); len(got) != 2 || got[0] != first || got[1] != second {
		t.Fatalf("earlyTokens() = %q", got)
	}
	for _, surface := range []string{first, second} {
		var stdout, stderr bytes.Buffer
		code := Execute(context.Background(), []string{"--token", first, "--token=" + second, "schema", "show", surface}, strings.NewReader(""), &stdout, &stderr)
		if code != 2 || strings.Contains(stderr.String(), surface) {
			t.Fatalf("surface %q: exit=%d stderr=%q", surface, code, stderr.String())
		}
	}
}

func TestReadAndAuthFailuresMapToThreeWhileRawErrorsRemainInvocationFailures(t *testing.T) {
	apiErr := &api.APIError{Status: 503}
	if code := exitCode(readFailure(apiErr)); code != 3 || !errors.Is(readFailure(apiErr), apiErr) {
		t.Fatalf("read failure exit=%d", code)
	}
	authErr := &api.APIError{Status: 401}
	if code := exitCode(authFailure(authErr)); code != 3 || !errors.Is(authFailure(authErr), authErr) {
		t.Fatalf("auth failure exit=%d", code)
	}
	if code := exitCode(apiErr); code != 2 {
		t.Fatalf("unwrapped operation error exit=%d, want 2", code)
	}
	if code := exitCode(readFailure(mattermost.ErrInvalidUsersResponse)); code != 3 {
		t.Fatalf("wrapped Mattermost read error exit=%d, want 3", code)
	}
}

func TestConcurrentExecuteUsesOnlyInvocationLocalCredentialsForErrors(t *testing.T) {
	const tokenA = "invocation-a-opaque"
	const tokenB = "invocation-b-opaque"
	for iteration := 0; iteration < 50; iteration++ {
		start := make(chan struct{})
		results := make(chan string, 2)
		run := func(own, other string) {
			<-start
			var stdout, stderr bytes.Buffer
			_ = Execute(context.Background(), []string{"--token", own, other}, strings.NewReader(""), &stdout, &stderr)
			results <- stderr.String()
		}
		go run(tokenA, tokenB)
		go run(tokenB, tokenA)
		close(start)
		first, second := <-results, <-results
		if !(strings.Contains(first, tokenA) || strings.Contains(first, tokenB)) || !(strings.Contains(second, tokenA) || strings.Contains(second, tokenB)) {
			t.Fatalf("cross-invocation token was over-redacted: %q / %q", first, second)
		}
	}
}

type capturedRuntime struct{ runtime *Runtime }

func runtimeProbe(t *testing.T, home string, env map[string]string, tty bool) (*rootState, *cobra.Command, *capturedRuntime) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	lookup := func(key string) (string, bool) { value, ok := env[key]; return value, ok }
	state := &rootState{streams: streams{in: strings.NewReader(""), out: &stdout, err: &stderr}, deps: dependencies{
		lookupEnv: lookup, homeDir: func() (string, error) { return home, nil },
		newClient: func(url, token string) (*api.Client, error) { return api.New(url, token) },
		stdoutTTY: func() bool { return tty },
	}}
	command := newRootWithState(state)
	captured := new(capturedRuntime)
	command.AddCommand(&cobra.Command{Use: "probe", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		runtime, err := state.runtimeFor(cmd)
		captured.runtime = runtime
		return err
	}})
	return state, command, captured
}

func writeRuntimeConfig(t *testing.T, home, body string) {
	t.Helper()
	writeFile(t, filepath.Join(home, ".config", "mattermost-cli", "config.toml"), body, 0o600)
}

func setTestHome(t *testing.T, home string) {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
}

func writeFile(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
