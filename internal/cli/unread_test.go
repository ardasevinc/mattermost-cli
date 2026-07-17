package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	mmSchema "github.com/ardasevinc/mattermost-cli/v2/internal/schema"
)

func TestUnreadValidatesFlagsBeforeNetwork(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	for _, args := range [][]string{{"unread", "--peek", "0"}, {"unread", "--peek", "nope"}, {"unread", "--team", " "}} {
		_, _, code := executeChannel(t, server.URL, args...)
		if code != 2 || requests.Load() != 0 {
			t.Fatalf("args=%v exit=%d requests=%d", args, code, requests.Load())
		}
	}
}

func TestUnreadMachineVerticalSliceBindsAllChannelTypesAndPeekOrder(t *testing.T) {
	var teamCalls, postCalls, userCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v4/users/me":
			writeJSON(t, w, `{"id":"u","username":"me"}`)
		case "/api/v4/users/u/teams":
			teamCalls.Add(1)
			writeJSON(t, w, `[{"id":"t","name":"core","display_name":"Core","type":"O"}]`)
		case "/api/v4/users/u/channels":
			writeJSON(t, w, `[
				{"id":"d","team_id":"","type":"D","name":"u__peer","display_name":"","total_msg_count":4},
				{"id":"g","team_id":"","type":"G","name":"opaque","display_name":"Crew","total_msg_count":3},
				{"id":"o","team_id":"t","type":"O","name":"general","display_name":"General","total_msg_count":2},
				{"id":"p","team_id":"t","type":"P","name":"private","display_name":"Private","total_msg_count":2}]`)
		case "/api/v4/users/u/teams/t/channels/members":
			writeJSON(t, w, `[
				{"channel_id":"d","user_id":"u","msg_count":1,"mention_count":3,"last_viewed_at":0},
				{"channel_id":"g","user_id":"u","msg_count":1,"mention_count":2,"last_viewed_at":0},
				{"channel_id":"o","user_id":"u","msg_count":1,"mention_count":1,"last_viewed_at":0},
				{"channel_id":"p","user_id":"u","msg_count":1,"mention_count":0,"last_viewed_at":0}]`)
		case "/api/v4/channels/d/posts":
			postCalls.Add(1)
			post := `{"id":"pd","channel_id":"d","user_id":"peer","message":"**short**\nlong markdown","create_at":2,"update_at":2,"delete_at":0,"root_id":"","reply_count":0}`
			writeJSON(t, w, `{"order":["pd"],"posts":{"pd":`+post+`},"has_next":false}`)
		case "/api/v4/channels/g/posts", "/api/v4/channels/o/posts", "/api/v4/channels/p/posts":
			postCalls.Add(1)
			writeJSON(t, w, `{"order":[],"posts":{},"has_next":false}`)
		case "/api/v4/users/ids":
			userCalls.Add(1)
			writeJSON(t, w, `[{"id":"peer","username":"bob"}]`)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()
	stdout, stderr, code := executeChannel(t, server.URL, "--json", "unread", "--team", "core", "--peek", "2")
	if code != 0 || stderr != "" {
		t.Fatalf("exit=%d stderr=%q stdout=%s", code, stderr, stdout)
	}
	registry, err := mmSchema.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Validate("mm/v2/unread", strings.NewReader(stdout)); err != nil {
		t.Fatalf("schema: %v\n%s", err, stdout)
	}
	var document struct {
		Data struct {
			Unread []struct {
				Channel struct{ ID, Type, Name string } `json:"channel"`
				Mention int64                           `json:"mentionCount"`
			} `json:"unread"`
			Peek []struct {
				Channel  struct{ ID string }     `json:"channel"`
				Messages []struct{ Text string } `json:"messages"`
			} `json:"peek"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &document); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, len(document.Data.Unread))
	for i := range ids {
		ids[i] = document.Data.Unread[i].Channel.ID
		if document.Data.Peek[i].Channel.ID != ids[i] {
			t.Fatalf("peek order mismatch")
		}
	}
	if strings.Join(ids, ",") != "d,g,o,p" || len(document.Data.Peek) != 4 || len(document.Data.Peek[1].Messages) != 0 || document.Data.Unread[0].Channel.Name != "@bob" || !strings.Contains(document.Data.Peek[0].Messages[0].Text, "\n") {
		t.Fatalf("document=%+v", document)
	}
	if teamCalls.Load() != 2 || postCalls.Load() != 4 || userCalls.Load() != 1 {
		t.Fatalf("teams=%d posts=%d users=%d", teamCalls.Load(), postCalls.Load(), userCalls.Load())
	}
}

func TestUnreadEmptyMachineAndHumanAreExact(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v4/users/me":
			writeJSON(t, w, `{"id":"u","username":"me"}`)
		case "/api/v4/users/u/teams":
			writeJSON(t, w, `[{"id":"t","name":"core","display_name":"Core","type":"O"}]`)
		case "/api/v4/users/u/channels", "/api/v4/users/u/teams/t/channels/members":
			writeJSON(t, w, `[]`)
		default:
			t.Fatalf("unexpected %s", r.URL.Path)
		}
	}))
	defer server.Close()
	stdout, stderr, code := executeChannel(t, server.URL, "--json", "unread")
	if code != 0 || stderr != "" || stdout != `{"schema":"mm/v2/unread","data":{"unread":[],"peek":[]}}`+"\n" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	stdout, stderr, code = executeChannel(t, server.URL, "unread")
	if code != 0 || stderr != "" || stdout != "All caught up!\n" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestUnreadPipeUsesSummaryThenMarkdown(t *testing.T) {
	server := singleUnreadServer(t, "hello")
	defer server.Close()
	stdout, stderr, code := executeChannel(t, server.URL, "unread", "--peek", "1")
	if code != 0 || stderr != "" || !strings.HasPrefix(stdout, "Unread Channels:\n") || !strings.Contains(stdout, "\n\n## Group DM: Crew") || !strings.Contains(stdout, "hello") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestUnreadTTYUsesExistingPrettySections(t *testing.T) {
	server := singleUnreadServer(t, "hello")
	defer server.Close()
	setChannelEnvironment(t, server.URL)
	var stdout, stderr bytes.Buffer
	state := &rootState{streams: streams{in: strings.NewReader(""), out: &stdout, err: &stderr}, deps: defaultDependencies(&stdout)}
	state.deps.stdoutTTY = func() bool { return true }
	command := newRootWithState(state)
	command.SetArgs([]string{"--no-color", "unread", "--peek", "1"})
	err := command.ExecuteContext(context.Background())
	state.close()
	if err != nil || stderr.Len() != 0 || !strings.Contains(stdout.String(), "\n\nGroup DM: Crew") || strings.Contains(stdout.String(), "## Group DM") {
		t.Fatalf("error=%v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
}

func TestUnreadIncompletePeekFailsWithEmptyMachineStdout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v4/users/me":
			writeJSON(t, w, `{"id":"u","username":"me"}`)
		case "/api/v4/users/u/teams":
			writeJSON(t, w, `[{"id":"t","name":"core","display_name":"Core","type":"O"}]`)
		case "/api/v4/users/u/channels":
			writeJSON(t, w, `[{"id":"g","team_id":"","type":"G","name":"opaque","display_name":"Crew","total_msg_count":2}]`)
		case "/api/v4/users/u/teams/t/channels/members":
			writeJSON(t, w, `[{"channel_id":"g","user_id":"u","msg_count":1,"mention_count":0,"last_viewed_at":0}]`)
		case "/api/v4/channels/g/posts":
			writeJSON(t, w, `{"order":[],"posts":{},"has_next":true}`)
		default:
			t.Fatalf("unexpected %s", r.URL.Path)
		}
	}))
	defer server.Close()
	stdout, stderr, code := executeChannel(t, server.URL, "--json", "unread", "--peek", "1")
	if code == 0 || stdout != "" || stderr == "" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestUnreadMachineWriterFailure(t *testing.T) {
	server := singleUnreadServer(t, "hello")
	defer server.Close()
	setChannelEnvironment(t, server.URL)
	var stderr bytes.Buffer
	code := Execute(context.Background(), []string{"--json", "unread"}, strings.NewReader(""), zeroErrorWriter{}, &stderr)
	if code != 3 || !strings.Contains(stderr.String(), `"code":"internal"`) {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
}

func TestUnreadNestedCredentialIsNormalizedBeforeEnvelope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v4/users/me":
			writeJSON(t, w, `{"id":"u","username":"me"}`)
		case "/api/v4/users/u/teams":
			writeJSON(t, w, `[{"id":"t","name":"core","display_name":"Core","type":"O"}]`)
		case "/api/v4/users/u/channels":
			writeJSON(t, w, `[{"id":"g","team_id":"","type":"G","name":"opaque","display_name":"Crew","total_msg_count":2}]`)
		case "/api/v4/users/u/teams/t/channels/members":
			writeJSON(t, w, `[{"channel_id":"g","user_id":"u","msg_count":1,"mention_count":0,"last_viewed_at":0}]`)
		case "/api/v4/channels/g/posts":
			post := `{"id":"p","channel_id":"g","user_id":"u","message":"safe","create_at":2,"update_at":2,"delete_at":0,"root_id":"","reply_count":0,"props":{"attachments":[{"fields":[{"title":"secret","value":"test-token","short":true}]}]}}`
			writeJSON(t, w, `{"order":["p"],"posts":{"p":`+post+`},"has_next":false}`)
		case "/api/v4/users/ids":
			writeJSON(t, w, `[{"id":"u","username":"me"}]`)
		default:
			t.Fatalf("unexpected %s", r.URL.Path)
		}
	}))
	defer server.Close()
	stdout, stderr, code := executeChannel(t, server.URL, "--json", "unread", "--peek", "1")
	if code != 0 || stderr != "" || strings.Contains(stdout, "test-token") || !strings.Contains(stdout, "[REDACTED:mattermost_credential]") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestUnreadCancellationPropagatesWithoutOutput(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v4/users/me":
			writeJSON(t, w, `{"id":"u","username":"me"}`)
		case "/api/v4/users/u/teams":
			writeJSON(t, w, `[{"id":"t","name":"core","display_name":"Core","type":"O"}]`)
		case "/api/v4/users/u/channels":
			writeJSON(t, w, `[{"id":"g","team_id":"","type":"G","name":"opaque","display_name":"Crew","total_msg_count":2}]`)
		case "/api/v4/users/u/teams/t/channels/members":
			writeJSON(t, w, `[{"channel_id":"g","user_id":"u","msg_count":1,"mention_count":0,"last_viewed_at":0}]`)
		case "/api/v4/channels/g/posts":
			close(started)
			<-r.Context().Done()
		default:
			t.Fatalf("unexpected %s", r.URL.Path)
		}
	}))
	defer server.Close()
	setChannelEnvironment(t, server.URL)
	ctx, cancel := context.WithCancel(context.Background())
	var stdout, stderr bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- Execute(ctx, []string{"--json", "unread", "--peek", "1"}, strings.NewReader(""), &stdout, &stderr)
	}()
	<-started
	cancel()
	if code := <-done; code == 0 || stdout.Len() != 0 || stderr.Len() == 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func singleUnreadServer(t *testing.T, message string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v4/users/me":
			writeJSON(t, w, `{"id":"u","username":"me"}`)
		case "/api/v4/users/u/teams":
			writeJSON(t, w, `[{"id":"t","name":"core","display_name":"Core","type":"O"}]`)
		case "/api/v4/users/u/channels":
			writeJSON(t, w, `[{"id":"g","team_id":"","type":"G","name":"opaque","display_name":"Crew","total_msg_count":2}]`)
		case "/api/v4/users/u/teams/t/channels/members":
			writeJSON(t, w, `[{"channel_id":"g","user_id":"u","msg_count":1,"mention_count":0,"last_viewed_at":0}]`)
		case "/api/v4/channels/g/posts":
			post := fmt.Sprintf(`{"id":"p","channel_id":"g","user_id":"u","message":%q,"create_at":2,"update_at":2,"delete_at":0,"root_id":"","reply_count":0}`, message)
			writeJSON(t, w, `{"order":["p"],"posts":{"p":`+post+`},"has_next":false}`)
		case "/api/v4/users/ids":
			writeJSON(t, w, `[{"id":"u","username":"me"}]`)
		default:
			t.Fatalf("unexpected %s", r.URL.Path)
		}
	}))
}
