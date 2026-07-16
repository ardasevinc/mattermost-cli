package mattermost

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type postTransportFunc func(context.Context, string, any) error

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
