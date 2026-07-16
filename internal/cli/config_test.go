package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	mmSchema "github.com/ardasevinc/mattermost-cli/internal/schema"
)

func TestConfigStatusIsOfflineAndNeverEmitsValues(t *testing.T) {
	home := t.TempDir()
	const token = "config-status-opaque-token"
	const server = "https://private-mm.example/team"
	writeFile(t, filepath.Join(home, ".config", "mattermost-cli", "config.toml"), "url = \""+server+"\"\ntoken = \""+token+"\"\n", 0o600)
	t.Setenv("HOME", home)
	t.Setenv("MM_URL", "")
	t.Setenv("MM_TOKEN", "")
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"--json", "config"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if strings.Contains(stdout.String(), token) || strings.Contains(stdout.String(), server) {
		t.Fatalf("config status leaked a configured value: %q", stdout.String())
	}
	registry, err := mmSchema.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Validate("mm/v2/config", bytes.NewReader(stdout.Bytes())); err != nil {
		t.Fatalf("machine config document invalid: %v; %s", err, stdout.String())
	}
	var document map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if document["urlConfigured"] != true || document["tokenConfigured"] != true || document["readStatus"] != "ok" {
		t.Fatalf("machine status = %#v", document)
	}
}

func TestConfigPathAndInitUseSelectedXDGPathWithoutCredentials(t *testing.T) {
	home, xdg := t.TempDir(), filepath.Join(t.TempDir(), "xdg")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("MM_URL", "")
	t.Setenv("MM_TOKEN", "")
	want := filepath.Join(xdg, "mattermost-cli", "config.toml")

	run := func(args ...string) (int, string, string) {
		var stdout, stderr bytes.Buffer
		code := Execute(context.Background(), args, strings.NewReader(""), &stdout, &stderr)
		return code, stdout.String(), stderr.String()
	}
	if code, stdout, stderr := run("config", "--path"); code != 0 || stdout != want+"\n" || stderr != "" {
		t.Fatalf("path exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if code, stdout, stderr := run("config", "--init"); code != 0 || !strings.Contains(stdout, "Created config file") || stderr != "" {
		t.Fatalf("init exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	info, err := os.Stat(want)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("initialized config info=%v err=%v", info, err)
	}
	if err := os.WriteFile(want, []byte("url = \"keep me\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, stdout, stderr := run("config", "--init"); code != 0 || !strings.Contains(stdout, "already exists") || stderr != "" {
		t.Fatalf("second init exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if data, err := os.ReadFile(want); err != nil || string(data) != "url = \"keep me\"\n" {
		t.Fatalf("second init changed file: %q %v", data, err)
	}
}

func TestConfigReportsLegacyFallbackAndUnsafeDiagnostics(t *testing.T) {
	home, xdg := t.TempDir(), filepath.Join(t.TempDir(), "xdg")
	legacy := filepath.Join(home, ".config", "mattermost-cli", "config.toml")
	writeFile(t, legacy, "broken = [", 0o644)
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("MM_TOKEN", "")
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"config"}, strings.NewReader(""), &stdout, &stderr)
	if code != 3 || !strings.Contains(stderr.String(), "reading legacy config") ||
		!strings.Contains(stdout.String(), "Migration: legacy_fallback") ||
		!strings.Contains(stdout.String(), "Permissions: insecure") ||
		!strings.Contains(stdout.String(), "Parse status: error") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestConfigFlagsAreMutuallyExclusiveAndMachineErrorsAreIsolated(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, args := range [][]string{{"config", "--path", "--init"}, {"config", "--path="}} {
		var stdout, stderr bytes.Buffer
		code := Execute(context.Background(), append([]string{"--json"}, args...), strings.NewReader(""), &stdout, &stderr)
		if code != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), `"schema":"mm/v2/error"`) {
			t.Fatalf("args=%q exit=%d stdout=%q stderr=%q", args, code, stdout.String(), stderr.String())
		}
	}
}

func TestConfigRedactionFlagsShareGlobalValidationAndSemantics(t *testing.T) {
	const token = "config-redaction-owned-token"
	const heuristic = "AKIAIOSFODNN7EXAMPLE"
	home := filepath.Join(t.TempDir(), token+"-"+heuristic)
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("MM_TOKEN", token)

	for _, flag := range []string{"--no-redact", "--redact=false"} {
		var stdout, stderr bytes.Buffer
		code := Execute(context.Background(), []string{"--json", flag, "config", "--path"}, strings.NewReader(""), &stdout, &stderr)
		if code != 0 || stderr.Len() != 0 || strings.Contains(stdout.String(), token) || !strings.Contains(stdout.String(), heuristic) || !strings.Contains(stdout.String(), "mattermost_credential") {
			t.Fatalf("flag=%s exit=%d stdout=%q stderr=%q", flag, code, stdout.String(), stderr.String())
		}
	}
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"--json", "config", "--path"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 || strings.Contains(stdout.String(), token) || strings.Contains(stdout.String(), heuristic) {
		t.Fatalf("default redaction exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}

	for _, machine := range []bool{false, true} {
		args := []string{"--redact", "--no-redact", "config"}
		if machine {
			args = append([]string{"--json"}, args...)
		}
		stdout.Reset()
		stderr.Reset()
		code = Execute(context.Background(), args, strings.NewReader(""), &stdout, &stderr)
		if code != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "cannot be used together") {
			t.Fatalf("machine=%v exit=%d stdout=%q stderr=%q", machine, code, stdout.String(), stderr.String())
		}
	}
}

func TestConfigInsecureStoredTokenMatchesRuntimeSeverity(t *testing.T) {
	for _, test := range []struct {
		name, body string
		wantExit   int
	}{
		{name: "stored token", body: `token = "stored-private-token"`, wantExit: 3},
		{name: "tokenless", body: `url = "https://example.com"`, wantExit: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			writeFile(t, filepath.Join(home, ".config", "mattermost-cli", "config.toml"), test.body, 0o644)
			t.Setenv("HOME", home)
			for _, machine := range []bool{false, true} {
				args := []string{"config"}
				if machine {
					args = append([]string{"--json"}, args...)
				}
				var stdout, stderr bytes.Buffer
				code := Execute(context.Background(), args, strings.NewReader(""), &stdout, &stderr)
				if code != test.wantExit || stdout.Len() == 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), "insecure") || strings.Contains(stdout.String(), "stored-private-token") {
					t.Fatalf("machine=%v exit=%d stdout=%q stderr=%q", machine, code, stdout.String(), stderr.String())
				}
			}
		})
	}
}

