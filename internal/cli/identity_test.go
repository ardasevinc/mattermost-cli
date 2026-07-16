package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ardasevinc/mattermost-cli/internal/output"
	"github.com/ardasevinc/mattermost-cli/internal/schema"
)

func TestIdentityCommandsEmitStrictMachineSchemasAndHumanSemantics(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v4/users/me":
			writeJSON(t, w, `{"id":"u","username":"arda","first_name":"Arda","last_name":"Sevinc","roles":"system_user"}`)
		case "/api/v4/users/u/teams":
			writeJSON(t, w, `[{"id":"t","name":"core","display_name":"Core Team","type":"O"}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	registry, err := schema.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"whoami", "teams"} {
		stdout, stderr, code := executeChannel(t, server.URL, "--json", command)
		if code != 0 || stderr != "" {
			t.Fatalf("%s exit=%d stderr=%q", command, code, stderr)
		}
		if err := registry.Validate("mm/v2/"+command, strings.NewReader(stdout)); err != nil {
			t.Fatalf("%s schema: %v", command, err)
		}
	}
	stdout, stderr, code := executeChannel(t, server.URL, "whoami")
	if code != 0 || stderr != "" || stdout != "@arda (Arda Sevinc) [u]\nRoles: system_user\n" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	stdout, stderr, code = executeChannel(t, server.URL, "teams")
	if code != 0 || stderr != "" || stdout != "core (Core Team) [t] open\n" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestUsersValidatesLimitBeforeNetworkAndProvesTriState(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/api/v4/users/search" {
			t.Fatalf("unexpected %s", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["term"] != "dev" || body["limit"] != float64(3) {
			t.Fatalf("body=%v", body)
		}
		writeJSON(t, w, `[{"id":"b","username":"z"},{"id":"a","username":"a"},{"id":"c","username":"more"}]`)
	}))
	defer server.Close()
	_, _, code := executeChannel(t, server.URL, "users", "--limit", "0")
	if code != 2 || requests.Load() != 0 {
		t.Fatalf("exit=%d requests=%d", code, requests.Load())
	}
	stdout, stderr, code := executeChannel(t, server.URL, "--json", "users", "  dev  ", "--limit", "2")
	if code != 0 || stderr != "" {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, `"query":"dev"`) || !strings.Contains(stdout, `"truncated":true`) || strings.Index(stdout, `"username":"a"`) > strings.Index(stdout, `"username":"z"`) {
		t.Fatalf("stdout=%s", stdout)
	}
}

func TestChannelsFiltersBeforeHydrationAndBatchesDMPeers(t *testing.T) {
	var teamCalls, userBatchCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v4/users/me":
			writeJSON(t, w, `{"id":"user","username":"me"}`)
		case "/api/v4/users/user/channels":
			writeJSON(t, w, `[{"type":"O"},{"id":"d","team_id":"","type":"D","name":"user__peer","display_name":"","last_post_at":7,"total_msg_count":2}]`)
		case "/api/v4/users/ids":
			userBatchCalls.Add(1)
			writeJSON(t, w, `[{"id":"peer","username":"bob"}]`)
		case "/api/v4/users/user/teams":
			teamCalls.Add(1)
			t.Fatal("D-only filter must not read teams")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	stdout, stderr, code := executeChannel(t, server.URL, "--json", "channels", "--type", "dm")
	if code != 0 || stderr != "" {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if teamCalls.Load() != 0 || userBatchCalls.Load() != 1 || !strings.Contains(stdout, `"name":"@bob"`) {
		t.Fatalf("teams=%d users=%d stdout=%s", teamCalls.Load(), userBatchCalls.Load(), stdout)
	}
}

func TestChannelsGroupFilterUsesNoTeamOrUserHydrationAndEmptyIsStrict(t *testing.T) {
	var unrelated atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v4/users/me":
			writeJSON(t, w, `{"id":"user","username":"me"}`)
		case "/api/v4/users/user/channels":
			writeJSON(t, w, `[]`)
		default:
			unrelated.Add(1)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	stdout, stderr, code := executeChannel(t, server.URL, "--json", "channels", "--type", "group")
	if code != 0 || stderr != "" || unrelated.Load() != 0 || stdout != `{"schema":"mm/v2/channels","channels":[]}`+"\n" {
		t.Fatalf("exit=%d unrelated=%d stdout=%q stderr=%q", code, unrelated.Load(), stdout, stderr)
	}
}

func TestChannelsPublicFilterHydratesOnlyCompleteTeams(t *testing.T) {
	var teamCalls, userCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v4/users/me":
			writeJSON(t, w, `{"id":"user","username":"me"}`)
		case "/api/v4/users/user/channels":
			writeJSON(t, w, `[{"id":"d","team_id":"","type":"D","name":"user__peer","display_name":""},{"id":"c","team_id":"t","type":"O","name":"general","display_name":"General"}]`)
		case "/api/v4/users/user/teams":
			teamCalls.Add(1)
			writeJSON(t, w, `[{"id":"t","name":"core","display_name":"Core","type":"O"}]`)
		case "/api/v4/users/ids":
			userCalls.Add(1)
			t.Fatal("public filter must not hydrate D peers")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	stdout, stderr, code := executeChannel(t, server.URL, "--json", "channels", "--type", "public")
	if code != 0 || stderr != "" || teamCalls.Load() != 1 || userCalls.Load() != 0 || !strings.Contains(stdout, `"team":{"id":"t","name":"core"`) {
		t.Fatalf("exit=%d teams=%d users=%d stdout=%s stderr=%q", code, teamCalls.Load(), userCalls.Load(), stdout, stderr)
	}
}

func TestIdentityPresentationMasksCredentialsAndFailureKeepsStdoutEmpty(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v4/users/me" {
			writeJSON(t, w, `{"id":"u","username":"test-token\u001b[2J"}`)
			return
		}
		http.Error(w, "remote secret", 500)
	}))
	defer server.Close()
	stdout, stderr, code := executeChannel(t, server.URL, "--json", "whoami")
	if code != 0 || stderr != "" || strings.Contains(stdout, "test-token") || strings.ContainsRune(stdout, '\x1b') || !strings.Contains(stdout, "[REDACTED:mattermost_credential]") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	stdout, stderr, code = executeChannel(t, server.URL, "--json", "teams")
	if code != 3 || stdout != "" || !strings.Contains(stderr, `"code":"read_failed"`) {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestRemoteBindingFailuresAreReadFailedMachineErrors(t *testing.T) {
	registry, err := schema.Load()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, command string
		handler       http.HandlerFunc
	}{
		{"hostile identity", "whoami", func(w http.ResponseWriter, r *http.Request) { writeJSON(t, w, `{"id":"u","username":"\u202e"}`) }},
		{"duplicate teams", "teams", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/v4/users/me" {
				writeJSON(t, w, `{"id":"u","username":"a"}`)
				return
			}
			writeJSON(t, w, `[{"id":"t","name":"a","type":"O"},{"id":"t","name":"b","type":"O"}]`)
		}},
		{"oversized identity", "whoami", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(t, w, `{"id":"u","username":"`+strings.Repeat("x", output.MaxMachineDocumentBytes)+`"}`)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(test.handler)
			defer server.Close()
			stdout, stderr, code := executeChannel(t, server.URL, "--json", test.command)
			if code != 3 || stdout != "" || !strings.Contains(stderr, `"code":"read_failed"`) {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			if err := registry.Validate("mm/v2/error", strings.NewReader(stderr)); err != nil {
				t.Fatalf("error schema: %v", err)
			}
		})
	}
}
