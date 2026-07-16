package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ardasevinc/mattermost-cli/internal/api"
	"github.com/ardasevinc/mattermost-cli/internal/config"
	"github.com/ardasevinc/mattermost-cli/internal/cursor"
	"github.com/ardasevinc/mattermost-cli/internal/mattermost"
	"github.com/ardasevinc/mattermost-cli/internal/presentation"
	"github.com/ardasevinc/mattermost-cli/internal/retrieval"
	mmSchema "github.com/ardasevinc/mattermost-cli/internal/schema"
)

func TestChannelJSONRunsValidatedReadPipeline(t *testing.T) {
	postTime := time.Now().Add(-time.Hour).UnixMilli()
	server := channelServer(t, func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v4/users/me":
			writeJSON(t, w, `{"id":"user1","username":"arda"}`)
		case "/api/v4/users/user1/teams":
			writeJSON(t, w, `[{"id":"team1","name":"main","display_name":"Main","type":"O"}]`)
		case "/api/v4/teams/team1/channels/name/town-square":
			writeJSON(t, w, `{"id":"channel1","team_id":"team1","type":"O","name":"town-square","display_name":"Town Square"}`)
		case "/api/v4/channels/channel1/posts":
			post := fmt.Sprintf(`{"id":"post1","channel_id":"channel1","user_id":"user1","message":"**hello**","create_at":%d,"delete_at":0,"root_id":"","reply_count":0}`, postTime)
			writeJSON(t, w, `{"order":["post1"],"posts":{"post1":`+post+`},"has_next":false}`)
		case "/api/v4/users/ids":
			if request.Method != http.MethodPost {
				t.Fatalf("users/ids method = %s", request.Method)
			}
			writeJSON(t, w, `[{"id":"user1","username":"arda"}]`)
		default:
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.String())
		}
	})
	defer server.Close()

	stdout, stderr, code := executeChannel(t, server.URL, "--json", "--no-threads", "channel", "town-square", "--team", "main", "--limit", "1")
	if code != 0 || stderr != "" {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	registry, err := mmSchema.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Validate("mm/v2/channel", strings.NewReader(stdout)); err != nil {
		t.Fatalf("machine output did not validate: %v\n%s", err, stdout)
	}
	var document struct {
		Schema string `json:"schema"`
		Data   struct {
			Messages []struct{ Text, User string } `json:"messages"`
			Metadata struct {
				Completeness string `json:"completeness"`
				Selection    struct {
					SelectedCount int `json:"selectedCount"`
				} `json:"selection"`
			} `json:"metadata"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &document); err != nil {
		t.Fatal(err)
	}
	if document.Schema != "mm/v2/channel" || len(document.Data.Messages) != 1 || document.Data.Messages[0].Text != "**hello**" || document.Data.Messages[0].User != "you" || document.Data.Metadata.Completeness != "complete" || document.Data.Metadata.Selection.SelectedCount != 1 {
		t.Fatalf("unexpected document: %+v", document)
	}
}

func TestChannelRejectsInvalidCursorBeforeNetwork(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	_, stderr, code := executeChannel(t, server.URL, "channel", "town-square", "--cursor", "not-a-cursor")
	if code != 2 || !strings.Contains(stderr, "invalid channel cursor") || requests.Load() != 0 {
		t.Fatalf("exit=%d stderr=%q requests=%d", code, stderr, requests.Load())
	}
}

func TestChannelJSONFailureUsesMachineErrorSchema(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("network must not be used") }))
	defer server.Close()
	stdout, stderr, code := executeChannel(t, server.URL, "--json", "channel", "town-square", "--cursor", "not-a-cursor")
	if code != 2 || stdout != "" || strings.HasPrefix(stderr, "error:") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	registry, err := mmSchema.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Validate("mm/v2/error", strings.NewReader(stderr)); err != nil {
		t.Fatalf("machine error did not validate: %v\n%s", err, stderr)
	}
}

func TestChannelMachineOutputFailureDistinguishesZeroFromPartialWrite(t *testing.T) {
	server := basicEmptyChannelServer(t, false)
	defer server.Close()
	for _, test := range []struct {
		name       string
		writer     io.Writer
		wantStderr bool
	}{
		{name: "zero", writer: zeroErrorWriter{}, wantStderr: true},
		{name: "partial", writer: shortWriter{}, wantStderr: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			setChannelEnvironment(t, server.URL)
			var stderr bytes.Buffer
			code := Execute(context.Background(), []string{"--json", "--no-threads", "channel", "town-square"}, strings.NewReader(""), test.writer, &stderr)
			if code != 3 || (stderr.Len() > 0) != test.wantStderr {
				t.Fatalf("exit=%d stderr=%q", code, stderr.String())
			}
			if test.wantStderr && !strings.Contains(stderr.String(), `"code":"internal"`) {
				t.Fatalf("stderr=%q, want internal machine error", stderr.String())
			}
		})
	}
}

func TestNormalizeReadPostsBatchesMoreThanTwoHundredUsers(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v4/users/ids" {
			t.Errorf("unexpected request path %q", request.URL.Path)
			http.Error(writer, "unexpected", http.StatusNotFound)
			return
		}
		calls.Add(1)
		var ids []string
		if err := json.NewDecoder(request.Body).Decode(&ids); err != nil {
			t.Error(err)
			return
		}
		users := make([]map[string]string, len(ids))
		for index, id := range ids {
			users[index] = map[string]string{"id": id, "username": "user_" + id}
		}
		_ = json.NewEncoder(writer).Encode(users)
	}))
	defer server.Close()
	client, err := api.New(server.URL, "token")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	runtime := &Runtime{Config: config.Resolved{URL: server.URL, Token: "token", Redact: true}, Client: client, Users: mattermost.NewUsers(client)}
	posts := make([]mattermost.Post, 201)
	for index := range posts {
		posts[index] = mattermost.Post{ID: fmt.Sprintf("post%d", index), ChannelID: "channel1", UserID: fmt.Sprintf("user%d", index), Message: "hello", CreateAt: 1, UpdateAt: 1}
	}
	messages, _, err := normalizeReadPosts(context.Background(), runtime, posts, "me")
	if err != nil || len(messages) != len(posts) || calls.Load() != 2 {
		t.Fatalf("messages=%d calls=%d err=%v", len(messages), calls.Load(), err)
	}
}

func TestFailedThreadRootAlwaysMasksActiveCredential(t *testing.T) {
	runtime := &Runtime{Config: config.Resolved{Token: "secret-token", Redact: false}}
	metadata, redactions := processedVisibleThreads(retrieval.VisibleThreadsMetadata{
		Status: retrieval.VisibleThreadsPartial, FailedRootIDs: []string{"secret-token"},
	}, runtime)
	if len(metadata.FailedRootIDs) != 1 || metadata.FailedRootIDs[0] == "secret-token" || !strings.Contains(metadata.FailedRootIDs[0], "REDACTED") || len(redactions) != 1 || redactions[0].Field != "retrieval.failedRootId" {
		t.Fatalf("metadata=%+v redactions=%+v", metadata, redactions)
	}
	if got := presentation.Preprocess(metadata.FailedRootIDs[0], []string{"secret-token"}).Text; got == "secret-token" {
		t.Fatal("failed root was not masked")
	}
}

func TestChannelRejectsCursorWithExplicitSinceBeforeNetwork(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	since := time.Now().Add(-24 * time.Hour).UnixMilli()
	encoded, err := cursor.EncodeChannelHistory(cursor.ChannelHistory{Version: 1, Scope: "channel", ChannelID: "channel1", Boundary: cursor.Boundary{CreateAt: time.Now().UnixMilli(), ID: "post1"}, Since: &since})
	if err != nil {
		t.Fatal(err)
	}
	_, stderr, code := executeChannel(t, server.URL, "channel", "town-square", "--cursor", encoded, "--since", "1d")
	if code != 2 || !strings.Contains(stderr, "cannot be combined") || requests.Load() != 0 {
		t.Fatalf("exit=%d stderr=%q requests=%d", code, stderr, requests.Load())
	}
}

func TestDurationAndLimitValidation(t *testing.T) {
	now := time.UnixMilli(10 * int64(24*time.Hour/time.Millisecond))
	boundary, err := durationBoundary("2d", now)
	if err != nil || boundary != now.Add(-48*time.Hour).UnixMilli() {
		t.Fatalf("durationBoundary = %d, %v", boundary, err)
	}
	for _, invalid := range []string{"", "01", "0", "-1", "1.5", "9007199254740992"} {
		if _, err := positiveInteger(invalid); err == nil {
			t.Errorf("positiveInteger(%q) succeeded", invalid)
		}
	}
}

func TestChannelCompleteEmptyHumanOutput(t *testing.T) {
	server := basicEmptyChannelServer(t, false)
	defer server.Close()
	stdout, stderr, code := executeChannel(t, server.URL, "--no-threads", "channel", "town-square")
	if code != 0 || stderr != "" || stdout != "No messages found.\n" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestChannelUnknownEmptyContinuationPreservesCursor(t *testing.T) {
	server := basicEmptyChannelServer(t, true)
	defer server.Close()
	since := time.Now().Add(-24 * time.Hour).UnixMilli()
	encoded, err := cursor.EncodeChannelHistory(cursor.ChannelHistory{Version: 1, Scope: "channel", ChannelID: "channel1", Boundary: cursor.Boundary{CreateAt: time.Now().UnixMilli(), ID: "post1"}, Since: &since})
	if err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := executeChannel(t, server.URL, "--no-threads", "channel", "town-square", "--cursor", encoded)
	if code != 0 || stderr != "" || !strings.Contains(stdout, "completeness unknown") || !strings.Contains(stdout, "Next cursor: `"+encoded+"`") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func basicEmptyChannelServer(t *testing.T, uncertain bool) *httptest.Server {
	t.Helper()
	return channelServer(t, func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v4/users/me":
			writeJSON(t, w, `{"id":"user1","username":"arda"}`)
		case "/api/v4/users/user1/teams":
			writeJSON(t, w, `[{"id":"team1","name":"main","display_name":"Main","type":"O"}]`)
		case "/api/v4/teams/team1/channels/name/town-square":
			writeJSON(t, w, `{"id":"channel1","team_id":"team1","type":"O","name":"town-square","display_name":"Town Square"}`)
		case "/api/v4/channels/channel1/posts":
			if uncertain {
				writeJSON(t, w, `{"order":[],"posts":{},"has_next":true}`)
			} else {
				writeJSON(t, w, `{"order":[],"posts":{},"has_next":false}`)
			}
		default:
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.String())
		}
	})
}

func channelServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	return httptest.NewServer(handler)
}

func executeChannel(t *testing.T, serverURL string, args ...string) (string, string, int) {
	t.Helper()
	setChannelEnvironment(t, serverURL)
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), args, strings.NewReader(""), &stdout, &stderr)
	return stdout.String(), stderr.String(), code
}

func setChannelEnvironment(t *testing.T, serverURL string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("MM_URL", serverURL)
	t.Setenv("MM_TOKEN", "test-token")
}

type zeroErrorWriter struct{}

func (zeroErrorWriter) Write([]byte) (int, error) { return 0, errors.New("closed") }

func writeJSON(t *testing.T, writer http.ResponseWriter, body string) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	if _, err := writer.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
}
