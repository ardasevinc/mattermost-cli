package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	mmSchema "github.com/ardasevinc/mattermost-cli/v2/internal/schema"
)

func TestDoctorMachineReportUsesPublicPingAndAuthenticatedIdentity(t *testing.T) {
	const token = "doctor-active-token"
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		switch request.URL.Path {
		case "/api/v4/system/ping":
			if request.Header.Get("Authorization") != "" || request.URL.Query().Get("get_server_status") != "true" {
				t.Fatalf("public ping carried auth or omitted status query")
			}
			writeJSON(t, w, `{"status":"OK","database_status":"OK","filestore_status":"OK"}`)
		case "/api/v4/users/me":
			if request.Header.Get("Authorization") != "Bearer "+token {
				t.Fatalf("identity authorization = %q", request.Header.Get("Authorization"))
			}
			writeJSON(t, w, `{"id":"user-id","username":"arda","email":"`+token+`"}`)
		default:
			t.Fatalf("unexpected request %s", request.URL.String())
		}
	}))
	defer server.Close()

	setTestHome(t, t.TempDir())
	t.Setenv("MM_URL", "")
	t.Setenv("MM_TOKEN", "")
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"--json", "--url", server.URL, "--token", token, "doctor"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 || calls.Load() != 2 || strings.Contains(stdout.String(), token) {
		t.Fatalf("exit=%d calls=%d stdout=%q stderr=%q", code, calls.Load(), stdout.String(), stderr.String())
	}
	registry, err := mmSchema.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Validate("mm/v2/doctor", bytes.NewReader(stdout.Bytes())); err != nil {
		t.Fatalf("doctor report failed schema validation: %v\n%s", err, stdout.String())
	}
}

func TestDoctorIncompleteConfigurationEmitsReportThenHandledExit(t *testing.T) {
	setTestHome(t, t.TempDir())
	t.Setenv("MM_URL", "")
	t.Setenv("MM_TOKEN", "")
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"--json", "doctor"}, strings.NewReader(""), &stdout, &stderr)
	if code != 3 || stderr.Len() != 0 || strings.Contains(stderr.String(), "mm/v2/error") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	var report struct {
		Schema string                          `json:"schema"`
		OK     bool                            `json:"ok"`
		Checks []struct{ Name, Status string } `json:"checks"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Schema != "mm/v2/doctor" || report.OK || len(report.Checks) != 3 || report.Checks[0].Name != "configuration" || report.Checks[1].Name != "server" || report.Checks[2].Name != "authentication" {
		t.Fatalf("report = %+v", report)
	}
}

func TestDoctorMigrationWarningDoesNotCorruptMachineReport(t *testing.T) {
	home, xdg := t.TempDir(), filepath.Join(t.TempDir(), "xdg\x1b]8;;bad\x07")
	legacy := filepath.Join(home, ".config", "mattermost-cli", "config.toml")
	writeFile(t, legacy, "url = \"http://127.0.0.1:1\"\n", 0o600)
	setTestHome(t, home)
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("MM_URL", "")
	t.Setenv("MM_TOKEN", "")
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"--json", "doctor"}, strings.NewReader(""), &stdout, &stderr)
	if code != 3 || !strings.Contains(stderr.String(), "warning:") || strings.Contains(stderr.String(), "\x1b") || strings.Contains(stdout.String(), "warning:") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if !json.Valid(stdout.Bytes()) {
		t.Fatalf("invalid report: %q", stdout.String())
	}
}

func TestDoctorRejectsArgumentsAndReportsWriterFailure(t *testing.T) {
	setTestHome(t, t.TempDir())
	var stdout, stderr bytes.Buffer
	if code := Execute(context.Background(), []string{"doctor", "extra"}, strings.NewReader(""), &stdout, &stderr); code != 2 || stdout.Len() != 0 {
		t.Fatalf("argument exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	stderr.Reset()
	if code := Execute(context.Background(), []string{"--json", "doctor"}, strings.NewReader(""), shortWriter{}, &stderr); code != 3 || strings.Contains(stderr.String(), "mm/v2/error") {
		t.Fatalf("short writer exit=%d stderr=%q", code, stderr.String())
	}
}

func TestDoctorHonorsEnvAndFileRedactionFalseWithoutExposingActiveCredential(t *testing.T) {
	const token = "doctor-owned-active-credential"
	const probe = "ghp_abcdefghijklmnopqrstuvwxyz1234567890"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v4/system/ping":
			writeJSON(t, w, `{"status":"OK","database_status":"`+probe+`","filestore_status":"OK"}`)
		case "/api/v4/users/me":
			writeJSON(t, w, `{"id":"`+token+`","username":"`+token+probe+`"}`)
		default:
			t.Fatalf("unexpected request %s", request.URL.String())
		}
	}))
	defer server.Close()

	t.Run("environment", func(t *testing.T) {
		setTestHome(t, t.TempDir())
		t.Setenv("MM_REDACT", "false")
		var stdout, stderr bytes.Buffer
		code := Execute(context.Background(), []string{"--json", "--url", server.URL, "--token", token, "doctor"}, strings.NewReader(""), &stdout, &stderr)
		assertDoctorHeuristicsDisabled(t, code, stdout.String(), stderr.String(), probe, token)
	})

	t.Run("file", func(t *testing.T) {
		previous, present := os.LookupEnv("MM_REDACT")
		if err := os.Unsetenv("MM_REDACT"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if present {
				_ = os.Setenv("MM_REDACT", previous)
			} else {
				_ = os.Unsetenv("MM_REDACT")
			}
		})
		home := t.TempDir()
		writeFile(t, filepath.Join(home, ".config", "mattermost-cli", "config.toml"), "url = \""+server.URL+"\"\ntoken = \""+token+"\"\nredact = false\n", 0o600)
		setTestHome(t, home)
		t.Setenv("MM_URL", "")
		t.Setenv("MM_TOKEN", "")
		var stdout, stderr bytes.Buffer
		code := Execute(context.Background(), []string{"--json", "doctor"}, strings.NewReader(""), &stdout, &stderr)
		assertDoctorHeuristicsDisabled(t, code, stdout.String(), stderr.String(), probe, token)
	})
}

func assertDoctorHeuristicsDisabled(t *testing.T, code int, stdout, stderr, probe, token string) {
	t.Helper()
	if code != 3 || stderr != "" || !strings.Contains(stdout, probe) || strings.Contains(stdout, token) || !strings.Contains(stdout, "REDACTED") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}
