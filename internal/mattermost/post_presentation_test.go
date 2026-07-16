package mattermost

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestPostRetainsValidatedEmbeddedPresentationData(t *testing.T) {
	var post Post
	payload := `{
		"id":"post_1","channel_id":"channel_1","user_id":"user raw","message":"latest","create_at":1000,
		"update_at":3000,"edit_at":2000,"delete_at":0,"root_id":"","reply_count":0,"type":"system_webhook",
		"is_pinned":true,"override_username":"direct hook","file_ids":["file raw",7,"file-2",""],
		"props":{"override_username":"props hook","ignored":{"secret":"no"},"attachments":[null,7,{},
			{"pretext":"before","title":"title","title_link":"https://example.test","text":"body","fields":[null,7,{"title":7,"value":9,"short":false},{"short":true}],"footer":"foot","footer_icon":"fi","author_name":"author","author_link":"al","author_icon":"ai","color":"#fff","image_url":"image","thumb_url":"thumb","ts":123}]},
		"metadata":{"files":[null,7,{"id":"file raw","name":"name","mime_type":"text/plain","size":12,"extension":"txt"},{"id":7},{"id":"file-2","size":-1}],
		"reactions":[null,7,{"user_id":"u2","post_id":"post_1","emoji_name":"eyes","create_at":2},{"user_id":"u1","post_id":"post_1","emoji_name":"eyes","create_at":1},{"user_id":7,"emoji_name":"bad"}]}}
	`
	if err := json.Unmarshal([]byte(payload), &post); err != nil {
		t.Fatal(err)
	}
	if post.ID != "post_1" || post.ChannelID != "channel_1" || post.UserID != "user raw" || post.Message != "latest" || post.CreateAt != 1000 || post.UpdateAt != 3000 || post.EditAt != 2000 || post.Type != "system_webhook" || !post.IsPinned || post.OverrideUsername != "direct hook" {
		t.Fatalf("post scalars = %#v", post)
	}
	if !reflect.DeepEqual(post.FileIDs, []string{"file raw", "file-2"}) {
		t.Fatalf("file ids = %#v", post.FileIDs)
	}
	if len(post.Files) != 2 || post.Files[0].ID != "file raw" || post.Files[0].Size == nil || *post.Files[0].Size != 12 || post.Files[1].ID != "file-2" || post.Files[1].Size != nil {
		t.Fatalf("files = %#v", post.Files)
	}
	if len(post.Attachments) != 1 || post.Attachments[0].Timestamp != "123" || !reflect.DeepEqual(post.Attachments[0].Fields, []PostAttachmentField{{Title: "7", Value: "9", Short: boolPostPointer(false)}, {Short: boolPostPointer(true)}}) {
		t.Fatalf("attachments = %#v", post.Attachments)
	}
	wantReactions := []PostReaction{{UserID: "u2", PostID: "post_1", EmojiName: "eyes", CreateAt: 2}, {UserID: "u1", PostID: "post_1", EmojiName: "eyes", CreateAt: 1}}
	if !reflect.DeepEqual(post.Reactions, wantReactions) {
		t.Fatalf("reactions = %#v", post.Reactions)
	}
}

func TestPostOptionalPresentationMetadataFailsLocally(t *testing.T) {
	var post Post
	payload := `{"id":"post","channel_id":"channel","user_id":7,"message":"ok","create_at":10,"update_at":"bad","edit_at":8640000000000001,"delete_at":0,"type":7,"is_pinned":"true","override_username":7,"file_ids":"bad","props":{"override_username":"props hook","attachments":"bad"},"metadata":{"files":"bad","reactions":[{"user_id":"u","emoji_name":"eyes"},{"user_id":"bad","emoji_name":7}]}}`
	if err := json.Unmarshal([]byte(payload), &post); err != nil {
		t.Fatal(err)
	}
	if post.UserID != "" || post.UpdateAt != post.CreateAt || post.EditAt != 0 || post.Type != "" || post.IsPinned || post.OverrideUsername != "props hook" || len(post.FileIDs) != 0 || len(post.Files) != 0 || len(post.Attachments) != 0 || !reflect.DeepEqual(post.Reactions, []PostReaction{{UserID: "u", EmojiName: "eyes"}}) {
		t.Fatalf("post = %#v", post)
	}
}

