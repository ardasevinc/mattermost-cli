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

	mmSchema "github.com/ardasevinc/mattermost-cli/v2/internal/schema"
)

func TestSearchJSONRunsSelectedTeamReadPipeline(t *testing.T) {
	postTime := time.Now().Add(-time.Hour).UnixMilli()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v4/users/me":
			writeJSON(t, writer, `{"id":"user1","username":"arda"}`)
		case "/api/v4/users/user1/teams":
			writeJSON(t, writer, `[{"id":"team1","name":"main","display_name":"Main","type":"O"}]`)
		case "/api/v4/teams/team1/posts/search":
			if request.Method != http.MethodPost {
				t.Fatalf("search method = %s", request.Method)
			}
			var body struct {
				Terms   string `json:"terms"`
				Page    int    `json:"page"`
				PerPage int    `json:"per_page"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.Terms != "needle" || body.Page != 0 || body.PerPage != 2 {
				t.Fatalf("search body = %+v", body)
			}
			post := fmt.Sprintf(`{"id":"post1","channel_id":"channel1","user_id":"user1","message":"needle and test-token","create_at":%d,"update_at":%d,"delete_at":0,"root_id":"","reply_count":0}`, postTime, postTime)
			writeJSON(t, writer, `{"order":["post1"],"posts":{"post1":`+post+`},"matches":{"post1":["needle"]},"has_next":false}`)
		case "/api/v4/users/ids":
			writeJSON(t, writer, `[{"id":"user1","username":"arda"}]`)
		case "/api/v4/channels/channel1":
			writeJSON(t, writer, `{"id":"channel1","team_id":"team1","type":"O","name":"town-square","display_name":"Town Square"}`)
		default:
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.String())
		}
	}))
	defer server.Close()

	stdout, stderr, code := executeChannel(t, server.URL, "--json", "--no-threads", "search", "  needle  ", "--team", "main", "--limit", "1")
	if code != 0 || stderr != "" {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	registry, err := mmSchema.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Validate("mm/v2/search", strings.NewReader(stdout)); err != nil {
		t.Fatalf("machine output did not validate: %v\n%s", err, stdout)
	}
	var document struct {
		Schema  string `json:"schema"`
		Results []struct {
			Messages []struct{ Text, User string } `json:"messages"`
			Metadata struct {
				Completeness string `json:"completeness"`
				Selection    struct {
					Source, Query string
					SelectedCount int `json:"selectedCount"`
				} `json:"selection"`
			} `json:"metadata"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(stdout), &document); err != nil {
		t.Fatal(err)
	}
	if document.Schema != "mm/v2/search" || len(document.Results) != 1 || len(document.Results[0].Messages) != 1 || document.Results[0].Messages[0].User != "you" || strings.Contains(document.Results[0].Messages[0].Text, "test-token") || document.Results[0].Metadata.Completeness != "complete" || document.Results[0].Metadata.Selection.Source != "search" || document.Results[0].Metadata.Selection.SelectedCount != 1 {
		t.Fatalf("unexpected document: %+v", document)
	}
}

func TestSearchRejectsBlankQueryBeforeNetwork(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	_, stderr, code := executeChannel(t, server.URL, "search", " \t ")
	if code != 2 || !strings.Contains(stderr, "search query cannot be empty") || requests.Load() != 0 {
		t.Fatalf("exit=%d stderr=%q requests=%d", code, stderr, requests.Load())
	}
}

func TestSearchCompleteEmptyEmitsEmptyEnvelopeAndUnknownEmptyFailsClosed(t *testing.T) {
	for _, test := range []struct {
		name, response string
		wantCode       int
	}{
		{name: "complete", response: `{"order":[],"posts":{},"has_next":false}`, wantCode: 0},
		{name: "unknown", response: `{"order":[],"posts":{},"has_next":true}`, wantCode: 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := searchEmptyServer(t, test.response)
			defer server.Close()
			stdout, stderr, code := executeChannel(t, server.URL, "--json", "--no-threads", "search", "needle")
			if code != test.wantCode {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			if test.wantCode == 0 && !strings.Contains(stdout, `"schema":"mm/v2/search"`) || test.wantCode != 0 && !strings.Contains(stderr, `"code":"read_failed"`) {
				t.Fatalf("stdout=%q stderr=%q", stdout, stderr)
			}
		})
	}
}

