package cli

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ardasevinc/mattermost-cli/internal/mattermost"
	"github.com/ardasevinc/mattermost-cli/internal/presentation"
	mmSchema "github.com/ardasevinc/mattermost-cli/internal/schema"
)

func TestWatchSelectorsFailBeforeNetwork(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	for _, args := range [][]string{{"watch"}, {"watch", "town", "--dm", "bob"}, {"watch", "--dm", "bob", "--team", "main"}, {"watch", "--dm="}, {"watch", "town", "--team="}} {
		_, _, code := executeChannel(t, server.URL, args...)
		if code != 2 || requests.Load() != 0 {
			t.Fatalf("args=%v code=%d requests=%d", args, code, requests.Load())
		}
	}
}

func TestWatchMachineChannelResolutionAndSplit(t *testing.T) {
	server := watchChannelServer(t)
	defer server.Close()
	watch := func(_ context.Context, options mattermost.WatchOptions) error {
		if options.ChannelID != "channel1" {
			t.Fatalf("channel=%s", options.ChannelID)
		}
		release := presentation.ActiveCredentials.Register(options.Token)
		defer release()
		if err := options.Sink.Post(mattermost.WatchPost{ID: "post", ChannelID: "channel1", UserID: "user1", SenderName: "spoofed", Message: "line one\nline two test-token", CreateAt: 1, FileIDs: []string{}}, mattermost.Sequence{ConnectionID: "connection", Number: 1}); err != nil {
			return mattermost.ErrWatchSink
		}
		attempt := 1
		delay := time.Second
		return options.Sink.Diagnostic(mattermost.WatchDiagnostic{Type: "reconnect", Timestamp: time.UnixMilli(1), Message: "retry", Attempt: &attempt, Delay: &delay})
	}
	stdout, stderr, err := runWatchCommand(t, server.URL, watch, context.Background(), "--json", "watch", "town", "--team", "main")
	if err != nil {
		t.Fatal(err)
	}
	registry, _ := mmSchema.Load()
	if err := registry.Validate("mm/v2/watch-event", strings.NewReader(stdout)); err != nil {
		t.Fatalf("event schema: %v\n%s", err, stdout)
	}
	if err := registry.Validate("mm/v2/watch-diagnostic", strings.NewReader(stderr)); err != nil {
		t.Fatalf("diagnostic schema: %v\n%s", err, stderr)
	}
	if !strings.Contains(stdout, `line one\nline two`) || !strings.Contains(stdout, `"sender":"arda"`) || strings.Contains(stdout, "spoofed") || strings.Contains(stdout, "test-token") || strings.Contains(stderr, "Watching") {
		t.Fatalf("stdout=%q stderr=%q", stdout, stderr)
	}
}

func TestWatchDMResolutionUsesReadOnlyExactDirectListAndRejectsSelf(t *testing.T) {
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/api/v4/users/me":
			writeJSON(t, w, `{"id":"user1","username":"arda"}`)
		case "/api/v4/users/username/bob":
			writeJSON(t, w, `{"id":"bob","username":"bob"}`)
		case "/api/v4/users/user1/channels":
			writeJSON(t, w, `[{"id":"dm","team_id":"","type":"D","name":"bob__user1","display_name":""}]`)
		default:
			t.Fatalf("unexpected %s", r.URL.Path)
		}
	}))
	defer server.Close()
	called := false
	_, _, err := runWatchCommand(t, server.URL, func(_ context.Context, options mattermost.WatchOptions) error {
		called = true
		if options.ChannelID != "dm" {
			t.Fatal(options.ChannelID)
		}
		return nil
	}, context.Background(), "watch", "--dm", "bob")
	if err != nil || !called {
		t.Fatalf("err=%v called=%v", err, called)
	}
	for _, method := range methods {
		if strings.HasPrefix(method, "POST ") {
			t.Fatalf("mutation: %v", methods)
		}
	}
}

func TestWatchHumanPresentationCancellationAndTerminalOwnership(t *testing.T) {
	server := watchChannelServer(t)
	defer server.Close()
	ctx, cancel := context.WithCancelCause(context.Background())
	watch := func(ctx context.Context, options mattermost.WatchOptions) error {
		_ = options.Sink.Post(mattermost.WatchPost{ID: "p", ChannelID: "channel1", UserID: "u", SenderName: "", Message: "hello\n world", CreateAt: 0, FileIDs: []string{}}, mattermost.Sequence{ConnectionID: "c", Number: 1})
		cancel(ErrSignalCancellation)
		<-ctx.Done()
		return ctx.Err()
	}
	stdout, stderr, err := runWatchCommand(t, server.URL, watch, ctx, "--no-color", "watch", "town", "--team", "main")
	if err != nil || !strings.Contains(stdout, "unknown: hello world") || !strings.Contains(stderr, "Watching #town") {
		t.Fatalf("err=%v stdout=%q stderr=%q", err, stdout, stderr)
	}
	_, machineErr, err := runWatchCommand(t, server.URL, func(context.Context, mattermost.WatchOptions) error { return mattermost.ErrWatchAuthentication }, context.Background(), "--json", "watch", "town", "--team", "main")
	if _, ok := err.(watchTerminalFailure); !ok {
		t.Fatalf("err=%T %v", err, err)
	}
	if strings.Count(machineErr, "\n") != 1 || !strings.Contains(machineErr, `"type":"terminal"`) || strings.Contains(machineErr, "mm/v2/error") {
		t.Fatalf("stderr=%q", machineErr)
	}
}