func TestDeletedPostSuppressesEveryStalePresentationField(t *testing.T) {
	var post Post
	secret := "ghp_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	payload := `{"id":"gone","channel_id":"channel","user_id":"author","message":{"stale":"` + secret + `"},"create_at":10,"update_at":11,"edit_at":12,"delete_at":13,"type":"` + secret + `","is_pinned":true,"override_username":"` + secret + `","file_ids":["` + secret + `"],"props":{"attachments":[{"text":"` + secret + `"}]},"metadata":{"files":[{"id":"secret-file","name":"` + secret + `"}],"reactions":[{"user_id":"reactor","emoji_name":"` + secret + `"}]}}`
	if err := json.Unmarshal([]byte(payload), &post); err != nil {
		t.Fatal(err)
	}
	if post.Message != "" || post.DeleteAt != 13 || post.Type != "" || post.IsPinned || post.OverrideUsername != "" || len(post.FileIDs) != 0 || len(post.Files) != 0 || len(post.Attachments) != 0 || len(post.Reactions) != 0 {
		t.Fatalf("deleted post retained stale data: %#v", post)
	}
	if encoded, err := json.Marshal(post); err != nil || bytes.Contains(encoded, []byte(secret)) {
		t.Fatalf("deleted post encoded stale secret: %s (error %v)", encoded, err)
	}

	var page OrderedPostsPage
	if err := json.Unmarshal([]byte(`{"order":["gone"],"posts":{"gone":`+payload+`}}`), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Posts) != 0 || page.RawCount != 1 || page.Continuation == nil || page.Continuation.PostID != "gone" {
		t.Fatalf("page policy changed: %#v", page)
	}
}

func TestPostNullPresentationScalarsAreOmitted(t *testing.T) {
	var post Post
	payload := `{"id":"post","channel_id":"channel","message":"ok","create_at":10,"update_at":null,"edit_at":null,"delete_at":0,"props":{"attachments":[{"fields":[{"title":null,"value":null,"short":null}],"ts":null}]},"metadata":{"files":[{"id":"file","size":null}]}}`
	if err := json.Unmarshal([]byte(payload), &post); err != nil {
		t.Fatal(err)
	}
	if post.UpdateAt != 10 || post.EditAt != 0 || len(post.Files) != 1 || post.Files[0].Size != nil || len(post.Attachments) != 0 {
		t.Fatalf("post = %#v", post)
	}
}

func TestPostOverrideUsernameUsesNullishNotEmptyFallback(t *testing.T) {
	for _, tt := range []struct {
		name     string
		direct   string
		expected string
	}{
		{"missing", "", "props"},
		{"null", `,"override_username":null`, "props"},
		{"empty", `,"override_username":""`, ""},
		{"direct", `,"override_username":"direct"`, "direct"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var post Post
			payload := `{"id":"post","channel_id":"channel","message":"ok","create_at":10,"delete_at":0` + tt.direct + `,"props":{"override_username":"props"}}`
			if err := json.Unmarshal([]byte(payload), &post); err != nil {
				t.Fatal(err)
			}
			if post.OverrideUsername != tt.expected {
				t.Fatalf("override = %q, want %q", post.OverrideUsername, tt.expected)
			}
		})
	}
}

func TestECMAScriptNumberString(t *testing.T) {
	for _, tt := range []struct {
		input float64
		want  string
	}{
		{1e-6, "0.000001"},
		{1e20, "100000000000000000000"},
		{1e-7, "1e-7"},
		{-0.0, "0"},
		{1e21, "1e+21"},
		{-1e-7, "-1e-7"},
	} {
		if got := ecmaScriptNumberString(tt.input); got != tt.want {
			t.Fatalf("ecmaScriptNumberString(%v) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestPostStillRejectsMalformedRequiredIdentityAndTimestamps(t *testing.T) {
	for _, payload := range []string{
		`{"id":7,"channel_id":"channel","message":"x","create_at":1,"delete_at":0}`,
		`{"id":"post","channel_id":7,"message":"x","create_at":1,"delete_at":0}`,
		`{"id":"post","channel_id":"channel","message":"x","create_at":"1","delete_at":0}`,
		`{"id":"post","channel_id":"channel","message":"x","create_at":1,"delete_at":"0"}`,
	} {
		var post Post
		if !errors.Is(json.Unmarshal([]byte(payload), &post), ErrInvalidPostResponse) {
			t.Fatalf("payload accepted: %s", payload)
		}
	}
}

func boolPostPointer(value bool) *bool { return &value }
