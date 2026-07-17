package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	mmSchema "github.com/ardasevinc/mattermost-cli/v2/internal/schema"
)

func TestMentionsJSONUsesAliasesScopesGlobalSelectionAndHydration(t *testing.T) {
	now := time.Now()
	var searches, userBatches atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v4/users/me":
			writeJSON(t, writer, `{"id":"user1","username":"arda"}`)
		case "/api/v4/users/user1/teams":
			writeJSON(t, writer, `[{"id":"team1","name":"main","display_name":"Main","type":"O"}]`)
		case "/api/v4/teams/team1/channels/name/town-square":
			writeJSON(t, writer, `{"id":"channel1","team_id":"team1","type":"O","name":"town-square","display_name":"Town Square"}`)
		case "/api/v4/teams/team1/posts/search":
			var body struct {
				Terms string `json:"terms"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(body.Terms, "after:") || !strings.HasSuffix(body.Terms, " in:town-square") {
				t.Fatalf("scoped terms = %q", body.Terms)
			}
			searches.Add(1)
			post := func(id, channel, user, message string, created int64) string {
				return fmt.Sprintf(`{"id":%q,"channel_id":%q,"user_id":%q,"message":%q,"create_at":%d,"update_at":%d,"delete_at":0,"root_id":"","reply_count":0}`, id, channel, user, message, created, created)
			}
			switch {
			case strings.HasPrefix(body.Terms, "@arda "):
				writeJSON(t, writer, `{"order":["foreign","shared"],"posts":{"foreign":`+post("foreign", "channel2", "user2", "@arda exact but wrong channel", now.UnixMilli()+3)+`,"shared":`+post("shared", "channel1", "user2", "@arda exact", now.UnixMilli())+`},"has_next":false}`)
			case strings.HasPrefix(body.Terms, `"Arda Sevinc" `):
				writeJSON(t, writer, `{"order":["new","shared"],"posts":{"new":`+post("new", "channel1", "user3", "hello Arda Sevinc!", now.UnixMilli()+2)+`,"shared":`+post("shared", "channel1", "user2", "Arda Sevinc and @arda", now.UnixMilli())+`},"has_next":false}`)
			default:
				t.Fatalf("unexpected search terms %q", body.Terms)
			}
		case "/api/v4/users/ids":
			userBatches.Add(1)
			writeJSON(t, writer, `[{"id":"user2","username":"two"},{"id":"user3","username":"three"}]`)
		case "/api/v4/channels/channel1":
			writeJSON(t, writer, `{"id":"channel1","team_id":"team1","type":"O","name":"town-square","display_name":"Town Square"}`)
		default:
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.String())
		}
	}))
	defer server.Close()

	configRoot := t.TempDir()
	configDir := filepath.Join(configRoot, "mattermost-cli")
	if err := os.Mkdir(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(`mention_names = ["Arda Sevinc", "Arda Sevinc", ""]`), 0o600); err != nil {
		t.Fatal(err)
	}
	setTestHome(t, t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	t.Setenv("MM_URL", server.URL)
	t.Setenv("MM_TOKEN", "test-token")
	stdout, stderr, code := executeWithCurrentEnvironment(t, "--json", "--no-threads", "mentions", "--team", "main", "--limit", "2", "--since", "24h", "--channel", "#town-square")
	if code != 0 || stderr != "" || searches.Load() != 2 || userBatches.Load() != 1 {
		t.Fatalf("exit=%d stderr=%q searches=%d user batches=%d", code, stderr, searches.Load(), userBatches.Load())
	}
	registry, err := mmSchema.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Validate("mm/v2/mentions", strings.NewReader(stdout)); err != nil {
		t.Fatalf("machine output did not validate: %v\n%s", err, stdout)
	}
	if !strings.Contains(stdout, `"source":"mentions"`) || !strings.Contains(stdout, `"selectedCount":2`) || !strings.Contains(stdout, `"requestedLimit":2`) || !strings.Contains(stdout, `"completeness":"complete"`) || strings.Contains(stdout, "wrong channel") {
		t.Fatalf("unexpected output: %s", stdout)
	}
}

func TestMentionsIncompleteEmptyFailsClosed(t *testing.T) {
	server := searchEmptyServer(t, `{"order":[],"posts":{},"has_next":true}`)
	defer server.Close()
	stdout, stderr, code := executeChannel(t, server.URL, "--json", "mentions")
	if code != 3 || stdout != "" || !strings.Contains(stderr, `"code":"read_failed"`) {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func executeWithCurrentEnvironment(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	var stdout, stderr strings.Builder
	code := Execute(t.Context(), args, strings.NewReader(""), &stdout, &stderr)
	return stdout.String(), stderr.String(), code
}