func TestWatchMachineWriterFailureIsTerminalWithoutSecondObject(t *testing.T) {
	server := watchChannelServer(t)
	defer server.Close()
	setChannelEnvironment(t, server.URL)
	var stderr bytes.Buffer
	state := &rootState{streams: streams{in: strings.NewReader(""), out: shortWriter{}, err: &stderr}, deps: defaultDependencies(shortWriter{})}
	state.deps.watch = func(_ context.Context, options mattermost.WatchOptions) error {
		if options.Sink.Post(mattermost.WatchPost{ID: "p", ChannelID: "channel1", UserID: "u", CreateAt: 1, FileIDs: []string{}}, mattermost.Sequence{ConnectionID: "c", Number: 1}) != nil {
			return mattermost.ErrWatchSink
		}
		return nil
	}
	command := newRootWithState(state)
	command.SetArgs([]string{"--json", "watch", "town", "--team", "main"})
	err := command.ExecuteContext(context.Background())
	state.close()
	if _, ok := err.(watchOutputFailure); !ok || stderr.Len() != 0 {
		t.Fatalf("err=%T stderr=%q", err, stderr.String())
	}
}

func TestWatchMachineWarningsAreJSONLAndConsumed(t *testing.T) {
	server := watchChannelServer(t)
	defer server.Close()
	watch := func(_ context.Context, options mattermost.WatchOptions) error {
		if err := options.Sink.Post(mattermost.WatchPost{ID: "post", ChannelID: "channel1", UserID: "u2", SenderName: "arda", Message: "credential test-token", CreateAt: 1, FileIDs: []string{}}, mattermost.Sequence{ConnectionID: "connection", Number: 1}); err != nil {
			return mattermost.ErrWatchSink
		}
		return nil
	}
	stdout, stderr, err := runWatchCommand(t, server.URL, watch, context.Background(), "--json", "--no-redact", "watch", "town", "--team", "main")
	if err != nil || !strings.Contains(stdout, `"type":"posted"`) || strings.Contains(stdout, "test-token") {
		t.Fatalf("err=%v stdout=%q", err, stdout)
	}
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	if len(lines) != 1 || !strings.Contains(lines[0], `"type":"warning"`) || !strings.Contains(lines[0], `"code":"redaction_disabled"`) || strings.Contains(stderr, "warning: warning:") {
		t.Fatalf("stderr=%q", stderr)
	}
	registry, _ := mmSchema.Load()
	if err := registry.Validate("mm/v2/watch-diagnostic", strings.NewReader(lines[0])); err != nil {
		t.Fatal(err)
	}
}

func TestWatchHumanNoRedactWarningMatchesFrozenV1Text(t *testing.T) {
	server := watchChannelServer(t)
	defer server.Close()
	_, stderr, err := runWatchCommand(t, server.URL, func(context.Context, mattermost.WatchOptions) error { return nil }, context.Background(), "--no-redact", "watch", "town", "--team", "main")
	if err != nil || !strings.HasPrefix(stderr, "Warning: Secret redaction is disabled. Output may contain secrets.\nWatching #town (Ctrl+C to stop)\n") {
		t.Fatalf("err=%v stderr=%q", err, stderr)
	}
}

func TestWatchConsumesQueuedConfigurationWarningBeforeStreaming(t *testing.T) {
	server := watchChannelServer(t)
	defer server.Close()
	setChannelEnvironment(t, server.URL)
	var stdout, stderr bytes.Buffer
	state := &rootState{streams: streams{in: strings.NewReader(""), out: &stdout, err: &stderr}, deps: defaultDependencies(&stdout)}
	state.queueTypedMachineWarning("configuration_warning", "legacy configuration path in use")
	state.deps.watch = func(context.Context, mattermost.WatchOptions) error { return nil }
	command := newRootWithState(state)
	command.SetArgs([]string{"--json", "watch", "town", "--team", "main"})
	err := command.ExecuteContext(context.Background())
	state.close()
	if err != nil || !strings.Contains(stderr.String(), `"code":"configuration_warning"`) || strings.Contains(stderr.String(), "legacy configuration path in use\nwarning:") {
		t.Fatalf("err=%v stderr=%q", err, stderr.String())
	}
	if len(state.takeMachineWarnings()) != 0 {
		t.Fatal("warning was not consumed")
	}
}

