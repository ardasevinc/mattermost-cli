//go:build e2e

package e2e_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

type liveHarness struct {
	t      *testing.T
	url    string
	token  string
	binary string
	home   string
	client *http.Client
}

type stageReceipt struct {
	Schema string `json:"schema"`
	Stage  struct {
		StageRef string `json:"stageRef"`
	} `json:"stage"`
}

type applyReceipt struct {
	Schema    string `json:"schema"`
	AttemptID string `json:"attemptId"`
	Outcome   string `json:"outcome"`
	Steps     []struct {
		Kind   string          `json:"kind"`
		Result json.RawMessage `json:"result"`
	} `json:"steps"`
}

type postPage struct {
	Order []string `json:"order"`
	Posts map[string]struct {
		Message string `json:"message"`
	} `json:"posts"`
}

func TestGoStageApplyPreservesShortMarkdownAndReplaysWithoutAnotherPost(t *testing.T) {
	h := newLiveHarness(t)
	self := h.user("me")
	alice := h.user("username/alice")
	channel := h.createChannel("direct", []string{self.ID, alice.ID})
	message := "# review\n\n- **bold**\n- `code`\n\n> final line\n"
	h.assertStageApplyExactlyOnce("dm", "alice", channel.ID, self.ID, message, "short-markdown")
}

func TestGoStageApplyPreservesMaximumLongMarkdownInGroup(t *testing.T) {
	h := newLiveHarness(t)
	users := []user{h.user("me"), h.user("username/alice"), h.user("username/bob")}
	ids := []string{users[0].ID, users[1].ID, users[2].ID}
	channel := h.createChannel("group", ids)
	prefix := "# long markdown\n\n"
	message := prefix + strings.Repeat("λ", 16_383-utf8.RuneCountInString(prefix))
	if utf8.RuneCountInString(message) != 16_383 {
		t.Fatalf("long fixture has %d runes", utf8.RuneCountInString(message))
	}
	h.assertStageApplyExactlyOnce("group", channel.ID, channel.ID, users[0].ID, message, "long-markdown")
}

func newLiveHarness(t *testing.T) *liveHarness {
	t.Helper()
	rawURL, token, binary := os.Getenv("MM_E2E_URL"), os.Getenv("MM_E2E_TOKEN"), os.Getenv("MM_E2E_BINARY")
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" || parsed.Port() == "" {
		t.Fatal("refusing to run Go mutation E2E without an explicit loopback Mattermost URL")
	}
	if token == "" || binary == "" || !filepath.IsAbs(binary) {
		t.Fatal("disposable Mattermost token and absolute Go binary path are required")
	}
	transport := &http.Transport{Proxy: nil}
	t.Cleanup(transport.CloseIdleConnections)
	return &liveHarness{
		t: t, url: strings.TrimRight(rawURL, "/"), token: token, binary: binary, home: t.TempDir(),
		client: &http.Client{Transport: transport, Timeout: 15 * time.Second},
	}
}

type user struct {
	ID string `json:"id"`
}

type channel struct {
	ID string `json:"id"`
}

func (h *liveHarness) user(selector string) user {
	h.t.Helper()
	var result user
	h.api(http.MethodGet, "/users/"+selector, nil, &result)
	if result.ID == "" {
		h.t.Fatal("Mattermost returned an empty user id")
	}
	return result
}

func (h *liveHarness) createChannel(kind string, userIDs []string) channel {
	h.t.Helper()
	body, err := json.Marshal(userIDs)
	if err != nil {
		h.t.Fatal(err)
	}
	var result channel
	h.api(http.MethodPost, "/channels/"+kind, body, &result)
	if result.ID == "" {
		h.t.Fatal("Mattermost returned an empty channel id")
	}
	return result
}

