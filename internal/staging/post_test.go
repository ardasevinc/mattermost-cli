package staging

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ardasevinc/mattermost-cli/internal/mattermost"
	"github.com/ardasevinc/mattermost-cli/internal/schema"
	"github.com/ardasevinc/mattermost-cli/internal/stagestore"
)

type fakePosts struct {
	posts    map[string]mattermost.Post
	present  bool
	err      error
	calls    *[]string
	reaction []string
}

func (f *fakePosts) ByID(_ context.Context, id string) (mattermost.Post, error) {
	*f.calls = append(*f.calls, "post:"+id)
	if f.err != nil {
		return mattermost.Post{}, f.err
	}
	return f.posts[id], nil
}
func (f *fakePosts) ReactionState(_ context.Context, postID, channelID, userID, emoji string) (bool, error) {
	*f.calls = append(*f.calls, "reaction")
	f.reaction = []string{postID, channelID, userID, emoji}
	return f.present, f.err
}

type orderedUsers struct {
	user  mattermost.User
	calls *[]string
}

func (f orderedUsers) Current(context.Context) (mattermost.User, error) {
	*f.calls = append(*f.calls, "current")
	return f.user, nil
}
func (orderedUsers) ByUsernameFresh(context.Context, string) (mattermost.User, error) {
	return mattermost.User{}, errors.New("unused")
}

type orderedChannels struct {
	channel mattermost.Channel
	calls   *[]string
}

func (f orderedChannels) ExistingDirect(context.Context, string, string) (mattermost.Channel, bool, error) {
	return mattermost.Channel{}, false, errors.New("unused")
}
func (f orderedChannels) ByID(_ context.Context, id string) (mattermost.Channel, error) {
	*f.calls = append(*f.calls, "channel:"+id)
	return f.channel, nil
}
func (f orderedChannels) ByName(context.Context, string, string) (mattermost.Channel, error) {
	return mattermost.Channel{}, errors.New("unused")
}
func (f orderedChannels) Member(_ context.Context, channelID, userID string) (mattermost.ChannelMember, error) {
	*f.calls = append(*f.calls, "member")
	return mattermost.ChannelMember{ChannelID: channelID, UserID: userID}, nil
}

func postService(t *testing.T, post mattermost.Post, channel mattermost.Channel, store Store) (*Service, *fakePosts, *[]string) {
	t.Helper()
	calls := []string{}
	posts := &fakePosts{posts: map[string]mattermost.Post{post.ID: post}, calls: &calls}
	service, err := New("https://mattermost.example", "", nil,
		orderedUsers{mattermost.User{ID: "user-1", Username: "arda"}, &calls}, orderedChannels{channel, &calls}, emptyTeams{}, posts, store)
	if err != nil {
		t.Fatal(err)
	}
	return service, posts, &calls
}

func ordinaryPost() mattermost.Post {
	return mattermost.Post{ID: "post-1", ChannelID: "channel-1", UserID: "user-1", Message: "old", CreateAt: 1, UpdateAt: 2, FileIDs: []string{"file-1"}}
}

func TestReplyResolvesCanonicalRootBeforeBodyAndPersistsOnce(t *testing.T) {
	store := &recordingStore{}
	post := ordinaryPost()
	post.RootID = "root-1"
	service, posts, calls := postService(t, post, mattermost.Channel{ID: "channel-1", TeamID: "team-1", Type: "P", Name: "private"}, store)
	posts.posts["root-1"] = mattermost.Post{ID: "root-1", ChannelID: "channel-1", UserID: "other", Message: "root", CreateAt: 1, UpdateAt: 1, FileIDs: []string{}}
	reader := &panicReader{}
	_, err := service.Reply(context.Background(), ReplyInput{RequestID: "request-1", PostID: "post-1", Body: reader})
	if !errors.Is(err, ErrInput) || !reader.read || store.calls != 0 {
		t.Fatalf("error/read/store = %v/%v/%d", err, reader.read, store.calls)
	}
	wantOrder := []string{"current", "post:post-1", "post:root-1", "channel:channel-1", "member"}
	if !reflect.DeepEqual(*calls, wantOrder) {
		t.Fatalf("read order = %v", *calls)
	}

	result, err := service.Reply(context.Background(), ReplyInput{RequestID: "request-2", PostID: "post-1", Body: bytes.NewBufferString("reply")})
	if err != nil || store.calls != 1 || result.Preview.Destination.RootPostID == nil || *result.Preview.Destination.RootPostID != "root-1" {
		t.Fatalf("result/error/store = %#v/%v/%d", result, err, store.calls)
	}
	if got := string(store.in.Content.Destination); got != `{"kind":"post","channelId":"channel-1","channelType":"private","teamId":"team-1","postId":"post-1","rootPostId":"root-1","participantIds":[],"emoji":null,"postState":null,"reactionPresent":null}` {
		t.Fatalf("destination = %s", got)
	}
}

