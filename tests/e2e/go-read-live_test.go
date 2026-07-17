//go:build e2e

package e2e_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/ardasevinc/mattermost-cli/internal/output"
	"github.com/ardasevinc/mattermost-cli/internal/schema"
)

func TestGoRealServerChannelCursorSearchAndThreadReads(t *testing.T) {
	h := newLiveHarness(t)
	self := h.user("me")
	var team struct {
		ID string `json:"id"`
	}
	h.api(http.MethodGet, "/teams/name/e2e", nil, &team)
	channelBody, _ := json.Marshal(map[string]string{
		"team_id": team.ID, "name": "go-read-acceptance", "display_name": "Go Read Acceptance", "type": "O",
	})
	var target channel
	h.api(http.MethodPost, "/channels", channelBody, &target)
	baseline := h.posts(target.ID)
	expected := make(map[string]struct{ text, rootID, userID string }, len(baseline.Order)+4)
	for _, postID := range baseline.Order {
		post := h.post(postID)
		expected[post.ID] = struct{ text, rootID, userID string }{post.Message, post.RootID, post.UserID}
	}

	first := h.createLivePost(target.ID, "", "read acceptance first")
	second := h.createLivePost(target.ID, "", "read acceptance second")
	root := h.createLivePost(target.ID, "", "needle-cursor-thread-root")
	reply := h.createLivePost(target.ID, root.ID, "> exact reply\n\nwith `code`\n")
	expected[first.ID] = struct{ text, rootID, userID string }{"read acceptance first", "", self.ID}
	expected[second.ID] = struct{ text, rootID, userID string }{"read acceptance second", "", self.ID}
	expected[root.ID] = struct{ text, rootID, userID string }{"needle-cursor-thread-root", "", self.ID}
	expected[reply.ID] = struct{ text, rootID, userID string }{"> exact reply\n\nwith `code`\n", root.ID, self.ID}

	firstPageRaw := h.cli("", "--json", "--no-threads", "channel", "go-read-acceptance", "--team", "e2e", "--limit", "2")
	validateLiveDocument(t, "mm/v2/channel", firstPageRaw)
	var firstPage output.ChannelEnvelope
	decodeCLI(t, firstPageRaw, &firstPage)
	if firstPage.Schema != "mm/v2/channel" || firstPage.Data.Channel.ID != target.ID || firstPage.Data.Metadata.Completeness != output.MachineUnknown || firstPage.Data.Metadata.Selection.QueryTruncated != nil || firstPage.Data.Metadata.Selection.NextCursor == nil || len(firstPage.Data.Messages) != 2 {
		t.Fatalf("invalid first channel page: %+v", firstPage)
	}

	pages := []output.ChannelEnvelope{firstPage}
	cursor := *firstPage.Data.Metadata.Selection.NextCursor
	for len(pages) < 5 {
		pageRaw := h.cli("", "--json", "--no-threads", "channel", "go-read-acceptance", "--team", "e2e", "--limit", "2", "--cursor", cursor)
		validateLiveDocument(t, "mm/v2/channel", pageRaw)
		var page output.ChannelEnvelope
		decodeCLI(t, pageRaw, &page)
		if page.Data.Metadata.Selection.InputCursor == nil || *page.Data.Metadata.Selection.InputCursor != cursor || page.Data.Channel.ID != target.ID {
			t.Fatalf("invalid resumed channel page: %+v", page)
		}
		pages = append(pages, page)
		if page.Data.Metadata.Selection.NextCursor == nil {
			break
		}
		if *page.Data.Metadata.Selection.NextCursor == cursor {
			t.Fatal("live channel cursor did not advance")
		}
		cursor = *page.Data.Metadata.Selection.NextCursor
	}
	lastPage := pages[len(pages)-1]
	if lastPage.Data.Metadata.Completeness != output.MachineComplete || lastPage.Data.Metadata.Selection.NextCursor != nil {
		t.Fatalf("cursor pagination did not reach proven completion: %+v", lastPage.Data.Metadata)
	}
	seen := make(map[string]bool)
	for _, page := range pages {
		assertLiveReadChannel(t, page.Data.Channel, target.ID)
		if page.Data.Metadata.Selection.SelectedCount != len(page.Data.Messages) || page.Data.Metadata.VisiblePostCount != len(page.Data.Messages) || page.Data.Metadata.VisibleThreads.Status != "not_requested" {
			t.Fatalf("channel page metadata does not bind emitted messages: %+v", page.Data.Metadata)
		}
		for _, message := range page.Data.Messages {
			if seen[message.ID] {
				t.Fatalf("cursor pages duplicated post %q", message.ID)
			}
			want, ok := expected[message.ID]
			if !ok || len(message.Replies) != 0 {
				t.Fatalf("cursor pages emitted an unexpected or nested post: %+v", message)
			}
			assertLiveReadMessage(t, message, message.ID, want.text, want.rootID, want.userID)
			seen[message.ID] = true
		}
	}
	if len(seen) != len(expected) {
		t.Fatalf("cursor pages did not emit the exact fixture set: seen=%v expected=%v", seen, expected)
	}

	threadRaw := h.cli("", "--json", "thread", reply.ID)
	validateLiveDocument(t, "mm/v2/thread", threadRaw)
	var thread output.ThreadEnvelope
	decodeCLI(t, threadRaw, &thread)
	if thread.Data.Root == nil {
		t.Fatalf("live thread omitted its root: %+v", thread)
	}
	assertLiveReadChannel(t, thread.Data.Channel, target.ID)
	assertLiveReadMessage(t, *thread.Data.Root, root.ID, "needle-cursor-thread-root", "", self.ID)
	if len(thread.Data.Root.Replies) != 1 {
		t.Fatalf("live thread has the wrong reply shape: %+v", thread.Data.Root.Replies)
	}
	assertLiveReadMessage(t, thread.Data.Root.Replies[0], reply.ID, "> exact reply\n\nwith `code`\n", root.ID, self.ID)
	if len(thread.Data.UnboundPosts) != 0 || thread.Data.Metadata.Completeness != output.MachineComplete || thread.Data.Metadata.Selection.SelectedCount != 2 || thread.Data.Metadata.VisiblePostCount != 2 || thread.Data.Metadata.VisibleThreads.Status != "complete" || thread.Data.Metadata.VisibleThreads.HydratedRootCount != 1 || len(thread.Data.Metadata.VisibleThreads.FailedRootIDs) != 0 {
		t.Fatalf("invalid live thread: %+v", thread)
	}

	searchRaw := h.cli("", "--json", "search", "needle-cursor-thread-root", "--team", "e2e", "--limit", "5")
	validateLiveDocument(t, "mm/v2/search", searchRaw)
	var search output.SearchEnvelope
	decodeCLI(t, searchRaw, &search)
	if len(search.Results) != 1 {
		t.Fatalf("invalid live search result: %+v", search)
	}
	result := search.Results[0]
	assertLiveReadChannel(t, result.Channel, target.ID)
	if len(result.Messages) != 1 || len(result.Messages[0].Replies) != 1 {
		t.Fatalf("search did not return exactly one hydrated thread: %+v", result.Messages)
	}
	assertLiveReadMessage(t, result.Messages[0], root.ID, "needle-cursor-thread-root", "", self.ID)
	assertLiveReadMessage(t, result.Messages[0].Replies[0], reply.ID, "> exact reply\n\nwith `code`\n", root.ID, self.ID)
	if result.Metadata.Completeness != output.MachineComplete || result.Metadata.Selection.SelectedCount != 1 || result.Metadata.VisiblePostCount != 2 || result.Metadata.VisibleThreads.Status != "complete" || result.Metadata.VisibleThreads.HydratedRootCount != 1 || len(result.Metadata.VisibleThreads.FailedRootIDs) != 0 {
		t.Fatalf("search metadata does not prove exact complete hydration: %+v", result.Metadata)
	}
}