func (h *liveHarness) assertStageApplyExactlyOnce(kind, target, channelID, userID, message, requestPrefix string) {
	h.t.Helper()
	before := h.posts(channelID)
	stageRaw := h.cli(message, "--json", "stage", "send", kind, target, "--request-id", requestPrefix+"-stage")
	var staged stageReceipt
	if err := json.Unmarshal(stageRaw, &staged); err != nil || staged.Schema != "mm/v2/stage-receipt" || staged.Stage.StageRef == "" {
		h.t.Fatalf("invalid stage receipt: %v", err)
	}
	if afterStage := h.posts(channelID); len(afterStage.Order) != len(before.Order) {
		h.t.Fatalf("staging mutated Mattermost: before=%d after=%d", len(before.Order), len(afterStage.Order))
	}

	applyRaw := h.cli("", "--json", "apply", staged.Stage.StageRef, "--request-id", requestPrefix+"-apply")
	var applied applyReceipt
	if err := json.Unmarshal(applyRaw, &applied); err != nil || applied.Schema != "mm/v2/apply-receipt" || applied.Outcome != "succeeded" {
		h.t.Fatalf("invalid apply receipt: %v outcome=%q", err, applied.Outcome)
	}
	postID := createPostID(h.t, applied)
	var posted struct {
		ID        string `json:"id"`
		ChannelID string `json:"channel_id"`
		UserID    string `json:"user_id"`
		Message   string `json:"message"`
	}
	h.api(http.MethodGet, "/posts/"+postID, nil, &posted)
	if posted.ID != postID || posted.ChannelID != channelID || posted.UserID != userID || posted.Message != message {
		h.t.Fatalf("stored post mismatch: id=%q channel=%q user=%q bytes=%d", posted.ID, posted.ChannelID, posted.UserID, len(posted.Message))
	}
	afterApply := h.posts(channelID)
	if countMessage(afterApply, message) != 1 || len(afterApply.Order) != len(before.Order)+1 {
		h.t.Fatalf("apply count mismatch: before=%d after=%d matches=%d", len(before.Order), len(afterApply.Order), countMessage(afterApply, message))
	}

	replayRaw := h.cli("", "--json", "apply", staged.Stage.StageRef, "--request-id", requestPrefix+"-apply")
	var replay applyReceipt
	if err := json.Unmarshal(replayRaw, &replay); err != nil || replay.AttemptID != applied.AttemptID || replay.Outcome != "succeeded" {
		h.t.Fatalf("invalid apply replay: %v attempt=%q outcome=%q", err, replay.AttemptID, replay.Outcome)
	}
	afterReplay := h.posts(channelID)
	if countMessage(afterReplay, message) != 1 || len(afterReplay.Order) != len(afterApply.Order) {
		h.t.Fatalf("apply replay created another post: before=%d after=%d matches=%d", len(afterApply.Order), len(afterReplay.Order), countMessage(afterReplay, message))
	}
}

func createPostID(t *testing.T, receipt applyReceipt) string {
	t.Helper()
	for _, step := range receipt.Steps {
		if step.Kind != "create_post" {
			continue
		}
		var result struct {
			PostID string `json:"postId"`
		}
		if err := json.Unmarshal(step.Result, &result); err == nil && result.PostID != "" {
			return result.PostID
		}
	}
	t.Fatal("apply receipt has no validated create_post result")
	return ""
}

func (h *liveHarness) posts(channelID string) postPage {
	h.t.Helper()
	var result postPage
	h.api(http.MethodGet, "/channels/"+channelID+"/posts?per_page=200", nil, &result)
	if result.Order == nil || result.Posts == nil {
		h.t.Fatal("Mattermost returned an invalid post page")
	}
	return result
}

func countMessage(page postPage, message string) int {
	count := 0
	for _, id := range page.Order {
		if page.Posts[id].Message == message {
			count++
		}
	}
	return count
}

func (h *liveHarness) api(method, path string, body []byte, target any) {
	h.t.Helper()
	request, err := http.NewRequest(method, h.url+"/api/v4"+path, bytes.NewReader(body))
	if err != nil {
		h.t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+h.token)
	request.Header.Set("Content-Type", "application/json")
	response, err := h.client.Do(request)
	if err != nil {
		h.t.Fatalf("Mattermost E2E request failed: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, response.Body)
		h.t.Fatalf("Mattermost E2E request returned status %d", response.StatusCode)
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 4<<20))
	if err := decoder.Decode(target); err != nil {
		h.t.Fatalf("Mattermost E2E response decode failed: %v", err)
	}
}

func (h *liveHarness) cli(stdin string, args ...string) []byte {
	h.t.Helper()
	command := exec.Command(h.binary, args...)
	command.Stdin = strings.NewReader(stdin)
	command.Env = []string{
		"HOME=" + h.home,
		"XDG_CONFIG_HOME=" + filepath.Join(h.home, ".config"),
		"XDG_STATE_HOME=" + filepath.Join(h.home, ".local", "state"),
		"MM_URL=" + h.url,
		"MM_TOKEN=" + h.token,
		"PATH=" + os.Getenv("PATH"),
		"TMPDIR=" + h.home,
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
		"TZ=UTC",
		"NO_COLOR=1",
		"TERM=dumb",
	}
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		h.t.Fatalf("mm %s failed: %v, stderr=%s", strings.Join(args, " "), err, fmt.Sprintf("%q", stderr.String()))
	}
	if stderr.Len() != 0 {
		h.t.Fatalf("mm %s emitted stderr on success: %q", strings.Join(args, " "), stderr.String())
	}
	return stdout.Bytes()
}
