package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ardasevinc/mattermost-cli/v2/internal/cursor"
	mmSchema "github.com/ardasevinc/mattermost-cli/v2/internal/schema"
)

func TestGroupDMsJSONDiscoversFocusedChannelsAppliesGlobalLimitAndSanitizesLabel(t *testing.T) {
	now := time.Now().UnixMilli()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v4/users/me":
			writeJSON(t, writer, `{"id":"self","username":"arda"}`)
		case "/api/v4/users/self/channels":
			writeJSON(t, writer, `[{"id":"ignored","team_id":"team","type":"P","name":"private","display_name":""},{"id":"g1","team_id":"","type":"G","name":"opaque1","display_name":"Crew\u001b[31m AKIA1234567890ABCDEF"},{"id":"g2","team_id":"","type":"G","name":"opaque2","display_name":"Second"}]`)
		case "/api/v4/channels/g1/posts":
			post := fmt.Sprintf(`{"id":"newer","channel_id":"g1","user_id":"alice","message":"new","create_at":%d,"delete_at":0,"root_id":"","reply_count":0}`, now)
			writeJSON(t, writer, `{"order":["newer"],"posts":{"newer":`+post+`},"has_next":false}`)
		case "/api/v4/channels/g2/posts":
			post := fmt.Sprintf(`{"id":"older","channel_id":"g2","user_id":"alice","message":"old","create_at":%d,"delete_at":0,"root_id":"","reply_count":0}`, now-1)
			writeJSON(t, writer, `{"order":["older"],"posts":{"older":`+post+`},"has_next":false}`)
		case "/api/v4/users/ids":
			writeJSON(t, writer, `[{"id":"alice","username":"alice"}]`)
		default:
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.String())
		}
	}))
	defer server.Close()
	stdout, stderr, code := executeChannel(t, server.URL, "--json", "--no-threads", "group-dms", "--limit", "1", "--since", "1d")
	if code != 0 || stderr != "" {
		t.Fatalf("exit=%d stderr=%q stdout=%q", code, stderr, stdout)
	}
	registry, err := mmSchema.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Validate("mm/v2/group-dms", strings.NewReader(stdout)); err != nil {
		t.Fatalf("machine output did not validate: %v\n%s", err, stdout)
	}
	var document struct {
		Schema   string `json:"schema"`
		Channels []struct {
			Channel  struct{ Name string } `json:"channel"`
			Messages []struct{ ID string } `json:"messages"`
		} `json:"channels"`
	}
	if err := json.Unmarshal([]byte(stdout), &document); err != nil {
		t.Fatal(err)
	}
	name := document.Channels[0].Channel.Name
	if document.Schema != "mm/v2/group-dms" || len(document.Channels) != 1 || !strings.Contains(name, "AK...EF") || strings.ContainsRune(name, '\x1b') || document.Channels[0].Messages[0].ID != "newer" {
		t.Fatalf("document=%+v", document)
	}
}

func TestGroupDMsExplicitChannelProvesMembershipBeforePosts(t *testing.T) {
	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v4/users/me":
			writeJSON(t, writer, `{"id":"self","username":"arda"}`)
		case "/api/v4/channels/group":
			writeJSON(t, writer, `{"id":"group","team_id":"","type":"G","name":"opaque","display_name":"Crew"}`)
		case "/api/v4/channels/group/members/self":
			writer.WriteHeader(http.StatusNotFound)
			writeJSON(t, writer, `{"message":"not a member"}`)
		case "/api/v4/channels/group/posts":
			posts.Add(1)
		default:
			t.Fatalf("unexpected request: %s", request.URL.String())
		}
	}))
	defer server.Close()
	_, _, code := executeChannel(t, server.URL, "group-dms", "--channel", "group")
	if code != 3 || posts.Load() != 0 {
		t.Fatalf("exit=%d posts=%d", code, posts.Load())
	}
}

func TestGroupDMsRejectsCursorFailuresBeforeNetwork(t *testing.T) {
	since := time.Now().Add(-time.Hour).UnixMilli()
	encoded, err := cursor.EncodeChannelHistory(cursor.ChannelHistory{Version: 1, Scope: "channel", ChannelID: "other", Boundary: cursor.Boundary{CreateAt: time.Now().UnixMilli(), ID: "post"}, Since: &since})
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"group-dms", "--channel", "group", "--cursor", "not-a-cursor"},
		{"group-dms", "--cursor", "not-a-cursor"},
		{"group-dms", "--channel", "group", "--cursor", encoded},
		{"group-dms", "--channel="},
		{"group-dms", "--cursor="},
	} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
			defer server.Close()
			_, _, code := executeChannel(t, server.URL, args...)
			if code != 2 || requests.Load() != 0 {
				t.Fatalf("args=%v exit=%d requests=%d", args, code, requests.Load())
			}
		})
	}
}

func TestGroupDMsUnknownEmptyFailsClosed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v4/users/me":
			writeJSON(t, writer, `{"id":"self","username":"arda"}`)
		case "/api/v4/users/self/channels":
			writeJSON(t, writer, `[{"id":"group","team_id":"","type":"G","name":"opaque","display_name":"Crew"}]`)
		case "/api/v4/channels/group/posts":
			writeJSON(t, writer, `{"order":[],"posts":{},"has_next":true}`)
		default:
			t.Fatalf("unexpected request: %s", request.URL.String())
		}
	}))
	defer server.Close()
	stdout, stderr, code := executeChannel(t, server.URL, "--json", "--no-threads", "group-dms")
	if code != 3 || stdout != "" || !strings.Contains(stderr, `"code":"read_failed"`) {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}