func TestSearchDoesNotBindForeignTeamChannelMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v4/users/me":
			writeJSON(t, writer, `{"id":"user1","username":"arda"}`)
		case "/api/v4/users/user1/teams":
			writeJSON(t, writer, `[{"id":"team1","name":"main","display_name":"Main","type":"O"}]`)
		case "/api/v4/teams/team1/posts/search":
			writeJSON(t, writer, `{"order":["post1"],"posts":{"post1":{"id":"post1","channel_id":"channel1","user_id":"user1","message":"needle","create_at":1,"update_at":1,"delete_at":0,"root_id":"","reply_count":0}},"has_next":false}`)
		case "/api/v4/users/ids":
			writeJSON(t, writer, `[{"id":"user1","username":"arda"}]`)
		case "/api/v4/channels/channel1":
			writeJSON(t, writer, `{"id":"channel1","team_id":"team2","type":"O","name":"foreign","display_name":"Foreign"}`)
		default:
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.String())
		}
	}))
	defer server.Close()
	stdout, stderr, code := executeChannel(t, server.URL, "--json", "--no-threads", "search", "needle", "--team", "main")
	if code != 0 || !strings.Contains(stderr, "channel metadata is unavailable") || !strings.Contains(stdout, `"type":"unknown"`) || strings.Contains(stdout, "Foreign") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestSearchBatchesUserHydrationAcrossChannels(t *testing.T) {
	var userBatches atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v4/users/me":
			writeJSON(t, writer, `{"id":"user1","username":"arda"}`)
		case "/api/v4/users/user1/teams":
			writeJSON(t, writer, `[{"id":"team1","name":"main","display_name":"Main","type":"O"}]`)
		case "/api/v4/teams/team1/posts/search":
			writeJSON(t, writer, `{"order":["post2","post1"],"posts":{"post1":{"id":"post1","channel_id":"channel1","user_id":"user2","message":"one","create_at":1,"update_at":1,"delete_at":0,"root_id":"","reply_count":0},"post2":{"id":"post2","channel_id":"channel2","user_id":"user3","message":"two","create_at":2,"update_at":2,"delete_at":0,"root_id":"","reply_count":0}},"has_next":false}`)
		case "/api/v4/users/ids":
			userBatches.Add(1)
			writeJSON(t, writer, `[{"id":"user2","username":"two"},{"id":"user3","username":"three"}]`)
		case "/api/v4/channels/channel1":
			writeJSON(t, writer, `{"id":"channel1","team_id":"team1","type":"O","name":"one","display_name":"One"}`)
		case "/api/v4/channels/channel2":
			writeJSON(t, writer, `{"id":"channel2","team_id":"team1","type":"O","name":"two","display_name":"Two"}`)
		default:
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.String())
		}
	}))
	defer server.Close()
	_, stderr, code := executeChannel(t, server.URL, "--json", "--no-threads", "search", "needle", "--team", "main")
	if code != 0 || stderr != "" || userBatches.Load() != 1 {
		t.Fatalf("exit=%d stderr=%q user batches=%d", code, stderr, userBatches.Load())
	}
}

func searchEmptyServer(t *testing.T, response string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v4/users/me":
			writeJSON(t, writer, `{"id":"user1","username":"arda"}`)
		case "/api/v4/users/user1/teams":
			writeJSON(t, writer, `[{"id":"team1","name":"main","display_name":"Main","type":"O"}]`)
		case "/api/v4/teams/team1/posts/search":
			writeJSON(t, writer, response)
		default:
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.String())
		}
	}))
}