func TestEditBindsCanonicalDigestAndSchemaValidPreview(t *testing.T) {
	store := &recordingStore{}
	post := ordinaryPost()
	service, _, _ := postService(t, post, mattermost.Channel{ID: "channel-1", TeamID: "team-1", Type: "O", Name: "town-square"}, store)
	result, err := service.EditPost(context.Background(), EditPostInput{RequestID: "edit-1", PostID: "post-1", Body: bytes.NewBufferString("new")})
	if err != nil {
		t.Fatal(err)
	}
	wire := []byte(`{"message":"old","fileIds":["file-1"],"rootId":"","type":""}`)
	want := sha256.Sum256(wire)
	if result.Preview.Destination.PostState == nil || result.Preview.Destination.PostState.ContentDigest != hex.EncodeToString(want[:]) || result.Preview.Destination.PostState.UpdateAt != 2 {
		t.Fatalf("post state = %#v", result.Preview.Destination.PostState)
	}
	document := struct {
		Schema           string      `json:"schema"`
		Persist          bool        `json:"persist"`
		Operation        string      `json:"operation"`
		Binding          any         `json:"binding"`
		Destination      Destination `json:"destination"`
		Plan             Plan        `json:"plan"`
		ContentValidated bool        `json:"contentValidated"`
	}{"mm/v2/stage-preview", false, "edit_post", struct {
		ServerURL string  `json:"serverUrl"`
		ServerID  *string `json:"serverId"`
		UserID    string  `json:"userId"`
	}{result.Preview.ServerURL, nil, result.Preview.UserID}, result.Preview.Destination, result.Preview.Plan, false}
	encoded, _ := json.Marshal(document)
	registry, loadErr := schema.Load()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if err := registry.Validate("mm/v2/stage-preview", bytes.NewReader(encoded)); err != nil {
		t.Fatalf("preview rejected: %v\n%s", err, encoded)
	}
}

func TestReactionUsesAuthoritativeStateAndExactBinding(t *testing.T) {
	store := &recordingStore{}
	post := ordinaryPost()
	post.RootID = "root-1"
	service, posts, calls := postService(t, post, mattermost.Channel{ID: "channel-1", Type: "D", Name: "user-1__user-1"}, store)
	posts.present = true
	result, err := service.React(context.Background(), ReactionInput{RequestID: "react-1", PostID: "post-1", Emoji: "Eyes"})
	if err != nil || result.Preview.Destination.ReactionPresent == nil || !*result.Preview.Destination.ReactionPresent {
		t.Fatalf("result/error = %#v/%v", result, err)
	}
	if !reflect.DeepEqual(posts.reaction, []string{"post-1", "channel-1", "user-1", "eyes"}) || result.Preview.Destination.Emoji == nil || *result.Preview.Destination.Emoji != "eyes" || !reflect.DeepEqual(result.Preview.Destination.ParticipantIDs, []string{"user-1"}) {
		t.Fatalf("reaction/participants = %v/%v", posts.reaction, result.Preview.Destination.ParticipantIDs)
	}
	if result.Preview.Destination.RootPostID == nil || *result.Preview.Destination.RootPostID != "root-1" {
		t.Fatalf("root binding = %#v", result.Preview.Destination.RootPostID)
	}
	wantOrder := []string{"current", "post:post-1", "channel:channel-1", "member", "reaction"}
	if !reflect.DeepEqual(*calls, wantOrder) || string(store.in.Content.Plan) != `{"steps":[{"ordinal":1,"type":"add_reaction","condition":"if_missing"}]}` {
		t.Fatalf("order/plan = %v/%s", *calls, store.in.Content.Plan)
	}
}

