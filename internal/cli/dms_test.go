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

	mmSchema "github.com/ardasevinc/mattermost-cli/internal/schema"
)

func TestDMsJSONDiscoversAccountWideAndAppliesGlobalLimit(t *testing.T) {
	now := time.Now().UnixMilli()
	var userBatches atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v4/users/me":
			writeJSON(t, writer, `{"id":"self","username":"arda"}`)
		case "/api/v4/users/self/channels":
			writeJSON(t, writer, `[{"id":"dm-alice","team_id":"","type":"D","name":"alice__self","display_name":""},{"id":"dm-bob","team_id":"","type":"D","name":"bob__self","display_name":""}]`)
		case "/api/v4/channels/dm-alice/posts":
			post := fmt.Sprintf(`{"id":"alice-new","channel_id":"dm-alice","user_id":"alice","message":"alice","create_at":%d,"delete_at":0,"root_id":"","reply_count":0}`, now)
			writeJSON(t, writer, `{"order":["alice-new"],"posts":{"alice-new":`+post+`},"has_next":false}`)
		case "/api/v4/channels/dm-bob/posts":
			post := fmt.Sprintf(`{"id":"bob-new","channel_id":"dm-bob","user_id":"bob","message":"bob","create_at":%d,"delete_at":0,"root_id":"","reply_count":0}`, now-1)
			writeJSON(t, writer, `{"order":["bob-new"],"posts":{"bob-new":`+post+`},"has_next":false}`)
		case "/api/v4/users/ids":
			userBatches.Add(1)
			writeJSON(t, writer, `[{"id":"alice","username":"alice"},{"id":"bob","username":"bob"}]`)
		default:
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.String())
		}
	}))
	defer server.Close()

	stdout, stderr, code := executeChannel(t, server.URL, "--json", "--no-threads", "dms", "--limit", "2", "--since", "1d")
	if code != 0 || stderr != "" || userBatches.Load() != 1 {
		t.Fatalf("exit=%d stderr=%q user batches=%d", code, stderr, userBatches.Load())
	}
	registry, err := mmSchema.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Validate("mm/v2/dms", strings.NewReader(stdout)); err != nil {
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
	if document.Schema != "mm/v2/dms" || len(document.Channels) != 2 || document.Channels[0].Channel.Name != "@alice" || document.Channels[1].Channel.Name != "@bob" {
		t.Fatalf("unexpected document: %+v", document)
	}
}

func TestDMsRejectsInvalidCursorBeforeNetwork(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	_, stderr, code := executeChannel(t, server.URL, "dms", "--channel", "dm", "--cursor", "not-a-cursor")
	if code != 2 || !strings.Contains(stderr, "invalid direct-message cursor") || requests.Load() != 0 {
		t.Fatalf("exit=%d stderr=%q requests=%d", code, stderr, requests.Load())
	}
}

func TestDMsRejectsExplicitEmptyTargetFlagsBeforeRuntime(t *testing.T) {
	for _, args := range [][]string{{"dms", "--user="}, {"dms", "--channel="}, {"dms", "--cursor="}} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
			defer server.Close()
			_, stderr, code := executeChannel(t, server.URL, args...)
			if code != 2 || requests.Load() != 0 || !strings.Contains(stderr, "cannot be empty") {
				t.Fatalf("exit=%d stderr=%q requests=%d", code, stderr, requests.Load())
			}
		})
	}
}

func TestDMsHydratesOnlyOutputPartnersAndReactionActors(t *testing.T) {
	now := time.Now().UnixMilli()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v4/users/me":
			writeJSON(t, writer, `{"id":"self","username":"arda"}`)
		case "/api/v4/users/self/channels":
			writeJSON(t, writer, `[{"id":"dm-alice","team_id":"","type":"D","name":"alice__self","display_name":""},{"id":"dm-bob","team_id":"","type":"D","name":"bob__self","display_name":""}]`)
		case "/api/v4/channels/dm-alice/posts":
			post := fmt.Sprintf(`{"id":"alice-new","channel_id":"dm-alice","user_id":"alice","message":"alice","create_at":%d,"delete_at":0,"root_id":"","reply_count":0,"metadata":{"reactions":[{"user_id":"reactor","post_id":"alice-new","emoji_name":"eyes","create_at":1}]}}`, now)
			writeJSON(t, writer, `{"order":["alice-new"],"posts":{"alice-new":`+post+`},"has_next":false}`)
		case "/api/v4/channels/dm-bob/posts":
			post := fmt.Sprintf(`{"id":"bob-old","channel_id":"dm-bob","user_id":"bob","message":"bob","create_at":%d,"delete_at":0,"root_id":"","reply_count":0}`, now-1)
			writeJSON(t, writer, `{"order":["bob-old"],"posts":{"bob-old":`+post+`},"has_next":false}`)
		case "/api/v4/users/ids":
			var ids []string
			if err := json.NewDecoder(request.Body).Decode(&ids); err != nil {
				t.Fatal(err)
			}
			if strings.Join(ids, ",") != "alice,reactor" {
				t.Fatalf("hydrated IDs = %v", ids)
			}
			writeJSON(t, writer, `[{"id":"alice","username":"alice"},{"id":"reactor","username":"carol"}]`)
		default:
			t.Fatalf("unexpected request: %s", request.URL.String())
		}
	}))
	defer server.Close()
	stdout, stderr, code := executeChannel(t, server.URL, "--json", "--no-threads", "dms", "--limit", "1", "--since", "1d")
	if code != 0 || stderr != "" || !strings.Contains(stdout, `"username":"carol"`) || strings.Contains(stdout, "@bob") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestDMsConfirmedEmptyDoesNotHydratePartners(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v4/users/me":
			writeJSON(t, writer, `{"id":"self","username":"arda"}`)
		case "/api/v4/users/self/channels":
			writeJSON(t, writer, `[{"id":"dm","team_id":"","type":"D","name":"alice__self","display_name":""}]`)
		case "/api/v4/channels/dm/posts":
			writeJSON(t, writer, `{"order":[],"posts":{},"has_next":false}`)
		default:
			t.Fatalf("unexpected request: %s", request.URL.String())
		}
	}))
	defer server.Close()
	stdout, stderr, code := executeChannel(t, server.URL, "--json", "--no-threads", "dms")
	if code != 0 || stderr != "" || stdout != "{\"schema\":\"mm/v2/dms\",\"channels\":[]}\n" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestDMsUnknownEmptyFailsClosed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v4/users/me":
			writeJSON(t, writer, `{"id":"self","username":"arda"}`)
		case "/api/v4/users/self/channels":
			writeJSON(t, writer, `[{"id":"dm","team_id":"","type":"D","name":"alice__self","display_name":""}]`)
		case "/api/v4/channels/dm/posts":
			writeJSON(t, writer, `{"order":[],"posts":{},"has_next":true}`)
		default:
			t.Fatalf("unexpected request: %s", request.URL.String())
		}
	}))
	defer server.Close()
	stdout, stderr, code := executeChannel(t, server.URL, "--json", "--no-threads", "dms")
	if code != 3 || stdout != "" || !strings.Contains(stderr, `"code":"read_failed"`) {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}
