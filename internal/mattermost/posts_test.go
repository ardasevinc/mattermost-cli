package mattermost

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
)

type postTransportFunc func(context.Context, string, any) error

const validReactionRow = `{"user_id":"user","post_id":"post","emoji_name":"Eyes","create_at":1,"update_at":1,"delete_at":0,"remote_id":null,"channel_id":"channel"}`

func (f postTransportFunc) Get(ctx context.Context, path string, out any) error {
	return f(ctx, path, out)
}

func (f postTransportFunc) PostRead(ctx context.Context, path string, _ any, out any) error {
	return f(ctx, path, out)
}

type searchTransportFunc func(context.Context, string, any, any) error

func (f searchTransportFunc) Get(context.Context, string, any) error {
	return errors.New("unexpected GET")
}
func (f searchTransportFunc) PostRead(ctx context.Context, path string, body, out any) error {
	return f(ctx, path, body, out)
}

func TestPostByIDBuildsExactGETAndRequiresCanonicalLivePost(t *testing.T) {
	var gotPath string
	api := NewPosts(postTransportFunc(func(_ context.Context, path string, out any) error {
		gotPath = path
		return json.Unmarshal([]byte(`{"id":"post","channel_id":"channel","user_id":"author","message":"hello","create_at":1,"update_at":1,"delete_at":0,"root_id":"","type":"","file_ids":["file-a","file-b"]}`), out)
	}))
	post, err := api.ByID(context.Background(), "post")
	if err != nil || post.ID != "post" || post.ChannelID != "channel" || post.UserID != "author" || !reflect.DeepEqual(post.FileIDs, []string{"file-a", "file-b"}) {
		t.Fatalf("post=%#v error=%v", post, err)
	}
	if gotPath != "/posts/post" {
		t.Fatalf("path=%q", gotPath)
	}
}

