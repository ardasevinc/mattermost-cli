//go:build darwin || linux

package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	mmSchema "github.com/ardasevinc/mattermost-cli/internal/schema"
	"github.com/ardasevinc/mattermost-cli/internal/stagestore"
)

type failOnRead struct{ reads atomic.Int32 }

func (r *failOnRead) Read([]byte) (int, error) {
	r.reads.Add(1)
	return 0, errors.New("stdin must not be read")
}

func stageTargetServer(t *testing.T, methods *[]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		*methods = append(*methods, request.Method+" "+request.URL.Path)
		if request.Header.Get("Authorization") != "Bearer test-token" {
			t.Fatalf("authorization=%q", request.Header.Get("Authorization"))
		}
		switch request.URL.Path {
		case "/api/v4/users/me":
			writeJSON(t, writer, `{"id":"user1","username":"arda"}`)
		case "/api/v4/channels/channel1":
			writeJSON(t, writer, `{"id":"channel1","team_id":"team1","type":"O","name":"town-square","display_name":"Town Square"}`)
		case "/api/v4/channels/channel1/members/user1":
			writeJSON(t, writer, `{"channel_id":"channel1","user_id":"user1"}`)
		default:
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.String())
		}
	}))
}

func setStageEnvironment(t *testing.T, serverURL, stateRoot string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_STATE_HOME", stateRoot)
	t.Setenv("MM_URL", serverURL)
	t.Setenv("MM_TOKEN", "test-token")
}

func TestStageSendDryRunSkipsContentAndPersistence(t *testing.T) {
	var methods []string
	server := stageTargetServer(t, &methods)
	defer server.Close()
	stateRoot := filepath.Join(t.TempDir(), "state")
	setStageEnvironment(t, server.URL, stateRoot)
	stdin := new(failOnRead)
	var stdout, stderr bytes.Buffer
	code := Execute(t.Context(), []string{"--json", "stage", "send", "channel", "channel1", "--dry-run"}, stdin, &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 || stdin.reads.Load() != 0 {
		t.Fatalf("exit=%d reads=%d stdout=%q stderr=%q", code, stdin.reads.Load(), stdout.String(), stderr.String())
	}
	registry, err := mmSchema.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Validate("mm/v2/stage-preview", bytes.NewReader(stdout.Bytes())); err != nil {
		t.Fatalf("schema: %v\n%s", err, stdout.String())
	}
	if strings.Contains(stdout.String(), "test-token") || !strings.Contains(stdout.String(), `"persist":false`) || !strings.Contains(stdout.String(), `"contentValidated":false`) {
		t.Fatalf("stdout=%s", stdout.String())
	}
	for _, method := range methods {
		if !strings.HasPrefix(method, http.MethodGet+" ") {
			t.Fatalf("dry-run mutation request: %s", method)
		}
	}
	if _, err := os.Stat(stateRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry-run created local state: %v", err)
	}
}

func TestStructuredStagePreservesShortAndLongMarkdownWithoutRemoteMutation(t *testing.T) {
	bodies := map[string]string{
		"short": "# hello\n\n- **bold**\n- [link](https://example.com)",
		"long":  "# long\n\n" + strings.Repeat("- **item**\n", 1488),
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			var methods []string
			server := stageTargetServer(t, &methods)
			defer server.Close()
			stateRoot := filepath.Join(t.TempDir(), "state")
			setStageEnvironment(t, server.URL, stateRoot)
			request := map[string]any{
				"schema": "mm/v2/stage-request", "persist": true, "requestId": "test-" + name,
				"operation": "create_post",
				"target":    map[string]any{"kind": "conversation", "conversationType": "channel", "selector": map[string]any{"by": "id", "value": "channel1"}, "team": nil},
				"body":      body, "emoji": nil, "attachments": []any{},
			}
			encoded, err := json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			code := Execute(t.Context(), []string{"stage", "--from-json"}, bytes.NewReader(encoded), &stdout, &stderr)
			if code != 0 || stderr.Len() != 0 {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			registry, err := mmSchema.Load()
			if err != nil {
				t.Fatal(err)
			}
			if err := registry.Validate("mm/v2/stage-receipt", bytes.NewReader(stdout.Bytes())); err != nil {
				t.Fatalf("schema: %v\n%s", err, stdout.String())
			}
			if strings.Contains(stdout.String(), body) || strings.Contains(stdout.String(), "test-token") {
				t.Fatalf("receipt leaked content or credential")
			}
			var receipt stageoutputReceipt
			if err := json.Unmarshal(stdout.Bytes(), &receipt); err != nil {
				t.Fatal(err)
			}
			paths, err := stagestore.ResolvePaths(t.TempDir(), func(key string) (string, bool) { return stateRoot, key == "XDG_STATE_HOME" })
			if err != nil {
				t.Fatal(err)
			}
			store, err := stagestore.OpenReadOnly(t.Context(), paths.DBPath)
			if err != nil {
				t.Fatal(err)
			}
			detail, err := store.Show(t.Context(), receipt.Stage.StageID)
			closeErr := store.Close()
			if err != nil || closeErr != nil {
				t.Fatalf("show=%v close=%v", err, closeErr)
			}
			if string(detail.Body) != body {
				t.Fatalf("body changed: got bytes=%d want=%d", len(detail.Body), len(body))
			}
			for _, method := range methods {
				if !strings.HasPrefix(method, http.MethodGet+" ") {
					t.Fatalf("staging dispatched mutation: %s", method)
				}
			}
		})
	}
}