func TestPostDigestUsesCanonicalUTF8JSONAndPreservesFileOrder(t *testing.T) {
	post := mattermost.Post{Message: "<&>\u2028\u2029", FileIDs: []string{"b", "a"}, RootID: "root"}
	if got, want := digestPost(post, nil), "b570850c2b2a6334cb6ad60168d84baf9b0d6198872b58fab3c3646413d3f99e"; got != want {
		t.Fatalf("digest = %s, want %s", got, want)
	}
	post.FileIDs = []string{"a", "b"}
	if digestPost(post, nil) == "b570850c2b2a6334cb6ad60168d84baf9b0d6198872b58fab3c3646413d3f99e" {
		t.Fatal("file order was not bound")
	}
	post.Message, post.FileIDs = `<&>\u2028\u2029`, []string{"b", "a"}
	if got, want := digestPost(post, nil), "92065e494852d7c2196f8f53227cbbfa7f901ee37beabb6fb84b4fdebe91ada0"; got != want {
		t.Fatalf("literal escape digest = %s, want %s", got, want)
	}
}

func TestPostDigestCannotVerifyCredentialBearingFileID(t *testing.T) {
	first := mattermost.Post{Message: "safe", FileIDs: []string{"token-a"}}
	second := mattermost.Post{Message: "safe", FileIDs: []string{"token-b"}}
	if left, right := PostContentDigest(first, [][]byte{[]byte("token-a")}), PostContentDigest(second, [][]byte{[]byte("token-b")}); left != right {
		t.Fatalf("credential-elided file IDs produced distinct digests: %s/%s", left, right)
	}
}

func TestInvalidPostIDsAreZeroNetwork(t *testing.T) {
	service, _, calls := postService(t, ordinaryPost(), mattermost.Channel{ID: "channel-1", Type: "G", Name: "group"}, &recordingStore{})
	for _, id := range []string{"bad/id", "bad\u202e", strings.Repeat("a", 129)} {
		*calls = nil
		if _, err := service.DryRunDeletePost(context.Background(), PostDryRunInput{PostID: id}); !errors.Is(err, ErrInvalid) || len(*calls) != 0 {
			t.Fatalf("id/error/calls = %q/%v/%v", id, err, *calls)
		}
	}
}

func TestForeignAndSystemEditDeleteStopBeforeChannelRead(t *testing.T) {
	for _, post := range []mattermost.Post{
		{ID: "post-1", ChannelID: "channel-1", UserID: "other", UpdateAt: 1, FileIDs: []string{}},
		{ID: "post-1", ChannelID: "channel-1", UserID: "", Type: "system_join_channel", UpdateAt: 1, FileIDs: []string{}},
	} {
		service, _, calls := postService(t, post, mattermost.Channel{ID: "channel-1", Type: "G", Name: "group"}, &recordingStore{})
		if _, err := service.DryRunDeletePost(context.Background(), PostDryRunInput{PostID: "post-1"}); !errors.Is(err, ErrTarget) || !reflect.DeepEqual(*calls, []string{"current", "post:post-1"}) {
			t.Fatalf("post/error/calls = %#v/%v/%v", post, err, *calls)
		}
	}
}

