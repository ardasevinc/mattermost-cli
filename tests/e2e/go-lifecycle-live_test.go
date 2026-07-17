//go:build e2e

package e2e_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ardasevinc/mattermost-cli/internal/stageinput"
)

func TestGoApplyFullPostLifecycleWithAttachment(t *testing.T) {
	h := newLiveHarness(t)
	self := h.user("me")
	var team struct {
		ID string `json:"id"`
	}
	h.api(http.MethodGet, "/teams/name/e2e", nil, &team)
	if team.ID == "" {
		t.Fatal("fixture team has no id")
	}
	channelBody, _ := json.Marshal(map[string]string{
		"team_id": team.ID, "name": "go-lifecycle", "display_name": "Go Lifecycle", "type": "O",
	})
	var target channel
	h.api(http.MethodPost, "/channels", channelBody, &target)
	if target.ID == "" {
		t.Fatal("fixture channel has no id")
	}

	attachment := []byte("mattermost-cli Go v2 attachment\n\x00exact bytes\n")
	attachmentDir, err := os.MkdirTemp(".", ".mm-e2e-attachment-")
	if err != nil {
		t.Fatal(err)
	}
	attachmentDir, err = filepath.Abs(attachmentDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(attachmentDir) })
	attachmentPath := filepath.Join(attachmentDir, "proof.bin")
	if err := os.WriteFile(attachmentPath, attachment, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := stageinput.Bind(t.Context(), []stageinput.Attachment{{Path: attachmentPath}}, [][]byte{[]byte(h.token)}); err != nil {
		t.Fatalf("attachment fixture is not safely bindable: %v", err)
	}
	message := "# compound post\n\n- attachment\n- **Markdown**\n"
	created := h.stageApply(t, message, "lifecycle-create",
		[]string{"send", "channel", target.ID, "--attachment", attachmentPath},
		map[string]int{"POST /api/v4/files": 1, "POST /api/v4/posts": 1})
	postID := createPostID(t, created)
	fileID := uploadFileID(t, created)
	post := h.post(postID)
	if post.ChannelID != target.ID || post.UserID != self.ID || post.Message != message || !slices.Equal(post.FileIDs, []string{fileID}) {
		t.Fatalf("created post mismatch: %+v", post)
	}
	if downloaded := h.apiBytes(http.MethodGet, "/files/"+fileID); !bytes.Equal(downloaded, attachment) {
		t.Fatalf("downloaded attachment differs: got %d bytes, want %d", len(downloaded), len(attachment))
	}

	replyMessage := "> reply\n\nwith `code`\n"
	replied := h.stageApply(t, replyMessage, "lifecycle-reply", []string{"reply", postID}, map[string]int{"POST /api/v4/posts": 1})
	replyID := createPostID(t, replied)
	reply := h.post(replyID)
	if reply.ChannelID != target.ID || reply.UserID != self.ID || reply.RootID != postID || reply.Message != replyMessage {
		t.Fatalf("reply mismatch: %+v", reply)
	}

	editedMessage := "# edited\n\nattachment remains\n"
	h.stageApply(t, editedMessage, "lifecycle-edit", []string{"post-edit", postID}, map[string]int{"PUT /api/v4/posts/" + postID + "/patch": 1})
	edited := h.post(postID)
	if edited.Message != editedMessage || !slices.Equal(edited.FileIDs, []string{fileID}) {
		t.Fatalf("edit did not preserve exact content and files: %+v", edited)
	}

	h.stageApply(t, "", "lifecycle-react", []string{"react", postID, "eyes"}, map[string]int{"POST /api/v4/reactions": 1})
	if !h.hasReaction(postID, self.ID, "eyes") {
		t.Fatal("reaction was not present after apply")
	}
	h.stageApply(t, "", "lifecycle-unreact", []string{"unreact", postID, "eyes"}, map[string]int{"DELETE /api/v4/users/" + self.ID + "/posts/" + postID + "/reactions/eyes": 1})
	if h.hasReaction(postID, self.ID, "eyes") {
		t.Fatal("reaction remained after unreact apply")
	}

	h.stageApply(t, "", "lifecycle-delete", []string{"post-delete", replyID}, map[string]int{"DELETE /api/v4/posts/" + replyID: 1})
	if !h.postDeleted(replyID) {
		t.Fatal("reply remained live after delete apply")
	}
}

