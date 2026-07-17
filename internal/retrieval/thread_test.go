package retrieval

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ardasevinc/mattermost-cli/v2/internal/mattermost"
)

type threadSourceFunc func(context.Context, string, mattermost.ThreadPageOptions) (mattermost.OrderedPostsPage, error)

func (f threadSourceFunc) ThreadPage(ctx context.Context, rootID string, options mattermost.ThreadPageOptions) (mattermost.OrderedPostsPage, error) {
	return f(ctx, rootID, options)
}

func threadPost(id, rootID string, at int64, replies int) mattermost.Post {
	return mattermost.Post{ID: id, RootID: rootID, ChannelID: "channel", Message: id, CreateAt: at, ReplyCount: replies, ThreadShapeKnown: true}
}

func boolPointer(value bool) *bool { return &value }

func TestThreadPaginatesInResponseOrderAndDeduplicates(t *testing.T) {
	root := threadPost("root", "", 1, 2)
	reply1 := threadPost("reply-1", "root", 2, 0)
	reply2 := threadPost("reply-2", "root", 3, 0)
	var requests []mattermost.ThreadPageOptions
	result, err := Thread(context.Background(), threadSourceFunc(func(_ context.Context, _ string, options mattermost.ThreadPageOptions) (mattermost.OrderedPostsPage, error) {
		requests = append(requests, options)
		if len(requests) == 1 {
			return mattermost.OrderedPostsPage{Posts: []mattermost.Post{root, reply1}, RawCount: 2, HasNext: boolPointer(true), Continuation: &mattermost.ThreadCursor{PostID: reply1.ID, CreateAt: reply1.CreateAt}}, nil
		}
		return mattermost.OrderedPostsPage{Posts: []mattermost.Post{root, reply1, reply2}, RawCount: 3, HasNext: boolPointer(false)}, nil
	}), "root")
	if err != nil || result.Completeness != CompletenessComplete || fmt.Sprint(ids(result.Posts)) != "[root reply-1 reply-2]" {
		t.Fatalf("result=%#v error=%v", result, err)
	}
	if len(requests) != 2 || requests[1].FromPost != "reply-1" || requests[1].FromCreateAt == nil || *requests[1].FromCreateAt != 2 {
		t.Fatalf("requests = %#v", requests)
	}
}

func TestThreadRetainsLaterPageContentAsUnknown(t *testing.T) {
	root := threadPost("root", "", 1, 2)
	reply := threadPost("reply", "root", 2, 0)
	calls := 0
	result, err := Thread(context.Background(), threadSourceFunc(func(context.Context, string, mattermost.ThreadPageOptions) (mattermost.OrderedPostsPage, error) {
		calls++
		if calls == 2 {
			return mattermost.OrderedPostsPage{}, errors.New("later page failed")
		}
		return mattermost.OrderedPostsPage{Posts: []mattermost.Post{root, reply}, HasNext: boolPointer(true), Continuation: &mattermost.ThreadCursor{PostID: reply.ID, CreateAt: reply.CreateAt}}, nil
	}), "root")
	if err != nil || result.Completeness != CompletenessUnknown || fmt.Sprint(ids(result.Posts)) != "[root reply]" {
		t.Fatalf("result=%#v error=%v", result, err)
	}
}

func TestThreadLegacyCompletionAndStagnation(t *testing.T) {
	root := threadPost("root", "", 1, 1)
	reply := threadPost("reply", "root", 2, 0)
	complete, err := Thread(context.Background(), threadSourceFunc(func(context.Context, string, mattermost.ThreadPageOptions) (mattermost.OrderedPostsPage, error) {
		return mattermost.OrderedPostsPage{Posts: []mattermost.Post{root, reply}}, nil
	}), "root")
	if err != nil || complete.Completeness != CompletenessComplete {
		t.Fatalf("complete=%#v error=%v", complete, err)
	}
	calls := 0
	partial, err := Thread(context.Background(), threadSourceFunc(func(context.Context, string, mattermost.ThreadPageOptions) (mattermost.OrderedPostsPage, error) {
		calls++
		return mattermost.OrderedPostsPage{Posts: []mattermost.Post{root}, HasNext: boolPointer(true), Continuation: &mattermost.ThreadCursor{PostID: root.ID, CreateAt: root.CreateAt}}, nil
	}), "root")
	if err != nil || calls != 3 || partial.Completeness != CompletenessTruncated {
		t.Fatalf("calls=%d partial=%#v error=%v", calls, partial, err)
	}
}