func TestResolvedPostBoundsAndIdentityFailClosed(t *testing.T) {
	for _, alter := range []func(*mattermost.Post){
		func(p *mattermost.Post) { p.UpdateAt = 8_640_000_000_000_001 },
		func(p *mattermost.Post) { p.ChannelID = "bad/channel" },
		func(p *mattermost.Post) { p.RootID = "bad/root" },
	} {
		post := ordinaryPost()
		alter(&post)
		service, _, calls := postService(t, post, mattermost.Channel{ID: "channel-1", Type: "G", Name: "group"}, &recordingStore{})
		if _, err := service.DryRunReact(context.Background(), ReactionDryRunInput{PostID: "post-1", Emoji: "wave"}); !errors.Is(err, ErrTarget) || !reflect.DeepEqual(*calls, []string{"current", "post:post-1"}) {
			t.Fatalf("post/error/calls = %#v/%v/%v", post, err, *calls)
		}
	}
}

func TestReplyAttachmentMetadataPreflightIsZeroNetworkAndFileIO(t *testing.T) {
	for _, attachment := range []Attachment{{Path: "missing", RemoteFilename: "../x"}, {Path: "missing", RemoteFilename: "a/b"}, {Path: "missing", MediaType: "not mime"}} {
		service, _, calls := postService(t, ordinaryPost(), mattermost.Channel{ID: "channel-1", Type: "G", Name: "group"}, &recordingStore{})
		reader := &panicReader{}
		if _, err := service.Reply(context.Background(), ReplyInput{RequestID: "reply-1", PostID: "post-1", Body: reader, Attachments: []Attachment{attachment}}); !errors.Is(err, ErrInput) || len(*calls) != 0 || reader.read {
			t.Fatalf("attachment/error/calls/read = %#v/%v/%v/%v", attachment, err, *calls, reader.read)
		}
	}
}

func TestAuthorlessSystemPostAllowsReplyAndReaction(t *testing.T) {
	post := mattermost.Post{ID: "post-1", ChannelID: "channel-1", Type: "system_join_channel", UpdateAt: 1, FileIDs: []string{}}
	service, posts, _ := postService(t, post, mattermost.Channel{ID: "channel-1", Type: "G", Name: "group"}, &recordingStore{})
	if _, err := service.DryRunReply(context.Background(), PostDryRunInput{PostID: "post-1"}); err != nil {
		t.Fatalf("reply = %v", err)
	}
	posts.present = false
	if _, err := service.DryRunReact(context.Background(), ReactionDryRunInput{PostID: "post-1", Emoji: "wave"}); err != nil {
		t.Fatalf("react = %v", err)
	}
}

func TestEditCanRemediateCredentialInExistingRemoteMessage(t *testing.T) {
	const token = "leaked-active-credential"
	post := ordinaryPost()
	post.Message = "please remove " + token
	store := &recordingStore{}
	service, _, _ := postService(t, post, mattermost.Channel{ID: "channel-1", Type: "G", Name: "group"}, store)
	service.credentials = [][]byte{[]byte(token)}
	if _, err := service.EditPost(context.Background(), EditPostInput{"edit-remediation", "post-1", bytes.NewBufferString("credential removed")}); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(store.in.Content.Destination, []byte(token)) || bytes.Contains(store.in.Content.Plan, []byte(token)) {
		t.Fatal("remote credential leaked into staged semantics")
	}
	masked := digestPost(post, [][]byte{[]byte(token)})
	if masked == digestPost(post, nil) || !bytes.Contains(store.in.Content.Destination, []byte(masked)) {
		t.Fatal("post binding did not mask the active credential before hashing")
	}
	other := post
	other.Message = "please remove another-active-credential"
	if digestPost(other, [][]byte{[]byte("another-active-credential")}) != masked {
		t.Fatal("post binding remains suitable for offline credential guessing")
	}
}

func TestPostMutationRejectsCallerCredentialBeforeNetwork(t *testing.T) {
	post := ordinaryPost()
	service, posts, calls := postService(t, post, mattermost.Channel{ID: "channel-1", Type: "G", Name: "group"}, &recordingStore{})
	service.credentials = [][]byte{[]byte("Secret")}
	_, err := service.React(context.Background(), ReactionInput{RequestID: "request", PostID: "post-1", Emoji: "Secret"})
	if !errors.Is(err, ErrCredential) || len(*calls) != 0 || len(posts.reaction) != 0 {
		t.Fatalf("error/calls = %v/%v", err, *calls)
	}
}