func TestShortTokenFormsHaveParityAndNeverLeak(t *testing.T) {
	for _, args := range [][]string{{"-t", "short-separated-secret"}, {"-tshort-attached-secret"}, {"-t=short-equals-secret"}, {"-rtgrouped-secret"}, {"-rt=grouped-equals-secret"}} {
		secret := earlyTokens(args)[0]
		args = append(args, secret)
		var stdout, stderr bytes.Buffer
		code := Execute(context.Background(), args, strings.NewReader(""), &stdout, &stderr)
		if code != 2 || strings.Contains(stderr.String(), secret) || !strings.Contains(stderr.String(), "mattermost_credential") {
			t.Fatalf("args=%q exit=%d stderr=%q", args, code, stderr.String())
		}
	}
}

func TestConfigPresentationSanitizesPathsAndWarningsEvenWithoutRedaction(t *testing.T) {
	const token = "path-owned-opaque-token"
	hostile := token + "\x1b]0;evil\a" + "\u202e"
	home := filepath.Join(t.TempDir(), hostile)
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(home, ".config", "mattermost-cli", "config.toml")
	writeFile(t, legacy, "url = \"https://example.com\"\n", 0o600)
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "selected"))
	t.Setenv("MM_TOKEN", token)

	for _, args := range [][]string{{"--no-redact", "config"}, {"--json", "--no-redact", "config"}, {"--json", "--no-redact", "config", "--path"}} {
		var stdout, stderr bytes.Buffer
		code := Execute(context.Background(), args, strings.NewReader(""), &stdout, &stderr)
		combined := stdout.String() + stderr.String()
		if code != 0 || strings.Contains(combined, token) || strings.ContainsRune(combined, '\x1b') || strings.ContainsRune(combined, '\a') || strings.ContainsRune(combined, '\u202e') {
			t.Fatalf("args=%q exit=%d stdout=%q stderr=%q", args, code, stdout.String(), stderr.String())
		}
		if !strings.Contains(combined, "mattermost_credential") || !strings.Contains(combined, `\u001b`) {
			t.Fatalf("args=%q lacked safe provenance: %q", args, combined)
		}
	}
}