func TestHydrateVisibleThreadsReusesCompleteRootsAndBoundsConcurrency(t *testing.T) {
	seeds := []mattermost.Post{threadPost("complete", "", 1, 1), threadPost("complete-reply", "complete", 2, 0)}
	for index := range 9 {
		seeds = append(seeds, threadPost(fmt.Sprintf("seed-%d", index), fmt.Sprintf("root-%d", index), int64(index+10), 0))
	}
	var mu sync.Mutex
	active, maximum, calls := 0, 0, 0
	source := threadSourceFunc(func(_ context.Context, rootID string, _ mattermost.ThreadPageOptions) (mattermost.OrderedPostsPage, error) {
		mu.Lock()
		active++
		calls++
		if active > maximum {
			maximum = active
		}
		mu.Unlock()
		time.Sleep(time.Millisecond)
		mu.Lock()
		active--
		mu.Unlock()
		root := threadPost(rootID, "", 1, 1)
		return mattermost.OrderedPostsPage{Posts: []mattermost.Post{root}, HasNext: boolPointer(false)}, nil
	})
	result, err := HydrateVisibleThreads(context.Background(), source, seeds, true)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 9 || maximum != 4 || result.VisibleThreads.Status != VisibleThreadsComplete || result.VisibleThreads.HydratedRootCount != 10 {
		t.Fatalf("calls=%d maximum=%d metadata=%#v", calls, maximum, result.VisibleThreads)
	}
}

func TestHydrationFailureMetadataFollowsSeedOrderAndRetainsPartialPosts(t *testing.T) {
	seeds := []mattermost.Post{threadPost("seed-a", "root-a", 3, 0), threadPost("seed-b", "root-b", 4, 0)}
	source := threadSourceFunc(func(_ context.Context, rootID string, options mattermost.ThreadPageOptions) (mattermost.OrderedPostsPage, error) {
		if rootID == "root-b" {
			return mattermost.OrderedPostsPage{}, errors.New("failed")
		}
		if options.FromPost != "" {
			return mattermost.OrderedPostsPage{}, errors.New("later failed")
		}
		root := threadPost(rootID, "", 1, 2)
		contextPost := threadPost("context-a", rootID, 2, 0)
		return mattermost.OrderedPostsPage{Posts: []mattermost.Post{root, contextPost}, HasNext: boolPointer(true), Continuation: &mattermost.ThreadCursor{PostID: contextPost.ID, CreateAt: 2}}, nil
	})
	result, err := HydrateVisibleThreads(context.Background(), source, seeds, true)
	if err != nil {
		t.Fatal(err)
	}
	if result.VisibleThreads.Status != VisibleThreadsPartial || result.VisibleThreads.HydratedRootCount != 0 || fmt.Sprint(result.VisibleThreads.FailedRootIDs) != "[root-a root-b]" || fmt.Sprint(ids(result.Posts)) != "[seed-a seed-b root-a context-a]" {
		t.Fatalf("result = %#v", result)
	}
}

func TestHydrationRejectsThreadContextFromAnotherSeedChannel(t *testing.T) {
	seed := threadPost("seed", "root", 3, 0)
	foreignRoot := threadPost("root", "", 1, 1)
	foreignReply := threadPost("foreign-reply", "root", 2, 0)
	foreignRoot.ChannelID = "other-channel"
	foreignReply.ChannelID = "other-channel"
	result, err := HydrateVisibleThreads(context.Background(), threadSourceFunc(func(context.Context, string, mattermost.ThreadPageOptions) (mattermost.OrderedPostsPage, error) {
		return mattermost.OrderedPostsPage{Posts: []mattermost.Post{foreignRoot, foreignReply}, HasNext: boolPointer(false)}, nil
	}), []mattermost.Post{seed}, true)
	if err != nil {
		t.Fatal(err)
	}
	if result.VisibleThreads.Status != VisibleThreadsPartial || fmt.Sprint(result.VisibleThreads.FailedRootIDs) != "[root]" || fmt.Sprint(ids(result.Posts)) != "[seed]" {
		t.Fatalf("foreign thread context was not rejected: %#v", result)
	}
}