func TestEditAndDeleteRejectForeignOrSystemPosts(t *testing.T) {
	for _, alter := range []func(*mattermost.Post){
		func(p *mattermost.Post) { p.UserID = "other" },
		func(p *mattermost.Post) { p.Type = "system_join_channel" },
	} {
		post := ordinaryPost()
		alter(&post)
		store := &recordingStore{}
		service, _, _ := postService(t, post, mattermost.Channel{ID: "channel-1", Type: "G", Name: "group"}, store)
		if _, err := service.DeletePost(context.Background(), DeletePostInput{RequestID: "delete-1", PostID: "post-1"}); !errors.Is(err, ErrTarget) || store.calls != 0 {
			t.Fatalf("post/error/store = %#v/%v/%d", post, err, store.calls)
		}
	}
}

func TestDryRunsNeverReadBodyBindFilesOrPersist(t *testing.T) {
	store := &recordingStore{}
	service, _, _ := postService(t, ordinaryPost(), mattermost.Channel{ID: "channel-1", Type: "G", Name: "group"}, store)
	service = service.WithAttachmentBinder(func(context.Context, []Attachment, [][]byte) ([]stagestore.Attachment, error) {
		t.Fatal("binder called")
		return nil, nil
	})
	if _, err := service.DryRunReply(context.Background(), PostDryRunInput{PostID: "post-1"}); err != nil || store.calls != 0 {
		t.Fatalf("error/store = %v/%d", err, store.calls)
	}
}

func TestAllPostMutationDryRunProducersValidate(t *testing.T) {
	store := &recordingStore{}
	service, posts, _ := postService(t, ordinaryPost(), mattermost.Channel{ID: "channel-1", TeamID: "team-1", Type: "O", Name: "town-square"}, store)
	tests := []struct {
		operation string
		run       func() (Preview, error)
	}{
		{"reply", func() (Preview, error) { return service.DryRunReply(context.Background(), PostDryRunInput{"post-1"}) }},
		{"edit_post", func() (Preview, error) {
			return service.DryRunEditPost(context.Background(), PostDryRunInput{"post-1"})
		}},
		{"delete_post", func() (Preview, error) {
			return service.DryRunDeletePost(context.Background(), PostDryRunInput{"post-1"})
		}},
		{"react", func() (Preview, error) {
			return service.DryRunReact(context.Background(), ReactionDryRunInput{"post-1", "wave"})
		}},
		{"unreact", func() (Preview, error) {
			return service.DryRunUnreact(context.Background(), ReactionDryRunInput{"post-1", "wave"})
		}},
	}
	registry, err := schema.Load()
	if err != nil {
		t.Fatal(err)
	}
	posts.present = true
	for _, test := range tests {
		t.Run(test.operation, func(t *testing.T) {
			preview, runErr := test.run()
			if runErr != nil {
				t.Fatal(runErr)
			}
			document := struct {
				Schema, Operation string
				Persist           bool
				Binding           any
				Destination       Destination
				Plan              Plan
				ContentValidated  bool
			}{"mm/v2/stage-preview", test.operation, false, struct {
				ServerURL string  `json:"serverUrl"`
				ServerID  *string `json:"serverId"`
				UserID    string  `json:"userId"`
			}{preview.ServerURL, nil, preview.UserID}, preview.Destination, preview.Plan, false}
			encoded, marshalErr := json.Marshal(struct {
				Schema           string      `json:"schema"`
				Persist          bool        `json:"persist"`
				Operation        string      `json:"operation"`
				Binding          any         `json:"binding"`
				Destination      Destination `json:"destination"`
				Plan             Plan        `json:"plan"`
				ContentValidated bool        `json:"contentValidated"`
			}{document.Schema, document.Persist, document.Operation, document.Binding, document.Destination, document.Plan, document.ContentValidated})
			if marshalErr != nil || registry.Validate("mm/v2/stage-preview", bytes.NewReader(encoded)) != nil {
				t.Fatalf("producer rejected: %v\n%s", marshalErr, encoded)
			}
		})
	}
	if store.calls != 0 {
		t.Fatalf("dry-run store calls = %d", store.calls)
	}
}

