package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ardasevinc/mattermost-cli/internal/api"
	"github.com/ardasevinc/mattermost-cli/internal/config"
)

type fakeCall struct {
	public bool
	path   string
	ctx    context.Context
}

type fakeResult struct {
	value any
	err   error
	run   func(context.Context, any) error
}

type fakeTransport struct {
	results []fakeResult
	calls   []fakeCall
}

type fakeFactory struct {
	transport *fakeTransport
	baseURL   string
	token     string
	closed    int
	err       error
}

func (f *fakeFactory) make(baseURL, token string) (Transport, func(), error) {
	f.baseURL, f.token = baseURL, token
	if f.err != nil {
		return nil, nil, f.err
	}
	return f.transport, func() { f.closed++ }, nil
}

func factoryFor(transport *fakeTransport) Factory {
	factory := &fakeFactory{transport: transport}
	return factory.make
}

func (f *fakeTransport) GetPublic(ctx context.Context, path string, out any) error {
	return f.call(ctx, path, out, true)
}

func (f *fakeTransport) Get(ctx context.Context, path string, out any) error {
	return f.call(ctx, path, out, false)
}

func (f *fakeTransport) call(ctx context.Context, path string, out any, public bool) error {
	f.calls = append(f.calls, fakeCall{public: public, path: path, ctx: ctx})
	result := f.results[len(f.calls)-1]
	if result.run != nil {
		return result.run(ctx, out)
	}
	if result.err != nil {
		return result.err
	}
	data, err := json.Marshal(result.value)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

func completeConfig() config.Resolved {
	return config.Resolved{
		URL: "https://mm.example", Token: "active-token", Redact: true,
		URLSource: config.SourceCLI, TokenSource: config.SourceEnv,
	}
}

func healthy() map[string]any {
	return map[string]any{"status": "OK", "database_status": "OK", "filestore_status": "OK"}
}

func TestRunHealthyUsesOnlyNarrowReadEndpoints(t *testing.T) {
	transport := &fakeTransport{results: []fakeResult{{value: healthy()}, {value: map[string]any{
		"id": "user-id", "username": "arda", "email": "private", "roles": "system_admin",
	}}}}
	factory := &fakeFactory{transport: transport}
	report := Run(context.Background(), config.Resolved{URL: " HTTPS://MM.Example:443/base/../ ", Token: "active-token", Redact: true}, factory.make)
	if !report.OK || len(report.Checks) != 3 {
		t.Fatalf("Run() = %+v", report)
	}
	if len(transport.calls) != 2 || !transport.calls[0].public || transport.calls[0].path != "/system/ping?get_server_status=true" ||
		transport.calls[1].public || transport.calls[1].path != "/users/me" {
		t.Fatalf("calls = %+v", transport.calls)
	}
	if got := report.Checks[2].Details; len(got) != 2 || got["id"] != "user-id" || got["username"] != "arda" {
		t.Fatalf("authentication details = %#v", got)
	}
	if factory.baseURL != "https://mm.example" || factory.token != "active-token" || factory.closed != 1 {
		t.Fatalf("factory binding/lifecycle = url %q token %q closes %d", factory.baseURL, factory.token, factory.closed)
	}
}

func TestRunContinuesAfterFailuresWithoutReflectingErrors(t *testing.T) {
	token := "never-print-this"
	resolved := completeConfig()
	resolved.Token = token
	transport := &fakeTransport{results: []fakeResult{
		{err: fmt.Errorf("remote body: %s: %w", token, &api.APIError{Status: 503})},
		{err: fmt.Errorf("hostile %s: %w", token, &api.APIError{Status: 401})},
	}}
	report := Run(context.Background(), resolved, factoryFor(transport))
	encoded, _ := json.Marshal(report)
	if len(report.Checks) != 3 || len(transport.calls) != 2 || report.OK {
		t.Fatalf("Run() = %+v, calls = %d", report, len(transport.calls))
	}
	if strings.Contains(string(encoded), token) || strings.Contains(string(encoded), "remote body") || strings.Contains(string(encoded), "hostile") {
		t.Fatalf("report reflected an error: %s", encoded)
	}
	if report.Checks[1].Details["httpStatus"] != 503 || report.Checks[2].Details["httpStatus"] != 401 {
		t.Fatalf("status details = %#v / %#v", report.Checks[1].Details, report.Checks[2].Details)
	}
}

func TestRunSanitizesAndRedactsEveryRemoteString(t *testing.T) {
	for _, redact := range []bool{true, false} {
		resolved := completeConfig()
		resolved.Redact = redact
		token := resolved.Token
		control := "\x1b]8;;https://evil.example\x07click\x1b]8;;\x07"
		probe := "ghp_abcdefghijklmnopqrstuvwxyz1234567890"
		transport := &fakeTransport{results: []fakeResult{
			{value: map[string]any{"status": "OK", "database_status": token + control, "filestore_status": "OK", "secret": token}},
			{value: map[string]any{"id": token + control, "username": "arda" + control + probe, "email": token}},
		}}
		report := Run(context.Background(), resolved, factoryFor(transport))
		encoded, _ := json.Marshal(report)
		output := string(encoded)
		if strings.Contains(output, token) || strings.Contains(output, "\x1b") || strings.Contains(output, "\x07") {
			t.Fatalf("redact=%v leaked unsafe value: %q", redact, output)
		}
		if !strings.Contains(output, "REDACTED") || !strings.Contains(output, `\\u001b`) {
			t.Fatalf("redact=%v did not visibly sanitize/redact: %q", redact, output)
		}
		if redact && strings.Contains(output, probe) {
			t.Fatalf("heuristic secret leaked: %q", output)
		}
	}
}

func TestRunHandlesMalformedAndIncompleteResponses(t *testing.T) {
	transport := &fakeTransport{results: []fakeResult{
		{value: map[string]any{"status": "OK", "database_status": 42, "filestore_status": "OK"}},
		{value: map[string]any{"id": " ", "username": []string{"arda"}}},
	}}
	report := Run(context.Background(), completeConfig(), factoryFor(transport))
	if report.Checks[1].Status != StatusWarn || report.Checks[1].Details["databaseStatus"] != "unknown" {
		t.Fatalf("server check = %+v", report.Checks[1])
	}
	if report.Checks[2].Status != StatusFail || report.Checks[2].Message != "authentication response was invalid" {
		t.Fatalf("authentication check = %+v", report.Checks[2])
	}
}

func TestConfigurationPermissionAndSourceSemantics(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*config.Resolved)
		status  Status
		message string
	}{
		{"stored token exposed", func(r *config.Resolved) { r.File.InsecurePermissions = true; r.File.Config.Token = "file-secret" }, StatusFail, "config file permissions expose a stored token; run chmod 600"},
		{"tokenless file warns", func(r *config.Resolved) { r.File.InsecurePermissions = true }, StatusWarn, "config file permissions are broader than recommended; run chmod 600"},
		{"unsafe file fails", func(r *config.Resolved) { r.File.Unsafe = config.UnsafeOwnership }, StatusFail, "config file could not be loaded"},
		{"malformed file fails", func(r *config.Resolved) { r.File.Error = config.FileErrorParse }, StatusFail, "config file could not be loaded"},
		{"missing token fails", func(r *config.Resolved) { r.Token = ""; r.TokenSource = config.SourceMissing }, StatusFail, "configuration is incomplete"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolved := completeConfig()
			test.mutate(&resolved)
			transport := &fakeTransport{results: []fakeResult{{value: healthy()}, {value: map[string]any{"id": "id", "username": "user"}}}}
			report := Run(context.Background(), resolved, factoryFor(transport))
			check := report.Checks[0]
			if check.Status != test.status || check.Message != test.message {
				t.Fatalf("configuration check = %+v", check)
			}
			if check.Details["urlSource"] != config.SourceCLI || check.Details["tokenSource"] != resolved.TokenSource {
				t.Fatalf("source details = %#v", check.Details)
			}
			encoded, _ := json.Marshal(report)
			if strings.Contains(string(encoded), "file-secret") || (resolved.Token != "" && strings.Contains(string(encoded), resolved.Token)) {
				t.Fatalf("report leaked credential: %s", encoded)
			}
		})
	}
}