func TestPostByIDRejectsInvalidRequestMismatchMalformedAndDeleted(t *testing.T) {
	for _, id := range []string{"", " post", "post ", "slash/id", "nonascii-é"} {
		called := false
		api := NewPosts(postTransportFunc(func(context.Context, string, any) error { called = true; return nil }))
		if _, err := api.ByID(context.Background(), id); !errors.Is(err, ErrInvalidPostsRequest) || called {
			t.Fatalf("id=%q error=%v called=%v", id, err, called)
		}
	}
	for name, payload := range map[string]string{
		"mismatch":           `{"id":"other","channel_id":"channel","user_id":"author","message":"hello","create_at":1,"update_at":1,"delete_at":0,"root_id":""}`,
		"malformed":          `{"id":"post","channel_id":"channel","user_id":"author","create_at":1,"update_at":1,"delete_at":0,"root_id":""}`,
		"missing author":     `{"id":"post","channel_id":"channel","message":"hello","create_at":1,"update_at":1,"delete_at":0,"root_id":""}`,
		"missing update":     `{"id":"post","channel_id":"channel","user_id":"author","message":"hello","create_at":1,"delete_at":0,"root_id":""}`,
		"oversized update":   `{"id":"post","channel_id":"channel","user_id":"author","message":"hello","create_at":1,"update_at":8640000000000001,"delete_at":0,"root_id":""}`,
		"whitespace channel": `{"id":"post","channel_id":" channel","user_id":"author","message":"hello","create_at":1,"update_at":1,"delete_at":0,"root_id":""}`,
		"whitespace author":  `{"id":"post","channel_id":"channel","user_id":"author ","message":"hello","create_at":1,"update_at":1,"delete_at":0,"root_id":""}`,
		"missing root":       `{"id":"post","channel_id":"channel","user_id":"author","message":"hello","create_at":1,"update_at":1,"delete_at":0}`,
		"wrong-type root":    `{"id":"post","channel_id":"channel","user_id":"author","message":"hello","create_at":1,"update_at":1,"delete_at":0,"root_id":7}`,
		"unsafe root":        `{"id":"post","channel_id":"channel","user_id":"author","message":"hello","create_at":1,"update_at":1,"delete_at":0,"root_id":"bad/root"}`,
		"deleted":            `{"id":"post","channel_id":"channel","user_id":"author","message":"stale","create_at":1,"update_at":1,"delete_at":2,"root_id":""}`,
		"missing type":       `{"id":"post","channel_id":"channel","user_id":"author","message":"hello","create_at":1,"update_at":1,"delete_at":0,"root_id":"","file_ids":[]}`,
		"unsafe type":        `{"id":"post","channel_id":"channel","user_id":"author","message":"hello","create_at":1,"update_at":1,"delete_at":0,"root_id":"","type":"bad\u001b","file_ids":[]}`,
		"oversized type":     `{"id":"post","channel_id":"channel","user_id":"author","message":"hello","create_at":1,"update_at":1,"delete_at":0,"root_id":"","type":"123456789012345678901234567","file_ids":[]}`,
		"missing files":      `{"id":"post","channel_id":"channel","user_id":"author","message":"hello","create_at":1,"update_at":1,"delete_at":0,"root_id":"","type":""}`,
		"duplicate files":    `{"id":"post","channel_id":"channel","user_id":"author","message":"hello","create_at":1,"update_at":1,"delete_at":0,"root_id":"","type":"","file_ids":["file","file"]}`,
		"unsafe file":        `{"id":"post","channel_id":"channel","user_id":"author","message":"hello","create_at":1,"update_at":1,"delete_at":0,"root_id":"","type":"","file_ids":["bad/file"]}`,
	} {
		t.Run(name, func(t *testing.T) {
			api := NewPosts(postTransportFunc(func(_ context.Context, _ string, out any) error {
				return json.Unmarshal([]byte(payload), out)
			}))
			if _, err := api.ByID(context.Background(), "post"); !errors.Is(err, ErrInvalidPostResponse) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestPostByIDAcceptsExplicitNullFileIDsAsCanonicalEmpty(t *testing.T) {
	api := NewPosts(postTransportFunc(func(_ context.Context, _ string, out any) error {
		return json.Unmarshal([]byte(`{"id":"post","channel_id":"channel","user_id":"author","message":"hello","create_at":1,"update_at":1,"delete_at":0,"root_id":"","type":"","file_ids":null}`), out)
	}))
	post, err := api.ByID(context.Background(), "post")
	if err != nil || post.FileIDs == nil || len(post.FileIDs) != 0 {
		t.Fatalf("post=%#v error=%v", post, err)
	}
}

func TestPostByIDRejectsDuplicateJSONMembers(t *testing.T) {
	base := `"channel_id":"channel","user_id":"author","message":"hello","create_at":1,"update_at":1,"delete_at":0,"root_id":"","type":"","file_ids":[]`
	for name, payload := range map[string]string{
		"conflicting":        `{"id":"post","id":"other",` + base + `}`,
		"identical":          `{"id":"post","id":"post",` + base + `}`,
		"escaped equivalent": `{"id":"post","\u0069d":"post",` + base + `}`,
	} {
		t.Run(name, func(t *testing.T) {
			api := NewPosts(postTransportFunc(func(_ context.Context, _ string, out any) error {
				return json.Unmarshal([]byte(payload), out)
			}))
			if _, err := api.ByID(context.Background(), "post"); !errors.Is(err, ErrInvalidPostResponse) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestPostByIDUsesOnlyExactCanonicalMemberNames(t *testing.T) {
	base := `"channel_id":"channel","user_id":"author","message":"hello","create_at":1,"update_at":1,"delete_at":0,"root_id":"","type":"","file_ids":[]`
	api := NewPosts(postTransportFunc(func(_ context.Context, _ string, out any) error {
		return json.Unmarshal([]byte(`{"id":"post","ID":"other",`+base+`}`), out)
	}))
	post, err := api.ByID(context.Background(), "post")
	if err != nil || post.ID != "post" || post.Message != "hello" {
		t.Fatalf("post=%#v error=%v", post, err)
	}

	api = NewPosts(postTransportFunc(func(_ context.Context, _ string, out any) error {
		return json.Unmarshal([]byte(`{"ID":"post",`+base+`}`), out)
	}))
	if _, err := api.ByID(context.Background(), "post"); !errors.Is(err, ErrInvalidPostResponse) {
		t.Fatalf("missing canonical id error=%v", err)
	}
}

func TestPostByIDPropagatesCancellationAndTransportErrors(t *testing.T) {
	sentinel := errors.New("transport failed")
	api := NewPosts(postTransportFunc(func(_ context.Context, _ string, _ any) error { return sentinel }))
	if _, err := api.ByID(context.Background(), "post"); !errors.Is(err, sentinel) {
		t.Fatalf("error=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	api = NewPosts(postTransportFunc(func(ctx context.Context, _ string, _ any) error { return ctx.Err() }))
	if _, err := api.ByID(ctx, "post"); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
}

func TestPostByIDAcceptsCanonicalReplyRoot(t *testing.T) {
	api := NewPosts(postTransportFunc(func(_ context.Context, _ string, out any) error {
		return json.Unmarshal([]byte(`{"id":"reply","channel_id":"channel","user_id":"author","message":"hello","create_at":2,"update_at":2,"delete_at":0,"root_id":"root","type":"","file_ids":[]}`), out)
	}))
	post, err := api.ByID(context.Background(), "reply")
	if err != nil || post.RootID != "root" {
		t.Fatalf("post=%#v error=%v", post, err)
	}
}

func TestPostByIDIsRaceSafe(t *testing.T) {
	api := NewPosts(postTransportFunc(func(_ context.Context, _ string, out any) error {
		return json.Unmarshal([]byte(`{"id":"post","channel_id":"channel","user_id":"author","message":"hello","create_at":1,"update_at":1,"delete_at":0,"root_id":"","type":"","file_ids":[]}`), out)
	}))
	var wg sync.WaitGroup
	for range 40 {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = api.ByID(context.Background(), "post") }()
	}
	wg.Wait()
}

func TestReactionStateUsesExactFreshGETAndReturnsPresentOrAbsent(t *testing.T) {
	for _, tt := range []struct {
		name, payload string
		want          bool
	}{
		{"present", `[` + validReactionRow + `]`, true},
		{"absent", `[{"user_id":"other","post_id":"post","emoji_name":"Eyes","create_at":1,"update_at":1,"delete_at":0,"remote_id":"","channel_id":"channel"}]`, false},
		{"empty array", `[]`, false},
		{"real empty null", `null`, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var path string
			api := NewPosts(postTransportFunc(func(_ context.Context, got string, out any) error {
				path = got
				return json.Unmarshal([]byte(tt.payload), out)
			}))
			got, err := api.ReactionState(context.Background(), "post", "channel", "user", "Eyes")
			if err != nil || got != tt.want || path != "/posts/post/reactions" {
				t.Fatalf("state=%v path=%q error=%v", got, path, err)
			}
		})
	}
}

func TestReactionStateRejectsInvalidInputsBeforeNetwork(t *testing.T) {
	for _, args := range [][4]string{{"", "channel", "user", "eyes"}, {"bad/post", "channel", "user", "eyes"}, {"post", "bad/channel", "user", "eyes"}, {"post", "channel", "bad user", "eyes"}, {"post", "channel", "user", "bad:emoji"}} {
		called := false
		api := NewPosts(postTransportFunc(func(context.Context, string, any) error { called = true; return nil }))
		if _, err := api.ReactionState(context.Background(), args[0], args[1], args[2], args[3]); !errors.Is(err, ErrInvalidPostsRequest) || called {
			t.Fatalf("args=%q error=%v called=%v", args, err, called)
		}
	}
}

func TestReactionStateRejectsIncompleteAmbiguousOrHostileResponses(t *testing.T) {
	prefix := strings.TrimSuffix(validReactionRow, "}")
	for name, payload := range map[string]string{
		"object":             `{}`,
		"null item":          `[null]`,
		"missing field":      `[{"user_id":"user","post_id":"post","emoji_name":"Eyes","create_at":1,"update_at":1,"delete_at":0,"channel_id":"channel"}]`,
		"wrong type":         `[{"user_id":7,"post_id":"post","emoji_name":"Eyes","create_at":1,"update_at":1,"delete_at":0,"remote_id":null,"channel_id":"channel"}]`,
		"foreign post":       strings.Replace(validReactionRow, `"post_id":"post"`, `"post_id":"other"`, 1),
		"foreign channel":    strings.Replace(validReactionRow, `"channel_id":"channel"`, `"channel_id":"other"`, 1),
		"hostile user":       strings.Replace(validReactionRow, `"user_id":"user"`, `"user_id":"bad/user"`, 1),
		"bad emoji":          strings.Replace(validReactionRow, `"emoji_name":"Eyes"`, `"emoji_name":"bad:emoji"`, 1),
		"zero update":        strings.Replace(validReactionRow, `"update_at":1`, `"update_at":0`, 1),
		"deleted":            strings.Replace(validReactionRow, `"delete_at":0`, `"delete_at":2`, 1),
		"hostile remote":     strings.Replace(validReactionRow, `"remote_id":null`, `"remote_id":"bad\u001b"`, 1),
		"duplicate":          `[` + prefix + `},` + prefix + `}]`,
		"conflicting member": `[` + strings.Replace(validReactionRow, `"user_id":"user"`, `"user_id":"user","user_id":"other"`, 1) + `]`,
		"identical member":   `[` + strings.Replace(validReactionRow, `"user_id":"user"`, `"user_id":"user","user_id":"user"`, 1) + `]`,
		"escaped member":     `[` + strings.Replace(validReactionRow, `"user_id":"user"`, `"user_id":"user","user_\u0069d":"user"`, 1) + `]`,
	} {
		t.Run(name, func(t *testing.T) {
			api := NewPosts(postTransportFunc(func(_ context.Context, _ string, out any) error { return json.Unmarshal([]byte(payload), out) }))
			if _, err := api.ReactionState(context.Background(), "post", "channel", "user", "Eyes"); !errors.Is(err, ErrInvalidReactionsResponse) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestReactionStateAllowsUnknownAdditiveFields(t *testing.T) {
	payload := `[` + strings.TrimSuffix(validReactionRow, "}") + `,"future":{"value":true}}]`
	api := NewPosts(postTransportFunc(func(_ context.Context, _ string, out any) error { return json.Unmarshal([]byte(payload), out) }))
	got, err := api.ReactionState(context.Background(), "post", "channel", "user", "Eyes")
	if err != nil || !got {
		t.Fatalf("state=%v error=%v", got, err)
	}
}

func TestReactionStateBoundsCancellationAndRaceSafety(t *testing.T) {
	tooMany := make([]PostReaction, maxPostReactions+1)
	payload, err := json.Marshal(tooMany)
	if err != nil {
		t.Fatal(err)
	}
	api := NewPosts(postTransportFunc(func(_ context.Context, _ string, out any) error { return json.Unmarshal(payload, out) }))
	if _, err := api.ReactionState(context.Background(), "post", "channel", "user", "eyes"); !errors.Is(err, ErrInvalidReactionsResponse) {
		t.Fatalf("bounds error=%v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	api = NewPosts(postTransportFunc(func(ctx context.Context, _ string, _ any) error { return ctx.Err() }))
	if _, err := api.ReactionState(ctx, "post", "channel", "user", "eyes"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error=%v", err)
	}

	api = NewPosts(postTransportFunc(func(_ context.Context, _ string, out any) error { return json.Unmarshal([]byte(`[]`), out) }))
	var wg sync.WaitGroup
	for range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = api.ReactionState(context.Background(), "post", "channel", "user", "eyes")
		}()
	}
	wg.Wait()
}

func FuzzReactionStateDecoder(f *testing.F) {
	f.Add([]byte(`[]`))
	f.Add([]byte(`[` + validReactionRow + `]`))
	f.Fuzz(func(t *testing.T, payload []byte) {
		api := NewPosts(postTransportFunc(func(_ context.Context, _ string, out any) error { return json.Unmarshal(payload, out) }))
		_, _ = api.ReactionState(context.Background(), "post", "channel", "user", "Eyes")
	})
}

func TestOrderedPostsPageNormalizesAndSuppressesDeleted(t *testing.T) {
	var page OrderedPostsPage
	err := json.Unmarshal([]byte(`{
		"order":["live",7,"missing","deleted","mismatch"],
		"posts":{
			"live":{"id":"live","channel_id":"chan","message":"ok","create_at":3,"delete_at":0},
			"deleted":{"id":"deleted","channel_id":"chan","message":"secret stale text","create_at":2,"delete_at":9},
			"mismatch":{"id":"other","channel_id":"chan","message":"bad","create_at":1,"delete_at":0}
		},
		"has_next":false,"first_inaccessible_post_time":4
	}`), &page)
	if err != nil {
		t.Fatal(err)
	}
	if page.RawCount != 5 || len(page.Posts) != 1 || page.Posts[0].ID != "live" || !page.Incomplete {
		t.Fatalf("page = %#v", page)
	}
	if page.HasNext == nil || *page.HasNext || page.FirstInaccessiblePostTime == nil || *page.FirstInaccessiblePostTime != 4 {
		t.Fatalf("metadata = %#v", page)
	}
}

func TestOrderedPostsPageRejectsInvalidEnvelope(t *testing.T) {
	for _, payload := range []string{`null`, `{}`, `{"order":null,"posts":{}}`, `{"order":{},"posts":{}}`} {
		var page OrderedPostsPage
		if !errors.Is(json.Unmarshal([]byte(payload), &page), ErrInvalidPostsResponse) {
			t.Fatalf("payload %s accepted", payload)
		}
	}
}

func TestOrderedPostsPageMarksMalformedMetadataIncomplete(t *testing.T) {
	var page OrderedPostsPage
	if err := json.Unmarshal([]byte(`{"order":[],"posts":{},"has_next":"yes","first_inaccessible_post_time":-1}`), &page); err != nil {
		t.Fatal(err)
	}
	if !page.Incomplete || page.HasNext != nil || page.FirstInaccessiblePostTime != nil {
		t.Fatalf("page = %#v", page)
	}
}

func TestOrderedPostsPageTreatsExplicitNullHasNextAsIncomplete(t *testing.T) {
	var page OrderedPostsPage
	if err := json.Unmarshal([]byte(`{"order":[],"posts":{},"has_next":null}`), &page); err != nil {
		t.Fatal(err)
	}
	if !page.Incomplete || page.HasNext != nil {
		t.Fatalf("page = %#v", page)
	}
}

func TestOrderedPostsPageRejectsNullDeleteTimestampWithoutEmittingStalePost(t *testing.T) {
	var page OrderedPostsPage
	if err := json.Unmarshal([]byte(`{"order":["stale"],"posts":{"stale":{"id":"stale","channel_id":"channel","message":"secret stale text","create_at":1,"delete_at":null}}}`), &page); err != nil {
		t.Fatal(err)
	}
	if !page.Incomplete || page.RawCount != 1 || len(page.Posts) != 0 || page.Continuation != nil {
		t.Fatalf("page = %#v", page)
	}
}

func TestSearchPageBuildsExactReadPOSTAndNormalizesEnvelope(t *testing.T) {
	var gotPath string
	var gotBody []byte
	api := NewPosts(searchTransportFunc(func(_ context.Context, path string, body, out any) error {
		gotPath = path
		gotBody, _ = json.Marshal(body)
		return json.Unmarshal([]byte(`{"order":["live","live","missing","gone"],"posts":{"live":{"id":"live","channel_id":"chan","message":"ok","create_at":2,"delete_at":0},"gone":{"id":"gone","channel_id":"chan","message":"stale","create_at":1,"delete_at":3}},"matches":{"live":["needle",7]},"has_next":true}`), out)
	}))
	page, err := api.SearchPage(context.Background(), "team/id", SearchPageOptions{Terms: "needle", Page: 2, PerPage: 17})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/teams/team%2Fid/posts/search" || string(gotBody) != `{"terms":"needle","is_or_search":false,"page":2,"per_page":17}` {
		t.Fatalf("path=%q body=%s", gotPath, gotBody)
	}
	if page.RawCount != 4 || len(page.OrderedIDs) != 3 || len(page.Posts) != 1 || page.Posts[0].ID != "live" || !page.Incomplete || !reflect.DeepEqual(page.Matches["live"], []string{"needle"}) {
		t.Fatalf("page=%#v", page)
	}
}

func TestSearchPageRejectsMalformedEnvelopeAndInvalidRequest(t *testing.T) {
	api := NewPosts(searchTransportFunc(func(_ context.Context, _ string, _ any, out any) error {
		return json.Unmarshal([]byte(`null`), out)
	}))
	if _, err := api.SearchPage(context.Background(), "team", SearchPageOptions{Terms: "x", PerPage: 1}); !errors.Is(err, ErrInvalidSearchResponse) {
		t.Fatalf("malformed error=%v", err)
	}
	for _, options := range []SearchPageOptions{{Terms: "", PerPage: 1}, {Terms: "x", PerPage: 0}, {Terms: "x", PerPage: 101}, {Terms: "x", Page: -1, PerPage: 1}} {
		if _, err := api.SearchPage(context.Background(), "team", options); !errors.Is(err, ErrInvalidPostsRequest) {
			t.Fatalf("options=%#v error=%v", options, err)
		}
	}
}

func TestChannelPageBuildsBoundedGETAndChecksBinding(t *testing.T) {
	var gotPath string
	api := NewPosts(postTransportFunc(func(_ context.Context, path string, out any) error {
		gotPath = path
		return json.Unmarshal([]byte(`{"order":["p"],"posts":{"p":{"id":"p","channel_id":"channel/id","message":"","create_at":1,"delete_at":0}}}`), out)
	}))
	page, err := api.ChannelPage(context.Background(), "channel/id", ChannelPostsOptions{PerPage: 17, Page: 2, Before: "anchor/id"})
	if err != nil || len(page.Posts) != 1 {
		t.Fatalf("page = %#v, err = %v", page, err)
	}
	for _, part := range []string{"/channels/channel%2Fid/posts?", "before=anchor%2Fid", "page=2", "per_page=17", "skipFetchThreads=true"} {
		if !strings.Contains(gotPath, part) {
			t.Fatalf("path %q missing %q", gotPath, part)
		}
	}
}

func TestChannelPageRejectsInvalidRequestAndCrossChannelPost(t *testing.T) {
	api := NewPosts(postTransportFunc(func(_ context.Context, _ string, out any) error {
		return json.Unmarshal([]byte(`{"order":["p"],"posts":{"p":{"id":"p","channel_id":"other","message":"x","create_at":1,"delete_at":0}}}`), out)
	}))
	for _, options := range []ChannelPostsOptions{{PerPage: 0}, {PerPage: 201}, {PerPage: 1, Page: -1}} {
		if _, err := api.ChannelPage(context.Background(), "channel", options); !errors.Is(err, ErrInvalidPostsRequest) {
			t.Fatalf("options %#v: %v", options, err)
		}
	}
	if _, err := api.ChannelPage(context.Background(), "channel", ChannelPostsOptions{PerPage: 1}); !errors.Is(err, ErrInvalidPostsResponse) {
		t.Fatalf("cross-channel error = %v", err)
	}
}

func TestChannelPageRejectsDeletedCrossChannelPost(t *testing.T) {
	api := NewPosts(postTransportFunc(func(_ context.Context, _ string, out any) error {
		return json.Unmarshal([]byte(`{"order":["p"],"posts":{"p":{"id":"p","channel_id":"other","message":"stale","create_at":1,"delete_at":2}}}`), out)
	}))
	if _, err := api.ChannelPage(context.Background(), "channel", ChannelPostsOptions{PerPage: 1}); !errors.Is(err, ErrInvalidPostsResponse) {
		t.Fatalf("error = %v", err)
	}
}

func TestThreadPageBuildsMattermostCursorAndChecksDeletedBindings(t *testing.T) {
	var gotPath string
	api := NewPosts(postTransportFunc(func(_ context.Context, path string, out any) error {
		gotPath = path
		return json.Unmarshal([]byte(`{"order":["root","gone"],"posts":{"root":{"id":"root","channel_id":"chan","message":"root","create_at":1,"delete_at":0,"root_id":"","reply_count":1},"gone":{"id":"gone","channel_id":"chan","message":"stale","create_at":2,"delete_at":3,"root_id":"other","reply_count":0}},"has_next":true}`), out)
	}))
	fromAt := int64(7)
	_, err := api.ThreadPage(context.Background(), "root", ThreadPageOptions{PerPage: 200, FromPost: "cursor", FromCreateAt: &fromAt})
	if !errors.Is(err, ErrInvalidPostsResponse) {
		t.Fatalf("error = %v", err)
	}
	for _, part := range []string{"/posts/root/thread?", "direction=down", "fromCreateAt=7", "fromPost=cursor", "perPage=200"} {
		if !strings.Contains(gotPath, part) {
			t.Fatalf("path %q missing %q", gotPath, part)
		}
	}
}

func TestThreadPageContinuationUsesLastValidOrderedDeletedPost(t *testing.T) {
	api := NewPosts(postTransportFunc(func(_ context.Context, _ string, out any) error {
		return json.Unmarshal([]byte(`{"order":["root","gone"],"posts":{"root":{"id":"root","channel_id":"chan","message":"root","create_at":1,"delete_at":0,"root_id":"","reply_count":1},"gone":{"id":"gone","channel_id":"chan","message":"stale","create_at":2,"delete_at":3,"root_id":"root","reply_count":0}},"has_next":true}`), out)
	}))
	page, err := api.ThreadPage(context.Background(), "root", ThreadPageOptions{PerPage: 200})
	if err != nil || len(page.Posts) != 1 || page.Continuation == nil || page.Continuation.PostID != "gone" || page.Continuation.CreateAt != 2 {
		t.Fatalf("page=%#v error=%v", page, err)
	}
}

func TestThreadPageAcceptsReplyIDAndInfersCanonicalIdentity(t *testing.T) {
	api := NewPosts(postTransportFunc(func(_ context.Context, _ string, out any) error {
		return json.Unmarshal([]byte(`{"order":["root","reply"],"posts":{"root":{"id":"root","channel_id":"chan","message":"root","create_at":1,"delete_at":0,"root_id":"","reply_count":1},"reply":{"id":"reply","channel_id":"chan","message":"reply","create_at":2,"delete_at":0,"root_id":"root","reply_count":0}},"has_next":false}`), out)
	}))
	page, err := api.ThreadPage(context.Background(), "reply", ThreadPageOptions{PerPage: 200})
	if err != nil || page.ThreadRootID != "root" || page.ThreadChannelID != "chan" || !page.ContainsRequestedPost {
		t.Fatalf("page=%#v error=%v", page, err)
	}
}

func TestThreadPageRejectsCrossChannelCandidates(t *testing.T) {
	api := NewPosts(postTransportFunc(func(_ context.Context, _ string, out any) error {
		return json.Unmarshal([]byte(`{"order":["root","reply"],"posts":{"root":{"id":"root","channel_id":"one","message":"root","create_at":1,"delete_at":0,"root_id":"","reply_count":1},"reply":{"id":"reply","channel_id":"two","message":"reply","create_at":2,"delete_at":3,"root_id":"root","reply_count":0}}}`), out)
	}))
	if _, err := api.ThreadPage(context.Background(), "root", ThreadPageOptions{PerPage: 200}); !errors.Is(err, ErrInvalidPostsResponse) {
		t.Fatalf("error = %v", err)
	}
}

func TestThreadPageMarksMissingThreadShapeIncomplete(t *testing.T) {
	api := NewPosts(postTransportFunc(func(_ context.Context, _ string, out any) error {
		return json.Unmarshal([]byte(`{"order":["root"],"posts":{"root":{"id":"root","channel_id":"chan","message":"root","create_at":1,"delete_at":0}},"has_next":false}`), out)
	}))
	page, err := api.ThreadPage(context.Background(), "root", ThreadPageOptions{PerPage: 200})
	if err != nil || !page.Incomplete || page.Posts[0].ThreadShapeKnown {
		t.Fatalf("page=%#v error=%v", page, err)
	}
}

func TestPostRejectsValuesOutsideDeterministicDomain(t *testing.T) {
	for _, payload := range []string{
		`{"id":"nonascii-é","channel_id":"channel","message":"x","create_at":1,"delete_at":0}`,
		`{"id":"slash/id","channel_id":"channel","message":"x","create_at":1,"delete_at":0}`,
		`{"id":"ok","channel_id":"channel","message":"x","create_at":8640000000000001,"delete_at":0}`,
		`{"id":"ok","channel_id":"channel","message":"x","create_at":1,"delete_at":8640000000000001}`,
	} {
		var post Post
		if !errors.Is(json.Unmarshal([]byte(payload), &post), ErrInvalidPostResponse) {
			t.Fatalf("payload %s accepted", payload)
		}
	}
}

func FuzzOrderedPostsPageNeverPanics(f *testing.F) {
	f.Add([]byte(`{"order":[],"posts":{}}`))
	f.Add([]byte(`{"order":["x"],"posts":null}`))
	f.Fuzz(func(t *testing.T, payload []byte) {
		var page OrderedPostsPage
		_ = json.Unmarshal(payload, &page)
	})
}