func TestDeleteAndReactionPersistenceUseExactOperationsPlansAndState(t *testing.T) {
	store := &recordingStore{}
	service, posts, _ := postService(t, ordinaryPost(), mattermost.Channel{ID: "channel-1", Type: "G", Name: "group"}, store)
	if _, err := service.DeletePost(context.Background(), DeletePostInput{"delete-1", "post-1"}); err != nil || store.in.Operation != stagestore.DeletePost || string(store.in.Content.Plan) != `{"steps":[{"ordinal":1,"type":"delete_post","condition":"always"}]}` || len(store.in.Content.Body) != 0 || len(store.in.Content.Attachments) != 0 {
		t.Fatalf("delete/error = %#v/%v", store.in, err)
	}
	const deleteDestination = `{"kind":"post","channelId":"channel-1","channelType":"group","teamId":null,"postId":"post-1","rootPostId":null,"participantIds":[],"emoji":null,"postState":{"authorUserId":"user-1","updateAt":2,"contentDigest":"f21e180b300ba23e417df2f2dc5f9da53cf8bd538c7863eebe9c186dadf8c35f"},"reactionPresent":null}`
	if string(store.in.Content.Destination) != deleteDestination {
		t.Fatalf("delete destination = %s", store.in.Content.Destination)
	}
	for _, operation := range []stagestore.Operation{stagestore.React, stagestore.Unreact} {
		for _, present := range []bool{false, true} {
			posts.present = present
			requestID := string(operation) + map[bool]string{false: "-absent", true: "-present"}[present]
			var err error
			if operation == stagestore.React {
				_, err = service.React(context.Background(), ReactionInput{requestID, "post-1", "wave"})
			} else {
				_, err = service.Unreact(context.Background(), ReactionInput{requestID, "post-1", "wave"})
			}
			typeName := map[stagestore.Operation]string{stagestore.React: "add_reaction", stagestore.Unreact: "remove_reaction"}[operation]
			wantPlan := `{"steps":[{"ordinal":1,"type":"` + typeName + `","condition":"if_missing"}]}`
			wantDestination := `{"kind":"reaction","channelId":"channel-1","channelType":"group","teamId":null,"postId":"post-1","rootPostId":null,"participantIds":[],"emoji":"wave","postState":null,"reactionPresent":` + map[bool]string{false: "false", true: "true"}[present] + `}`
			if err != nil || store.in.Operation != operation || string(store.in.Content.Plan) != wantPlan || string(store.in.Content.Destination) != wantDestination || len(store.in.Content.Body) != 0 || len(store.in.Content.Attachments) != 0 {
				t.Fatalf("operation/present/input/error = %s/%v/%#v/%v", operation, present, store.in, err)
			}
		}
	}
}