func TestMissingAndUnsafeConfigurationStillProducesThreeChecks(t *testing.T) {
	missing := completeConfig()
	missing.Token = ""
	transport := &fakeTransport{results: []fakeResult{{value: healthy()}}}
	report := Run(context.Background(), missing, factoryFor(transport))
	if len(report.Checks) != 3 || report.Checks[1].Status != StatusPass || report.Checks[2].Status != StatusSkipped || len(transport.calls) != 1 {
		t.Fatalf("missing token report = %+v, calls = %+v", report, transport.calls)
	}

	unsafe := completeConfig()
	unsafe.URL = "http://mm.example"
	transport = &fakeTransport{}
	report = Run(context.Background(), unsafe, factoryFor(transport))
	if len(report.Checks) != 3 || report.Checks[1].Status != StatusFail || report.Checks[2].Status != StatusSkipped || len(transport.calls) != 0 {
		t.Fatalf("unsafe URL report = %+v, calls = %+v", report, transport.calls)
	}
}

func TestRequestsHaveIndependentChildContexts(t *testing.T) {
	type contextKey string
	parent := context.WithValue(context.Background(), contextKey("proof"), "inherited")
	transport := &fakeTransport{results: []fakeResult{{value: healthy()}, {value: map[string]any{"id": "id", "username": "user"}}}}
	Run(parent, completeConfig(), factoryFor(transport))
	if len(transport.calls) != 2 || transport.calls[0].ctx == parent || transport.calls[1].ctx == parent || transport.calls[0].ctx == transport.calls[1].ctx {
		t.Fatalf("request contexts are not independent children")
	}
	for _, call := range transport.calls {
		deadline, ok := call.ctx.Deadline()
		if call.ctx.Value(contextKey("proof")) != "inherited" || !errors.Is(call.ctx.Err(), context.Canceled) || !ok || time.Until(deadline) > checkTimeout {
			t.Fatalf("request context did not inherit parent or was not released")
		}
	}
}