func (h *liveHarness) createLivePost(channelID, rootID, message string) livePost {
	h.t.Helper()
	body := map[string]string{"channel_id": channelID, "message": message}
	if rootID != "" {
		body["root_id"] = rootID
	}
	raw, err := json.Marshal(body)
	if err != nil {
		h.t.Fatal(err)
	}
	var post livePost
	h.api(http.MethodPost, "/posts", raw, &post)
	if post.ID == "" || post.ChannelID != channelID || post.RootID != rootID || post.Message != message {
		h.t.Fatalf("invalid created fixture post: %+v", post)
	}
	return post
}

func validateLiveDocument(t *testing.T, schemaID string, raw []byte) {
	t.Helper()
	registry, err := schema.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err = registry.Validate(schemaID, bytes.NewReader(raw)); err != nil {
		t.Fatalf("%s output failed schema validation: %v", schemaID, err)
	}
}

func assertLiveReadChannel(t *testing.T, channel output.MachineChannel, id string) {
	t.Helper()
	if channel.ID != id || channel.Type != "public" || channel.Name != "go-read-acceptance" || channel.DisplayName != "Go Read Acceptance" || channel.MetadataStatus != "resolved" {
		t.Fatalf("live read channel identity mismatch: %+v", channel)
	}
}

func assertLiveReadMessage(t *testing.T, message output.MachineMessage, id, text, rootID, userID string) {
	t.Helper()
	rootMatches := rootID == "" && message.RootID == nil || rootID != "" && message.RootID != nil && *message.RootID == rootID
	if message.ID != id || message.Text != text || message.UserID != userID || !rootMatches {
		t.Fatalf("live read message mismatch: %+v", message)
	}
}