func TestConfigDiagnosticDocumentsUseHandledExitThree(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, path string)
	}{
		{name: "parse", setup: func(t *testing.T, path string) { writeFile(t, path, "broken = [", 0o600) }},
		{name: "read", setup: func(t *testing.T, path string) {
			if err := os.MkdirAll(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "unsafe", setup: func(t *testing.T, path string) {
			target := filepath.Join(filepath.Dir(path), "target.toml")
			writeFile(t, target, "url = \"https://example.com\"", 0o600)
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			path := filepath.Join(home, ".config", "mattermost-cli", "config.toml")
			test.setup(t, path)
			t.Setenv("HOME", home)
			for _, machine := range []bool{false, true} {
				args := []string{"config"}
				if machine {
					args = append([]string{"--json"}, args...)
				}
				var stdout, stderr bytes.Buffer
				code := Execute(context.Background(), args, strings.NewReader(""), &stdout, &stderr)
				if code != 3 || stdout.Len() == 0 || stderr.Len() != 0 || strings.Contains(stderr.String(), "error:") {
					t.Fatalf("machine=%v exit=%d stdout=%q stderr=%q", machine, code, stdout.String(), stderr.String())
				}
				if machine {
					registry, err := mmSchema.Load()
					if err != nil {
						t.Fatal(err)
					}
					if err := registry.Validate("mm/v2/config", bytes.NewReader(stdout.Bytes())); err != nil {
						t.Fatalf("invalid document: %v: %s", err, stdout.String())
					}
					if strings.Contains(stdout.String(), "mm/v2/error") {
						t.Fatalf("appended error document: %q", stdout.String())
					}
				}
			}
		})
	}
}

func TestConfigPathDoesNotInspectSelectedFile(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".config", "mattermost-cli", "config.toml")
	target := filepath.Join(filepath.Dir(path), "target.toml")
	writeFile(t, target, "broken = [", 0o600)
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"--json", "config", "--path"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	var document map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"readPath", "migration", "exists", "readStatus", "parseStatus", "unsafeReason"} {
		if document[field] != nil {
			t.Fatalf("path document %s=%v, want null: %#v", field, document[field], document)
		}
	}
}

func TestConfigSchemaRejectsContradictoryActionStates(t *testing.T) {
	registry, err := mmSchema.Load()
	if err != nil {
		t.Fatal(err)
	}
	contradictions := []string{
		`{"schema":"mm/v2/config","action":"path","selectedPath":"/tmp/config","readPath":"/tmp/config","migration":null,"exists":null,"urlConfigured":null,"tokenConfigured":null,"permissions":null,"readStatus":null,"parseStatus":null,"unsafeReason":null,"created":null,"warning":null}`,
		`{"schema":"mm/v2/config","action":"status","selectedPath":"/tmp/config","readPath":null,"migration":"none","exists":false,"urlConfigured":true,"tokenConfigured":false,"permissions":"not_applicable","readStatus":"missing","parseStatus":"not_attempted","unsafeReason":null,"created":null,"warning":null}`,
		`{"schema":"mm/v2/config","action":"init","selectedPath":"/tmp/config","readPath":null,"migration":"none","exists":false,"urlConfigured":false,"tokenConfigured":false,"permissions":"not_applicable","readStatus":"missing","parseStatus":"not_attempted","unsafeReason":null,"created":null,"warning":null}`,
		`{"schema":"mm/v2/config","action":"status","selectedPath":"/tmp/config","readPath":"/tmp/config","migration":"none","exists":true,"urlConfigured":false,"tokenConfigured":false,"permissions":"secure","readStatus":"ok","parseStatus":"not_attempted","unsafeReason":"type","created":null,"warning":null}`,
	}
	for _, document := range contradictions {
		if err := registry.Validate("mm/v2/config", strings.NewReader(document)); err == nil {
			t.Fatalf("contradictory document accepted: %s", document)
		}
	}
}