func TestPingTimeoutDoesNotPreventAuthentication(t *testing.T) {
	transport := &fakeTransport{results: []fakeResult{
		{run: func(ctx context.Context, _ any) error {
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) < 9*time.Second || time.Until(deadline) > checkTimeout {
				return errors.New("missing doctor deadline")
			}
			return context.DeadlineExceeded
		}},
		{value: map[string]any{"id": "id", "username": "user"}},
	}}
	report := Run(context.Background(), completeConfig(), factoryFor(transport))
	if len(transport.calls) != 2 || report.Checks[1].Status != StatusFail || report.Checks[2].Status != StatusPass {
		t.Fatalf("timeout continuation = %+v, calls = %+v", report, transport.calls)
	}
}

func TestParentDeadlineWins(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	transport := &fakeTransport{results: []fakeResult{
		{run: func(ctx context.Context, out any) error {
			if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > time.Second {
				return errors.New("parent deadline did not win")
			}
			return json.Unmarshal([]byte(`{"status":"OK","database_status":"OK","filestore_status":"OK"}`), out)
		}},
		{value: map[string]any{"id": "id", "username": "user"}},
	}}
	report := Run(parent, completeConfig(), factoryFor(transport))
	if !report.OK {
		t.Fatalf("Run() = %+v", report)
	}
}

func TestFactoryFailureIsGenericAndCloseIsNotRequired(t *testing.T) {
	token := "factory-secret-token"
	resolved := completeConfig()
	resolved.Token = token
	factory := &fakeFactory{err: fmt.Errorf("could not build for %s", token)}
	report := Run(context.Background(), resolved, factory.make)
	encoded, _ := json.Marshal(report)
	if len(report.Checks) != 3 || report.Checks[1].Status != StatusFail || report.Checks[2].Status != StatusFail || factory.closed != 0 {
		t.Fatalf("factory failure report = %+v, closes = %d", report, factory.closed)
	}
	if strings.Contains(string(encoded), token) || strings.Contains(string(encoded), "could not build") {
		t.Fatalf("factory error reflected: %s", encoded)
	}
}
