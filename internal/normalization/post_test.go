package normalization

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ardasevinc/mattermost-cli/v2/internal/mattermost"
	"github.com/ardasevinc/mattermost-cli/v2/internal/presentation"
)

const token = "ghp_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func basePost() mattermost.Post {
	return mattermost.Post{ID: "post-1", ChannelID: "channel", UserID: "author", Message: "latest visible text", CreateAt: 1000, UpdateAt: 3000, EditAt: 2000}
}

func normalizeOne(t *testing.T, post mattermost.Post, options presentation.Options) ([]byte, int) {
	t.Helper()
	users := map[string]mattermost.User{"author": {ID: "author", Username: "alice"}, "reactor": {ID: "reactor", Username: "bob"}}
	messages, redactions, err := NormalizePosts([]mattermost.Post{post}, users, "me", "https://mattermost.test", options)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(messages[0])
	if err != nil {
		t.Fatal(err)
	}
	return encoded, len(redactions)
}

func TestNormalizePosts(t *testing.T) {
	t.Run("latest visible edited state and long markdown exactness", func(t *testing.T) {
		post := basePost()
		post.Message = strings.Repeat("# heading\n\n- exact `markdown`\n", 600)
		messages, _, err := NormalizePosts([]mattermost.Post{post}, nil, "me", "https://mattermost.test", presentation.Options{})
		if err != nil {
			t.Fatal(err)
		}
		got := messages[0]
		if got.Text != post.Message || !got.UpdatedAt.Equal(time.UnixMilli(3000)) || got.EditedAt == nil || !got.EditedAt.Equal(time.UnixMilli(2000)) || got.IsDeleted {
			t.Fatalf("unexpected message: %#v", got)
		}
	})

	t.Run("deleted post never leaks stale nested data", func(t *testing.T) {
		post := basePost()
		post.DeleteAt = 4000
		post.Message = "stale " + token
		post.FileIDs = []string{"secret-file"}
		post.Files = []mattermost.PostFile{{ID: "secret-file", Name: "stale-" + token}}
		post.Attachments = []mattermost.PostAttachment{{Text: "stale " + token}}
		post.Reactions = []mattermost.PostReaction{{UserID: "reactor", EmojiName: "yes"}}
		encoded, _ := normalizeOne(t, post, presentation.Options{})
		got := string(encoded)
		if strings.Contains(got, token) || !strings.Contains(got, `"text":"[deleted post]"`) || !strings.Contains(got, `"files":[]`) || !strings.Contains(got, `"attachments":[]`) || !strings.Contains(got, `"reactions":[]`) {
			t.Fatalf("deleted output = %s", got)
		}
	})

	t.Run("author precedence", func(t *testing.T) {
		cases := []struct {
			name       string
			post       mattermost.Post
			myID, want string
		}{
			{"override", func() mattermost.Post { p := basePost(); p.OverrideUsername = "hook"; p.Type = "system_x"; return p }(), "author", "hook"},
			{"system", func() mattermost.Post { p := basePost(); p.Type = "system_x"; return p }(), "author", "system"},
			{"you", basePost(), "author", "you"}, {"username", basePost(), "me", "alice"},
			{"missing user id", func() mattermost.Post { p := basePost(); p.UserID = "missing"; return p }(), "me", "missing"},
		}
		users := map[string]mattermost.User{"author": {ID: "author", Username: "alice"}}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				messages, _, err := NormalizePosts([]mattermost.Post{tc.post}, users, tc.myID, "https://mattermost.test", presentation.Options{})
				if err != nil {
					t.Fatal(err)
				}
				if messages[0].User != tc.want {
					t.Fatalf("user = %q, want %q", messages[0].User, tc.want)
				}
			})
		}
	})

	t.Run("file id and metadata dedupe with nested metadata", func(t *testing.T) {
		size := int64(12)
		post := basePost()
		post.FileIDs = []string{"a", "a"}
		post.Files = []mattermost.PostFile{{ID: "a", Name: "old"}, {ID: "a", Name: "new", MIMEType: "text/plain", Size: &size}, {ID: "b", Extension: "txt"}}
		messages, _, err := NormalizePosts([]mattermost.Post{post}, nil, "", "https://mattermost.test", presentation.Options{})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(messages[0].Files, []string{"a", "b"}) || len(messages[0].FileDetails) != 2 || messages[0].FileDetails[0].Name != "new" || messages[0].FileDetails[0].Size == nil || *messages[0].FileDetails[0].Size != 12 {
			t.Fatalf("files = %#v / %#v", messages[0].Files, messages[0].FileDetails)
		}
	})

	t.Run("rich attachments preserve numeric-derived strings", func(t *testing.T) {
		short := true
		post := basePost()
		post.Attachments = []mattermost.PostAttachment{{Pretext: "before", TitleLink: "https://x.test/a\n", Fields: []mattermost.PostAttachmentField{{Title: "7", Value: "9", Short: &short}}, Timestamp: "123"}}
		messages, _, err := NormalizePosts([]mattermost.Post{post}, nil, "", "https://mattermost.test", presentation.Options{})
		if err != nil {
			t.Fatal(err)
		}
		got := messages[0].Attachments[0]
		if got.Pretext != "before" || got.TitleLink != "https://x.test/a\\n" || got.Timestamp != "123" || len(got.Fields) != 1 || got.Fields[0].Title != "7" || got.Fields[0].Short == nil || !*got.Fields[0].Short {
			t.Fatalf("attachment = %#v", got)
		}
	})

	t.Run("reactions group by raw emoji and sort actors by raw id", func(t *testing.T) {
		first := "ghp_a" + strings.Repeat("x", 35)
		second := "ghp_b" + strings.Repeat("x", 35)
		post := basePost()
		post.Reactions = []mattermost.PostReaction{{UserID: "reactor", EmojiName: second}, {UserID: "missing", EmojiName: first}, {UserID: "reactor", EmojiName: first}}
		messages, _, err := NormalizePosts([]mattermost.Post{post}, map[string]mattermost.User{"reactor": {Username: "bob"}}, "", "https://mattermost.test", presentation.Options{})
		if err != nil {
			t.Fatal(err)
		}
		got := messages[0].Reactions
		if len(got) != 2 || got[0].Count != 2 || got[1].Count != 1 || got[0].Actors[0].ID != "missing" || got[0].Actors[1].Username != "bob" || got[0].Emoji != got[1].Emoji {
			t.Fatalf("reactions = %#v", got)
		}
	})

	t.Run("sanitizes all emitted strings and records provenance without originals", func(t *testing.T) {
		post := basePost()
		post.ID = token
		post.RootID = "root\x1b" + token
		post.UserID = "user\x1b" + token
		post.OverrideUsername = "hook\x1b" + token
		post.FileIDs = []string{"file\x1b" + token}
		post.Attachments = []mattermost.PostAttachment{{Text: "body " + token}}
		post.Reactions = []mattermost.PostReaction{{UserID: "reactor", EmojiName: "yes\x1b" + token}}
		messages, redactions, err := NormalizePosts([]mattermost.Post{post}, nil, "", "https://mattermost.test", presentation.Options{})
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(struct {
			Messages   any
			Redactions any
		}{messages, redactions})
		text := string(encoded)
		if strings.Contains(text, token) || strings.ContainsRune(text, '\x1b') || strings.Contains(strings.ToLower(text), "original") {
			t.Fatalf("unsafe output = %s", text)
		}
		fields := map[string]bool{}
		for _, r := range redactions {
			fields[r.Field] = true
		}
		for _, field := range []string{"post.id", "post.userId", "post.rootId", "file.id", "post.permalink", "attachment.0.text", "reaction.emoji"} {
			if !fields[field] {
				t.Errorf("missing field %q in %#v", field, fields)
			}
		}
		if messages[0].CanonicalID != post.ID || messages[0].CanonicalRootID != post.RootID {
			t.Fatal("canonical raw identities were not retained")
		}
	})

	t.Run("label redaction positions use final UTF-16 coordinates", func(t *testing.T) {
		credential := "mm-active-secret"
		cases := []struct {
			name, value, mask string
			options           presentation.Options
		}{
			{"heuristic secret", token, "ghp_...aaaa", presentation.Options{}},
			{"exact credential", credential, "[REDACTED:mattermost_credential]", presentation.Options{DisableHeuristics: true, Credentials: []string{credential}}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				post := basePost()
				post.OverrideUsername = "😀\n\t" + tc.value
				messages, redactions, err := NormalizePosts([]mattermost.Post{post}, nil, "", "https://mattermost.test", tc.options)
				if err != nil {
					t.Fatal(err)
				}
				if got, want := messages[0].User, "😀\\n\\t"+tc.mask; got != want {
					t.Fatalf("user = %q, want %q", got, want)
				}
				for _, redaction := range redactions {
					if redaction.Field != "user" {
						continue
					}
					if redaction.Position != 6 {
						t.Fatalf("position = %d, want 6 in %q", redaction.Position, messages[0].User)
					}
					return
				}
				t.Fatal("missing user redaction")
			})
		}
	})

	t.Run("multiple label redactions track expansions and astral characters", func(t *testing.T) {
		credential := "mm-active-secret"
		post := basePost()
		post.OverrideUsername = "😀\n" + credential + "\t😀\n" + credential
		messages, redactions, err := NormalizePosts([]mattermost.Post{post}, nil, "", "https://mattermost.test", presentation.Options{DisableHeuristics: true, Credentials: []string{credential}})
		if err != nil {
			t.Fatal(err)
		}
		positions := make([]int, 0, 2)
		for _, redaction := range redactions {
			if redaction.Field == "user" {
				positions = append(positions, redaction.Position)
			}
		}
		mask := "[REDACTED:mattermost_credential]"
		firstByte := strings.Index(messages[0].User, mask)
		secondRelative := strings.Index(messages[0].User[firstByte+len(mask):], mask)
		secondByte := firstByte + len(mask) + secondRelative
		want := []int{utf16Length(messages[0].User[:firstByte]), utf16Length(messages[0].User[:secondByte])}
		if !reflect.DeepEqual(positions, want) {
			t.Fatalf("positions = %v, want %v in %q", positions, want, messages[0].User)
		}
	})

	t.Run("many label redactions remain exact at practical size", func(t *testing.T) {
		credential := "mm-active-secret"
		const count = 1000
		post := basePost()
		post.OverrideUsername = strings.Repeat("\n"+credential, count)
		messages, redactions, err := NormalizePosts([]mattermost.Post{post}, nil, "", "https://mattermost.test", presentation.Options{DisableHeuristics: true, Credentials: []string{credential}})
		if err != nil {
			t.Fatal(err)
		}
		userRedactions := 0
		lastPosition := -1
		for _, redaction := range redactions {
			if redaction.Field != "user" {
				continue
			}
			userRedactions++
			if redaction.Position <= lastPosition {
				t.Fatalf("positions not strictly increasing at %d: %d <= %d", userRedactions, redaction.Position, lastPosition)
			}
			lastPosition = redaction.Position
		}
		if userRedactions != count || strings.Count(messages[0].User, "\\n") != count || strings.Contains(messages[0].User, credential) {
			t.Fatalf("redactions=%d newlines=%d", userRedactions, strings.Count(messages[0].User, "\\n"))
		}
	})

	t.Run("permalink redaction position addresses mask in final canonical URL", func(t *testing.T) {
		post := basePost()
		post.ID = token
		messages, redactions, err := NormalizePosts([]mattermost.Post{post}, nil, "", "https://mattermost.test", presentation.Options{})
		if err != nil {
			t.Fatal(err)
		}
		permalink := messages[0].Permalink
		maskAt := strings.Index(permalink, "ghp_...aaaa")
		if maskAt < 0 {
			t.Fatalf("permalink has no mask: %q", permalink)
		}
		want := utf16Length(permalink[:maskAt])
		for _, redaction := range redactions {
			if redaction.Field == "post.permalink" {
				if redaction.Position != want {
					t.Fatalf("position = %d, want %d in %q", redaction.Position, want, permalink)
				}
				return
			}
		}
		t.Fatal("missing permalink redaction")
	})

	t.Run("disabled heuristics still masks active credential", func(t *testing.T) {
		credential := "mm-active-secret"
		post := basePost()
		post.Message = token + " " + credential + "\x1b"
		encoded, count := normalizeOne(t, post, presentation.Options{DisableHeuristics: true, Credentials: []string{credential}})
		got := string(encoded)
		if !strings.Contains(got, token) || strings.Contains(got, credential) || strings.ContainsRune(got, '\x1b') || count != 1 {
			t.Fatalf("output=%s redactions=%d", got, count)
		}
	})

	t.Run("reply count is nil at zero", func(t *testing.T) {
		post := basePost()
		messages, _, err := NormalizePosts([]mattermost.Post{post}, nil, "", "https://mattermost.test", presentation.Options{})
		if err != nil {
			t.Fatal(err)
		}
		if messages[0].ReplyCount != nil {
			t.Fatalf("reply count = %#v", messages[0].ReplyCount)
		}
		post.ReplyCount = 3
		messages, _, err = NormalizePosts([]mattermost.Post{post}, nil, "", "https://mattermost.test", presentation.Options{})
		if err != nil {
			t.Fatal(err)
		}
		if messages[0].ReplyCount == nil || *messages[0].ReplyCount != 3 {
			t.Fatalf("reply count = %#v", messages[0].ReplyCount)
		}
	})

	t.Run("does not mutate posts or users", func(t *testing.T) {
		size := int64(4)
		post := basePost()
		post.Files = []mattermost.PostFile{{ID: "a", Size: &size}}
		post.Reactions = []mattermost.PostReaction{{UserID: "z", EmojiName: "b"}, {UserID: "a", EmojiName: "b"}}
		users := map[string]mattermost.User{"z": {Username: "zed"}}
		beforePost := post
		beforePost.Files = append([]mattermost.PostFile(nil), post.Files...)
		beforePost.Reactions = append([]mattermost.PostReaction(nil), post.Reactions...)
		beforeUsers := map[string]mattermost.User{"z": users["z"]}
		_, _, err := NormalizePosts([]mattermost.Post{post}, users, "", "https://mattermost.test", presentation.Options{})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(post, beforePost) || !reflect.DeepEqual(users, beforeUsers) {
			t.Fatalf("inputs mutated: %#v %#v", post, users)
		}
	})
}

func TestPostUserIDs(t *testing.T) {
	posts := []mattermost.Post{{UserID: "author", Reactions: []mattermost.PostReaction{{UserID: "reactor"}, {UserID: "reactor"}}}, {UserID: "", Reactions: []mattermost.PostReaction{{UserID: "other"}}}}
	if got, want := PostUserIDs(posts), []string{"author", "reactor", "other"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("PostUserIDs()=%v, want %v", got, want)
	}
}

func TestRemapLabelRedactionPositionsHandlesSharedBoundaries(t *testing.T) {
	redactions := []presentation.Redaction{{Position: 0}, {Position: 0}, {Position: 3}, {Position: 4}}
	remapLabelRedactionPositions("😀\nx", redactions)
	want := []int{0, 0, 4, 5}
	for index, redaction := range redactions {
		if redaction.Position != want[index] {
			t.Fatalf("position[%d] = %d, want %d", index, redaction.Position, want[index])
		}
	}
}