func TestThreadMissingLegacyShapeCannotProveCompleteness(t *testing.T) {
	root := mattermost.Post{ID: "root", ChannelID: "channel", Message: "root", CreateAt: 1}
	result, err := Thread(context.Background(), threadSourceFunc(func(context.Context, string, mattermost.ThreadPageOptions) (mattermost.OrderedPostsPage, error) {
		return mattermost.OrderedPostsPage{Posts: []mattermost.Post{root}}, nil
	}), "root")
	if err != nil || result.Completeness != CompletenessUnknown {
		t.Fatalf("result=%#v error=%v", result, err)
	}
	hydrated, err := HydrateVisibleThreads(context.Background(), nil, []mattermost.Post{root}, true)
	if err != nil || hydrated.VisibleThreads.Status != VisibleThreadsPartial {
		t.Fatalf("hydrated=%#v error=%v", hydrated, err)
	}
}

func TestThreadReplyIDUsesInferredRootForLegacyCompletion(t *testing.T) {
	root := threadPost("root", "", 1, 1)
	reply := threadPost("reply", "root", 2, 0)
	result, err := Thread(context.Background(), threadSourceFunc(func(context.Context, string, mattermost.ThreadPageOptions) (mattermost.OrderedPostsPage, error) {
		return mattermost.OrderedPostsPage{
			Posts: []mattermost.Post{root, reply}, ThreadRootID: "root", ThreadChannelID: "channel", ContainsRequestedPost: true,
		}, nil
	}), "reply")
	if err != nil || result.Completeness != CompletenessComplete {
		t.Fatalf("result=%#v error=%v", result, err)
	}
}

func TestThreadRejectsCrossPageIdentityWithoutMergingForeignPosts(t *testing.T) {
	root := threadPost("root", "", 1, 1)
	foreign := threadPost("foreign", "root", 2, 0)
	foreign.ChannelID = "other"
	calls := 0
	result, err := Thread(context.Background(), threadSourceFunc(func(context.Context, string, mattermost.ThreadPageOptions) (mattermost.OrderedPostsPage, error) {
		calls++
		if calls == 1 {
			return mattermost.OrderedPostsPage{Posts: []mattermost.Post{root}, HasNext: boolPointer(true), Continuation: &mattermost.ThreadCursor{PostID: root.ID, CreateAt: root.CreateAt}, ThreadRootID: "root", ThreadChannelID: "channel", ContainsRequestedPost: true}, nil
		}
		return mattermost.OrderedPostsPage{Posts: []mattermost.Post{foreign}, HasNext: boolPointer(false), ThreadRootID: "root", ThreadChannelID: "other"}, nil
	}), "root")
	if err != nil || result.Completeness != CompletenessUnknown || fmt.Sprint(ids(result.Posts)) != "[root]" {
		t.Fatalf("result=%#v error=%v", result, err)
	}
}

func TestThreadAndHydrationPropagateCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	root := threadPost("root", "", 1, 1)
	calls := 0
	_, err := Thread(ctx, threadSourceFunc(func(ctx context.Context, _ string, _ mattermost.ThreadPageOptions) (mattermost.OrderedPostsPage, error) {
		calls++
		if calls == 1 {
			cancel()
			return mattermost.OrderedPostsPage{Posts: []mattermost.Post{root}, HasNext: boolPointer(true), Continuation: &mattermost.ThreadCursor{PostID: root.ID, CreateAt: root.CreateAt}}, nil
		}
		return mattermost.OrderedPostsPage{}, ctx.Err()
	}), "root")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("thread error = %v", err)
	}

	canceled, stop := context.WithCancel(context.Background())
	stop()
	if _, err := HydrateVisibleThreads(canceled, threadSourceFunc(func(context.Context, string, mattermost.ThreadPageOptions) (mattermost.OrderedPostsPage, error) {
		t.Fatal("source called after cancellation")
		return mattermost.OrderedPostsPage{}, nil
	}), []mattermost.Post{threadPost("seed", "root", 2, 0)}, true); !errors.Is(err, context.Canceled) {
		t.Fatalf("hydration error = %v", err)
	}
}
