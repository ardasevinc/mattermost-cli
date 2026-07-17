//go:build e2e

package e2e_test

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type liveWatchEvent struct {
	Schema   string `json:"schema"`
	Type     string `json:"type"`
	Sequence struct {
		ConnectionID string `json:"connectionId"`
		Number       int64  `json:"number"`
	} `json:"sequence"`
	PostID      string            `json:"postId"`
	ChannelID   string            `json:"channelId"`
	ChannelName string            `json:"channelName"`
	SenderID    string            `json:"senderId"`
	Sender      string            `json:"sender"`
	Message     string            `json:"message"`
	Timestamp   string            `json:"timestamp"`
	RootID      *string           `json:"rootId"`
	FileIDs     []string          `json:"fileIds"`
	Redactions  []json.RawMessage `json:"redactions"`
}

func TestGoRealServerWatchEmitsExactPostedEventAndStopsCleanly(t *testing.T) {
	h := newLiveHarness(t)
	var self struct {
		ID       string `json:"id"`
		Username string `json:"username"`
	}
	h.api(http.MethodGet, "/users/me", nil, &self)
	var team struct {
		ID string `json:"id"`
	}
	h.api(http.MethodGet, "/teams/name/e2e", nil, &team)
	channelBody, _ := json.Marshal(map[string]string{
		"team_id": team.ID, "name": "go-watch-acceptance", "display_name": "Go Watch Acceptance", "type": "O",
	})
	var target channel
	h.api(http.MethodPost, "/channels", channelBody, &target)

	writesBefore := h.mutationSnapshot()
	readyMarker := filepath.Join(h.home, "watch-ready")
	command, _, stderr := h.cliCommandWithEnv("", []string{"MM_E2E_WATCH_READY_MARKER=" + readyMarker}, "--json", "watch", "go-watch-acceptance", "--team", "e2e")
	command.Stdout = nil
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	finished := false
	t.Cleanup(func() {
		if !finished && command.Process != nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	})

	deadline := time.Now().Add(5 * time.Second)
	for {
		if info, statErr := os.Stat(readyMarker); statErr == nil {
			if info.Mode().Perm() != 0o600 || info.Size() != 0 {
				t.Fatalf("invalid watch readiness marker: mode=%o size=%d", info.Mode().Perm(), info.Size())
			}
			break
		} else if !os.IsNotExist(statErr) {
			t.Fatal(statErr)
		}
		if time.Now().After(deadline) {
			t.Fatal("watch did not authenticate and receive Mattermost hello")
		}
		time.Sleep(10 * time.Millisecond)
	}

	message := "# live watch\n\n- **exact** markdown\n- `code`\n"
	created := h.createLivePost(target.ID, "", message)
	lineResult := make(chan []byte, 1)
	lineError := make(chan error, 1)
	reader := bufio.NewReader(stdout)
	go func() {
		line, readErr := reader.ReadBytes('\n')
		if readErr != nil {
			lineError <- readErr
			return
		}
		lineResult <- line
	}()

	var line []byte
	select {
	case line = <-lineResult:
	case readErr := <-lineError:
		t.Fatalf("watch event read failed: %v", readErr)
	case <-time.After(5 * time.Second):
		t.Fatal("watch did not emit the live post")
	}
	validateLiveDocument(t, "mm/v2/watch-event", line)
	var event liveWatchEvent
	if err = json.Unmarshal(line, &event); err != nil {
		t.Fatal(err)
	}
	expectedTimestamp := time.UnixMilli(created.CreateAt).UTC().Format("2006-01-02T15:04:05.000Z")
	if event.Schema != "mm/v2/watch-event" || event.Type != "posted" || event.Sequence.ConnectionID == "" || event.Sequence.Number < 0 || event.PostID != created.ID || event.ChannelID != target.ID || event.ChannelName != "go-watch-acceptance" || event.SenderID != self.ID || event.Sender != self.Username || event.Message != message || event.Timestamp != expectedTimestamp || event.RootID != nil || len(event.FileIDs) != 0 || len(event.Redactions) != 0 {
		t.Fatalf("live watch event mismatch: %+v", event)
	}

	if err = command.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	type drainResult struct {
		remaining []byte
		err       error
	}
	drained := make(chan drainResult, 1)
	go func() {
		remaining, readErr := io.ReadAll(reader)
		drained <- drainResult{remaining, readErr}
	}()
	var remaining []byte
	select {
	case result := <-drained:
		remaining, err = result.remaining, result.err
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		_ = command.Process.Kill()
		_ = command.Wait()
		finished = true
		t.Fatal("watch did not close stdout within 5s after SIGINT")
	}
	waited := make(chan error, 1)
	go func() { waited <- command.Wait() }()
	select {
	case err = <-waited:
		finished = true
		if err != nil {
			t.Fatalf("watch did not stop cleanly after SIGINT: %v, stderr=%q", err, stderr.Bytes())
		}
	case <-time.After(5 * time.Second):
		_ = command.Process.Kill()
		err = <-waited
		finished = true
		t.Fatalf("watch process did not exit within 5s after SIGINT: %v", err)
	}
	if len(remaining) != 0 || stderr.Len() != 0 {
		t.Fatalf("watch emitted unexpected trailing output: stdout=%q stderr=%q", remaining, stderr.Bytes())
	}
	h.assertMutationDelta(writesBefore, nil)
}