func TestStoreCancellationIsPreserved(t *testing.T) {
	for _, sentinel := range []error{context.Canceled, context.DeadlineExceeded} {
		store := &recordingStore{err: sentinel}
		service, _, _ := postService(t, ordinaryPost(), mattermost.Channel{ID: "channel-1", Type: "G", Name: "group"}, store)
		if _, err := service.DeletePost(context.Background(), DeletePostInput{"delete-1", "post-1"}); !errors.Is(err, sentinel) || errors.Is(err, ErrStore) {
			t.Fatalf("post cancellation = %v", err)
		}
		create, _, _ := dmService(t, store)
		if _, err := create.CreatePost(context.Background(), CreatePostInput{RequestID: "create-1", Target: dmTarget(), Body: bytes.NewBufferString("hello")}); !errors.Is(err, sentinel) || errors.Is(err, ErrStore) {
			t.Fatalf("create cancellation = %v", err)
		}

		readService, posts, _ := postService(t, ordinaryPost(), mattermost.Channel{ID: "channel-1", Type: "G", Name: "group"}, &recordingStore{})
		posts.err = sentinel
		if _, err := readService.DryRunDeletePost(context.Background(), PostDryRunInput{"post-1"}); !errors.Is(err, sentinel) || errors.Is(err, ErrTarget) {
			t.Fatalf("target-read cancellation = %v", err)
		}

		binderService, _, _ := postService(t, ordinaryPost(), mattermost.Channel{ID: "channel-1", Type: "G", Name: "group"}, &recordingStore{})
		binderService = binderService.WithAttachmentBinder(func(context.Context, []Attachment, [][]byte) ([]stagestore.Attachment, error) { return nil, sentinel })
		if _, err := binderService.Reply(context.Background(), ReplyInput{RequestID: "reply-1", PostID: "post-1", Body: bytes.NewBufferString("hello"), Attachments: []Attachment{{Path: "safe"}}}); !errors.Is(err, sentinel) || errors.Is(err, ErrInput) {
			t.Fatalf("reply binder cancellation = %v", err)
		}

		conversation, users, channels := dmService(t, &recordingStore{})
		users.err = sentinel
		if _, err := conversation.DryRunCreatePost(context.Background(), DryRunInput{Target: dmTarget()}); !errors.Is(err, sentinel) || errors.Is(err, ErrTarget) {
			t.Fatalf("conversation-auth cancellation = %v", err)
		}
		users.err = nil
		channels.err = sentinel
		if _, err := conversation.CreatePost(context.Background(), CreatePostInput{RequestID: "conversation-1", Target: dmTarget(), Body: bytes.NewBufferString("hello")}); !errors.Is(err, sentinel) || errors.Is(err, ErrTarget) {
			t.Fatalf("conversation-target cancellation = %v", err)
		}
	}
}

