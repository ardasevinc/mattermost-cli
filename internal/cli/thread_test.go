package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ardasevinc/mattermost-cli/internal/api"
	"github.com/ardasevinc/mattermost-cli/internal/config"
	"github.com/ardasevinc/mattermost-cli/internal/mattermost"
	mmSchema "github.com/ardasevinc/mattermost-cli/internal/schema"
)

func TestThreadJSONRunsRootBoundReadPipeline(t *testing.T) {
	created := time.Now().Add(-time.Hour).UnixMilli()
	server := threadServer(t, created, true)
	defer server.Close()
	stdout, stderr, code := executeChannel(t, server.URL, "--json", "thread", "root")
	if code != 0 || stderr != "" {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	registry, err := mmSchema.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Validate("mm/v2/thread", strings.NewReader(stdout)); err != nil {
		t.Fatalf("schema validation: %v\n%s", err, stdout)
	}
	var document struct {
		Data struct {
			Root struct {
				ID      string `json:"id"`
				Replies []struct {
					ID string `json:"id"`
				} `json:"replies"`
			} `json:"root"`
			Metadata struct {
				Completeness   string `json:"completeness"`
				VisibleThreads struct {
					HydratedRootCount int `json:"hydratedRootCount"`
				} `json:"visibleThreads"`
			} `json:"metadata"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &document); err != nil {
		t.Fatal(err)
	}
	if document.Data.Root.ID != "root" || len(document.Data.Root.Replies) != 1 || document.Data.Root.Replies[0].ID != "reply" || document.Data.Metadata.Completeness != "complete" || document.Data.Metadata.VisibleThreads.HydratedRootCount != 1 {
		t.Fatalf("unexpected thread document: %+v", document)
	}
}

func TestThreadRejectsInvalidPostIDBeforeNetwork(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	_, stderr, code := executeChannel(t, server.URL, "thread", "not/a/post")
	if code != 2 || !strings.Contains(stderr, "post ID") || requests.Load() != 0 {
		t.Fatalf("exit=%d stderr=%q requests=%d", code, stderr, requests.Load())
	}
}

func TestResolvedReadChannelAcceptsSelfDM(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v4/channels/dm1":
			writeJSON(t, writer, `{"id":"dm1","team_id":"","type":"D","name":"user1__user1","display_name":""}`)
		case "/api/v4/users/user1":
			writeJSON(t, writer, `{"id":"user1","username":"arda"}`)
		default:
			t.Errorf("unexpected request %s", request.URL.Path)
			http.Error(writer, "unexpected", http.StatusNotFound)
		}
	}))
	defer server.Close()
	client, err := api.New(server.URL, "token")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	runtime := &Runtime{Config: config.Resolved{URL: server.URL, Token: "token", Redact: true}, Client: client, Users: mattermost.NewUsers(client), Channels: mattermost.NewChannels(client)}
	channel, _, unavailable, err := resolvedReadChannel(context.Background(), runtime, "dm1", "user1")
	if err != nil || unavailable || channel.Type != "dm" || channel.Name != "@arda" || channel.MetadataStatus != "resolved" {
		t.Fatalf("channel=%+v unavailable=%v err=%v", channel, unavailable, err)
	}
}

func TestThreadMissingRootIsExplicitlyPartialInHumanAndMachineOutput(t *testing.T) {
	server := threadServer(t, time.Now().UnixMilli(), false)
	defer server.Close()
	stdout, stderr, code := executeChannel(t, server.URL, "thread", "reply")
	if code != 0 || !strings.Contains(stderr, "partially hydrated") || !strings.Contains(stdout, "reply") {
		t.Fatalf("human exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	stdout, stderr, code = executeChannel(t, server.URL, "--json", "thread", "reply")
	if code != 0 || !strings.Contains(stdout, `"root":null`) || !strings.Contains(stdout, `"unboundPosts":[{"id":"reply"`) || !strings.Contains(stdout, `"completeness":"unknown"`) || !strings.Contains(stderr, "partially hydrated") {
		t.Fatalf("machine exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	registry, err := mmSchema.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Validate("mm/v2/thread", strings.NewReader(stdout)); err != nil {
		t.Fatalf("rootless machine output did not validate: %v", err)
	}
}

func threadServer(t *testing.T, created int64, includeRoot bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v4/users/me":
			writeJSON(t, writer, `{"id":"user1","username":"arda"}`)
		case "/api/v4/posts/root/thread", "/api/v4/posts/reply/thread":
			root := fmt.Sprintf(`{"id":"root","channel_id":"channel1","user_id":"user1","message":"root text","create_at":%d,"delete_at":0,"root_id":"","reply_count":1}`, created)
			reply := fmt.Sprintf(`{"id":"reply","channel_id":"channel1","user_id":"user2","message":"reply text","create_at":%d,"delete_at":0,"root_id":"root","reply_count":0}`, created+1)
			if includeRoot {
				writeJSON(t, writer, `{"order":["root","reply"],"posts":{"root":`+root+`,"reply":`+reply+`},"has_next":false}`)
			} else {
				writeJSON(t, writer, `{"order":["reply"],"posts":{"reply":`+reply+`},"has_next":false}`)
			}
		case "/api/v4/channels/channel1":
			writeJSON(t, writer, `{"id":"channel1","team_id":"team1","type":"O","name":"town-square","display_name":"Town Square"}`)
		case "/api/v4/users/ids":
			writeJSON(t, writer, `[{"id":"user2","username":"bob"}]`)
		default:
			t.Errorf("unexpected request: %s %s", request.Method, request.URL.String())
			http.Error(writer, "unexpected", http.StatusNotFound)
		}
	}))
}