type stageoutputReceipt struct {
	Stage struct {
		StageID string `json:"stageId"`
	} `json:"stage"`
}

func TestStructuredStageRejectsActiveCredentialBeforePersistence(t *testing.T) {
	var methods []string
	server := stageTargetServer(t, &methods)
	defer server.Close()
	stateRoot := filepath.Join(t.TempDir(), "state")
	setStageEnvironment(t, server.URL, stateRoot)
	request := `{"schema":"mm/v2/stage-request","persist":true,"requestId":"credential-test","operation":"create_post","target":{"kind":"conversation","conversationType":"channel","selector":{"by":"id","value":"channel1"},"team":null},"body":"do not send test-token ever","emoji":null,"attachments":[]}`
	var stdout, stderr bytes.Buffer
	code := Execute(t.Context(), []string{"stage", "--from-json"}, strings.NewReader(request), &stdout, &stderr)
	if code != 2 || stdout.Len() != 0 || strings.Contains(stderr.String(), "test-token") || !strings.Contains(stderr.String(), `"code":"invalid_input"`) {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	paths, err := stagestore.ResolvePaths(t.TempDir(), func(key string) (string, bool) { return stateRoot, key == "XDG_STATE_HOME" })
	if err != nil {
		t.Fatal(err)
	}
	store, err := stagestore.OpenReadOnly(t.Context(), paths.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	page, err := store.ListRecords(t.Context(), stagestore.ListOptions{Limit: 10})
	closeErr := store.Close()
	if err != nil || closeErr != nil || len(page.Records) != 0 {
		t.Fatalf("list=%v close=%v records=%d", err, closeErr, len(page.Records))
	}
}

func TestStructuredStageReplayUsesStoredTargetAndConflictingReuseFailsClosed(t *testing.T) {
	var methods []string
	server := stageTargetServer(t, &methods)
	defer server.Close()
	stateRoot := filepath.Join(t.TempDir(), "state")
	setStageEnvironment(t, server.URL, stateRoot)
	request := func(body string) string {
		encoded, err := json.Marshal(map[string]any{
			"schema": "mm/v2/stage-request", "persist": true, "requestId": "stable-replay",
			"operation": "create_post",
			"target":    map[string]any{"kind": "conversation", "conversationType": "channel", "selector": map[string]any{"by": "id", "value": "channel1"}, "team": nil},
			"body":      body, "emoji": nil, "attachments": []any{},
		})
		if err != nil {
			t.Fatal(err)
		}
		return string(encoded)
	}
	run := func(body string) (int, string, string) {
		var stdout, stderr bytes.Buffer
		code := Execute(t.Context(), []string{"stage", "--from-json"}, strings.NewReader(request(body)), &stdout, &stderr)
		return code, stdout.String(), stderr.String()
	}
	code, firstOut, firstErr := run("same **markdown**")
	if code != 0 || firstErr != "" || !strings.Contains(firstOut, `"replayed":false`) {
		t.Fatalf("first exit=%d stdout=%q stderr=%q", code, firstOut, firstErr)
	}
	beforeReplay := len(methods)
	code, replayOut, replayErr := run("same **markdown**")
	if code != 0 || replayErr != "" || !strings.Contains(replayOut, `"replayed":true`) {
		t.Fatalf("replay exit=%d stdout=%q stderr=%q", code, replayOut, replayErr)
	}
	if got := methods[beforeReplay:]; len(got) != 1 || got[0] != "GET /api/v4/users/me" {
		t.Fatalf("replay re-resolved target: %v", got)
	}
	beforeConflict := len(methods)
	code, conflictOut, conflictErr := run("changed **markdown**")
	if code != 6 || conflictOut != "" || !strings.Contains(conflictErr, `"code":"state_conflict"`) {
		t.Fatalf("conflict exit=%d stdout=%q stderr=%q", code, conflictOut, conflictErr)
	}
	if got := methods[beforeConflict:]; len(got) != 1 || got[0] != "GET /api/v4/users/me" {
		t.Fatalf("conflict re-resolved target: %v", got)
	}
}

func TestConversationCreationStagesExactParticipantsWithoutRemoteMutation(t *testing.T) {
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		methods = append(methods, request.Method+" "+request.URL.Path)
		switch request.URL.Path {
		case "/api/v4/users/me":
			writeJSON(t, writer, `{"id":"self","username":"arda"}`)
		case "/api/v4/users/username/alice":
			writeJSON(t, writer, `{"id":"z-peer","username":"alice"}`)
		case "/api/v4/users/username/bob":
			writeJSON(t, writer, `{"id":"a-peer","username":"bob"}`)
		case "/api/v4/users/username/hakan":
			writeJSON(t, writer, `{"id":"h-peer","username":"hakan"}`)
		default:
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.String())
		}
	}))
	defer server.Close()
	t.Run("structured DM preview", func(t *testing.T) {
		stateRoot := filepath.Join(t.TempDir(), "state")
		setStageEnvironment(t, server.URL, stateRoot)
		request := `{"schema":"mm/v2/stage-request","persist":false,"requestId":null,"operation":"resolve_dm","target":{"kind":"user","username":"hakan"},"body":null,"emoji":null,"attachments":[]}`
		var stdout, stderr bytes.Buffer
		code := Execute(t.Context(), []string{"stage", "--from-json"}, strings.NewReader(request), &stdout, &stderr)
		if code != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), `"channelId":null`) || !strings.Contains(stdout.String(), `"channelType":"dm"`) || !strings.Contains(stdout.String(), `"participantIds":["h-peer"]`) {
			t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		}
		registry, _ := mmSchema.Load()
		if err := registry.Validate("mm/v2/stage-preview", bytes.NewReader(stdout.Bytes())); err != nil {
			t.Fatalf("schema: %v\n%s", err, stdout.String())
		}
		if _, err := os.Stat(stateRoot); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("preview created state: %v", err)
		}
	})
	t.Run("human group stage", func(t *testing.T) {
		stateRoot := filepath.Join(t.TempDir(), "state")
		setStageEnvironment(t, server.URL, stateRoot)
		stdin := new(failOnRead)
		var stdout, stderr bytes.Buffer
		code := Execute(t.Context(), []string{"--json", "stage", "group-create", "alice", "bob", "--request-id", "group-create-1"}, stdin, &stdout, &stderr)
		if code != 0 || stderr.Len() != 0 || stdin.reads.Load() != 0 || !strings.Contains(stdout.String(), `"operation":"resolve_group_dm"`) || !strings.Contains(stdout.String(), `"participantIds":["a-peer","z-peer"]`) {
			t.Fatalf("exit=%d reads=%d stdout=%q stderr=%q", code, stdin.reads.Load(), stdout.String(), stderr.String())
		}
		registry, _ := mmSchema.Load()
		if err := registry.Validate("mm/v2/stage-receipt", bytes.NewReader(stdout.Bytes())); err != nil {
			t.Fatalf("schema: %v\n%s", err, stdout.String())
		}
		var receipt stageoutputReceipt
		if err := json.Unmarshal(stdout.Bytes(), &receipt); err != nil {
			t.Fatal(err)
		}
		detail := loadStoredStage(t, stateRoot, receipt.Stage.StageID)
		if detail.Operation != stagestore.ResolveGroupDM || detail.Body != nil || len(detail.Attachments) != 0 {
			t.Fatalf("detail=%+v", detail)
		}
	})
	for _, method := range methods {
		if !strings.HasPrefix(method, http.MethodGet+" ") {
			t.Fatalf("conversation staging dispatched mutation: %s", method)
		}
	}
}