func realStageStore(t *testing.T) *stagestore.Store {
	t.Helper()
	dir, err := os.MkdirTemp(".", ".staging-replay-")
	if err != nil {
		t.Fatal(err)
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	store, err := stagestore.Open(context.Background(), filepath.Join(dir, stagestore.DatabaseFilename))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestRealStorePostMutationReplaysIgnoreRemoteDrift(t *testing.T) {
	store := realStageStore(t)
	service, posts, calls := postService(t, ordinaryPost(), mattermost.Channel{ID: "channel-1", Type: "G", Name: "group"}, store)
	tests := []struct {
		name   string
		first  func() (CreatePostResult, error)
		replay func() (CreatePostResult, error)
	}{
		{"reply", func() (CreatePostResult, error) {
			return service.Reply(context.Background(), ReplyInput{RequestID: "replay-reply", PostID: "post-1", Body: bytes.NewBufferString("body")})
		}, func() (CreatePostResult, error) {
			return service.Reply(context.Background(), ReplyInput{RequestID: "replay-reply", PostID: "post-1", Body: bytes.NewBufferString("body")})
		}},
		{"edit", func() (CreatePostResult, error) {
			return service.EditPost(context.Background(), EditPostInput{"replay-edit", "post-1", bytes.NewBufferString("new")})
		}, func() (CreatePostResult, error) {
			return service.EditPost(context.Background(), EditPostInput{"replay-edit", "post-1", bytes.NewBufferString("new")})
		}},
		{"delete", func() (CreatePostResult, error) {
			return service.DeletePost(context.Background(), DeletePostInput{"replay-delete", "post-1"})
		}, func() (CreatePostResult, error) {
			return service.DeletePost(context.Background(), DeletePostInput{"replay-delete", "post-1"})
		}},
		{"react", func() (CreatePostResult, error) {
			posts.present = false
			return service.React(context.Background(), ReactionInput{"replay-react", "post-1", "wave"})
		}, func() (CreatePostResult, error) {
			return service.React(context.Background(), ReactionInput{"replay-react", "post-1", "wave"})
		}},
		{"unreact", func() (CreatePostResult, error) {
			posts.present = true
			return service.Unreact(context.Background(), ReactionInput{"replay-unreact", "post-1", "wave"})
		}, func() (CreatePostResult, error) {
			return service.Unreact(context.Background(), ReactionInput{"replay-unreact", "post-1", "wave"})
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			posts.err = nil
			first, err := test.first()
			if err != nil {
				t.Fatal(err)
			}
			firstDestination, _ := json.Marshal(first.Preview.Destination)
			before := len(*calls)
			posts.err = errors.New("remote deleted")
			posts.present = !posts.present
			replay, err := test.replay()
			if err != nil || !replay.Stored.Replay || replay.Stored.Stage.ID != first.Stored.Stage.ID {
				t.Fatalf("replay/error = %#v/%v", replay, err)
			}
			replayDestination, _ := json.Marshal(replay.Preview.Destination)
			if !bytes.Equal(firstDestination, replayDestination) || len(*calls) != before+1 || (*calls)[before] != "current" {
				t.Fatalf("destination/calls drift = %s/%s/%v", firstDestination, replayDestination, (*calls)[before:])
			}
		})
	}
}

func TestRealStoreReplyReplayDoesNotOpenMissingAttachment(t *testing.T) {
	store := realStageStore(t)
	service, _, _ := postService(t, ordinaryPost(), mattermost.Channel{ID: "channel-1", Type: "G", Name: "group"}, store)
	dir, err := os.MkdirTemp(".", ".attachment-replay-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "attachment.txt")
	if err := os.WriteFile(path, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	in := func() ReplyInput {
		return ReplyInput{RequestID: "attachment-replay", PostID: "post-1", Body: bytes.NewBufferString("body"), Attachments: []Attachment{{Path: path}}}
	}
	first, err := service.Reply(context.Background(), in())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	service = service.WithAttachmentBinder(func(context.Context, []Attachment, [][]byte) ([]stagestore.Attachment, error) {
		t.Fatal("binder called on replay")
		return nil, nil
	})
	replay, err := service.Reply(context.Background(), in())
	if err != nil || !replay.Stored.Replay || replay.Stored.Stage.ID != first.Stored.Stage.ID {
		t.Fatalf("replay/error = %#v/%v", replay, err)
	}
}

type foundCreateStore struct {
	record stagestore.CreateRecord
	finds  int
}

func (s *foundCreateStore) FindCreate(context.Context, string, string, string) (stagestore.CreateRecord, bool, error) {
	s.finds++
	return s.record, true, nil
}
func (*foundCreateStore) Create(context.Context, stagestore.CreateInput) (stagestore.CreateRecord, error) {
	panic("Create called")
}

func TestReplayOperationConflictPrecedesBodyAndAuthPrecedesFind(t *testing.T) {
	store := &foundCreateStore{record: stagestore.CreateRecord{MutationResult: stagestore.MutationResult{Stage: stagestore.StageSummary{Operation: stagestore.Reply}}}}
	service, _, _ := postService(t, ordinaryPost(), mattermost.Channel{ID: "channel-1", Type: "G", Name: "group"}, store)
	reader := &panicReader{}
	if _, err := service.EditPost(context.Background(), EditPostInput{"same-request", "post-1", reader}); !errors.Is(err, ErrConflict) || reader.read || store.finds != 1 {
		t.Fatalf("error/read/finds = %v/%v/%d", err, reader.read, store.finds)
	}

	calls := []string{}
	badUsers := orderedUsers{mattermost.User{ID: "bad\u200b", Username: "arda"}, &calls}
	service, err := New("https://mattermost.example", "", nil, badUsers, orderedChannels{mattermost.Channel{}, &calls}, emptyTeams{}, &fakePosts{calls: &calls}, store)
	if err != nil {
		t.Fatal(err)
	}
	store.finds = 0
	if _, err = service.DeletePost(context.Background(), DeletePostInput{"request", "post-1"}); !errors.Is(err, ErrTarget) || store.finds != 0 {
		t.Fatalf("error/finds = %v/%d", err, store.finds)
	}
}