func TestWatchTerminalClassificationsAreClosed(t *testing.T) {
	server := watchChannelServer(t)
	defer server.Close()
	tests := []struct {
		err            error
		code, recovery string
	}{{mattermost.ErrWatchAuthentication, "authentication", "check_token"}, {mattermost.ErrWatchRetries, "reconnect_exhausted", "retry_later"}, {mattermost.ErrInvalidWatchOptions, "invalid_options", "none"}, {context.Canceled, "canceled", "none"}, {errors.New("hostile remote token"), "watch_failed", "none"}}
	for _, test := range tests {
		_, stderr, err := runWatchCommand(t, server.URL, func(context.Context, mattermost.WatchOptions) error { return test.err }, context.Background(), "--json", "watch", "town", "--team", "main")
		if _, ok := err.(watchTerminalFailure); !ok || !strings.Contains(stderr, `"code":"`+test.code+`"`) || !strings.Contains(stderr, `"recovery":"`+test.recovery+`"`) || strings.Contains(stderr, "hostile remote token") {
			t.Fatalf("case=%s err=%T stderr=%q", test.code, err, stderr)
		}
	}
}

func TestWatchHumanTTYColorAndCanonicalSenderFallback(t *testing.T) {
	server := watchChannelServer(t)
	defer server.Close()
	for _, test := range []struct {
		args  []string
		color bool
	}{{[]string{"watch", "town", "--team", "main"}, true}, {[]string{"--no-color", "watch", "town", "--team", "main"}, false}} {
		setChannelEnvironment(t, server.URL)
		var stdout, stderr bytes.Buffer
		state := &rootState{streams: streams{in: strings.NewReader(""), out: &stdout, err: &stderr}, deps: defaultDependencies(&stdout)}
		state.deps.stdoutTTY = func() bool { return true }
		state.deps.watch = func(_ context.Context, options mattermost.WatchOptions) error {
			return options.Sink.Post(mattermost.WatchPost{ID: "p", ChannelID: "channel1", UserID: "user1", SenderName: "spoofed", Message: "a\tb\u00a0c\ufeffd\u0085e", CreateAt: 0, FileIDs: []string{}}, mattermost.Sequence{ConnectionID: "c", Number: 1})
		}
		command := newRootWithState(state)
		command.SetArgs(test.args)
		err := command.ExecuteContext(context.Background())
		state.close()
		if err != nil || !strings.Contains(stdout.String(), "arda") || strings.Contains(stdout.String(), "spoofed") || !strings.Contains(stdout.String(), `a b c d\u0085e`) || (strings.Contains(stdout.String(), "\x1b[") != test.color) {
			t.Fatalf("color=%v err=%v stdout=%q", test.color, err, stdout.String())
		}
	}
}

func TestWatchSignalDoesNotSuppressRuntimeWarningOutputFailure(t *testing.T) {
	home := t.TempDir()
	xdg := t.TempDir()
	writeRuntimeConfig(t, home, "url = \"https://example.com\"\ntoken = \"token\"\n")
	lookup := func(key string) (string, bool) {
		if key == "XDG_CONFIG_HOME" {
			return xdg, true
		}
		return "", false
	}
	var stdout bytes.Buffer
	state := &rootState{streams: streams{in: strings.NewReader(""), out: &stdout, err: shortWriter{}}, deps: defaultDependencies(&stdout)}
	state.deps.homeDir = func() (string, error) { return home, nil }
	state.deps.lookupEnv = lookup
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(ErrSignalCancellation)
	command := newRootWithState(state)
	command.SetArgs([]string{"watch", "town"})
	err := command.ExecuteContext(ctx)
	state.close()
	var outputFailure outputError
	if !errors.As(err, &outputFailure) {
		t.Fatalf("err=%T %v", err, err)
	}
}

func TestWatchSignalCauseDuringResolutionIsClean(t *testing.T) {
	started := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithCancelCause(context.Background())
	done := make(chan error, 1)
	go func() {
		_, _, err := runWatchCommand(t, server.URL, func(context.Context, mattermost.WatchOptions) error { return errors.New("watch unexpectedly started") }, ctx, "--json", "watch", "town")
		done <- err
	}()
	<-started
	cancel(ErrSignalCancellation)
	if err := <-done; err != nil {
		t.Fatalf("err=%v", err)
	}
}

func runWatchCommand(t *testing.T, serverURL string, watch func(context.Context, mattermost.WatchOptions) error, ctx context.Context, args ...string) (string, string, error) {
	t.Helper()
	setChannelEnvironment(t, serverURL)
	var stdout, stderr bytes.Buffer
	state := &rootState{streams: streams{in: strings.NewReader(""), out: &stdout, err: &stderr}, deps: defaultDependencies(&stdout)}
	state.deps.watch = watch
	command := newRootWithState(state)
	command.SetArgs(args)
	err := command.ExecuteContext(ctx)
	state.close()
	return stdout.String(), stderr.String(), err
}
func watchChannelServer(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v4/users/me":
			writeJSON(t, w, `{"id":"user1","username":"arda"}`)
		case "/api/v4/users/user1/teams":
			writeJSON(t, w, `[{"id":"team1","name":"main","display_name":"Main","type":"O"}]`)
		case "/api/v4/teams/team1/channels/name/town":
			writeJSON(t, w, `{"id":"channel1","team_id":"team1","type":"O","name":"town","display_name":"Town"}`)
		case "/api/v4/channels/channel1/members/user1":
			writeJSON(t, w, `{"channel_id":"channel1","user_id":"user1"}`)
		default:
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
}