type livePost struct {
	ID        string   `json:"id"`
	ChannelID string   `json:"channel_id"`
	UserID    string   `json:"user_id"`
	RootID    string   `json:"root_id"`
	Message   string   `json:"message"`
	FileIDs   []string `json:"file_ids"`
	DeleteAt  int64    `json:"delete_at"`
}

func (h *liveHarness) stageApply(t *testing.T, stdin, requestPrefix string, stageArgs []string, expectedWrites map[string]int) applyReceipt {
	t.Helper()
	args := append([]string{"--json", "stage"}, stageArgs...)
	args = append(args, "--request-id", requestPrefix+"-stage")
	writesBeforeStage := h.mutationSnapshot()
	var staged stageReceipt
	decodeCLI(t, h.cli(stdin, args...), &staged)
	h.assertMutationDelta(writesBeforeStage, nil)
	if staged.Schema != "mm/v2/stage-receipt" || staged.Stage.StageRef == "" {
		t.Fatalf("invalid stage receipt: %+v", staged)
	}
	writesBeforeApply := h.mutationSnapshot()
	var applied applyReceipt
	decodeCLI(t, h.cli("", "--json", "apply", staged.Stage.StageRef, "--request-id", requestPrefix+"-apply"), &applied)
	h.assertMutationDelta(writesBeforeApply, expectedWrites)
	if applied.Schema != "mm/v2/apply-receipt" || applied.Outcome != "succeeded" {
		t.Fatalf("invalid apply receipt: %+v", applied)
	}
	return applied
}

func uploadFileID(t *testing.T, receipt applyReceipt) string {
	t.Helper()
	for _, step := range receipt.Steps {
		if step.Kind != "upload_attachment" {
			continue
		}
		var result struct {
			FileID string `json:"fileId"`
		}
		if err := json.Unmarshal(step.Result, &result); err == nil && result.FileID != "" {
			return result.FileID
		}
	}
	t.Fatal("apply receipt has no validated upload result")
	return ""
}

func (h *liveHarness) post(postID string) livePost {
	h.t.Helper()
	var result livePost
	h.api(http.MethodGet, "/posts/"+postID, nil, &result)
	if result.ID != postID || result.DeleteAt != 0 {
		h.t.Fatalf("invalid live post: %+v", result)
	}
	return result
}

func (h *liveHarness) apiBytes(method, path string) []byte {
	h.t.Helper()
	request, err := http.NewRequest(method, h.url+"/api/v4"+path, nil)
	if err != nil {
		h.t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+h.token)
	response, err := h.client.Do(request)
	if err != nil {
		h.t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		h.t.Fatalf("Mattermost byte response returned status %d", response.StatusCode)
	}
	result, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		h.t.Fatal(err)
	}
	return result
}

func (h *liveHarness) hasReaction(postID, userID, emoji string) bool {
	h.t.Helper()
	var reactions []struct {
		UserID    string `json:"user_id"`
		PostID    string `json:"post_id"`
		EmojiName string `json:"emoji_name"`
	}
	h.api(http.MethodGet, "/posts/"+postID+"/reactions", nil, &reactions)
	for _, reaction := range reactions {
		if reaction.UserID == userID && reaction.PostID == postID && reaction.EmojiName == emoji {
			return true
		}
	}
	return false
}

func (h *liveHarness) postDeleted(postID string) bool {
	h.t.Helper()
	request, err := http.NewRequest(http.MethodGet, h.url+"/api/v4/posts/"+postID, nil)
	if err != nil {
		h.t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+h.token)
	response, err := h.client.Do(request)
	if err != nil {
		h.t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return true
	}
	if response.StatusCode != http.StatusOK {
		h.t.Fatalf("deleted-post probe returned status %d", response.StatusCode)
	}
	var result livePost
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result); err != nil {
		h.t.Fatal(err)
	}
	return result.ID == postID && result.DeleteAt > 0
}
