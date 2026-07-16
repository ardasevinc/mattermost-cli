package retrieval

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/ardasevinc/mattermost-cli/internal/mattermost"
)

type pageSourceFunc func(context.Context, string, mattermost.ChannelPostsOptions) (mattermost.OrderedPostsPage, error)

func (f pageSourceFunc) ChannelPage(ctx context.Context, channelID string, options mattermost.ChannelPostsOptions) (mattermost.OrderedPostsPage, error) {
	return f(ctx, channelID, options)
}

func testPost(id string, at int64) mattermost.Post {
	return mattermost.Post{ID: id, ChannelID: "channel", Message: id, CreateAt: at}
}

func testPage(posts ...mattermost.Post) mattermost.OrderedPostsPage {
	return mattermost.OrderedPostsPage{Posts: posts, RawCount: len(posts)}
}

func TestChannelHistoryUsesLimitPlusOneAndDeterministicOrder(t *testing.T) {
	var requested mattermost.ChannelPostsOptions
	result, err := ChannelHistory(context.Background(), pageSourceFunc(func(_ context.Context, _ string, options mattermost.ChannelPostsOptions) (mattermost.OrderedPostsPage, error) {
		requested = options
		return testPage(testPost("b", 3), testPost("a", 3), testPost("older", 2)), nil
	}), "channel", ChannelHistoryOptions{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if requested.PerPage != 3 || result.Completeness != CompletenessUnknown || fmt.Sprint(ids(result.Posts)) != "[a b]" {
		t.Fatalf("requested=%#v result=%#v", requested, result)
	}
}

func TestChannelHistoryCompletesEqualMillisecondTiesAndBoundary(t *testing.T) {
	pages := []mattermost.OrderedPostsPage{
		testPage(testPost("newer", 300), testPost("anchor", 200), testPost("c", 200)),
		testPage(testPost("b", 200), testPost("a", 200), testPost("older", 100)),
	}
	result, err := ChannelHistory(context.Background(), indexedPages(t, pages), "channel", ChannelHistoryOptions{
		Limit: 2, Boundary: &Boundary{CreateAt: 200, ID: "anchor"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(ids(result.Posts)) != "[b c]" || result.Completeness != CompletenessUnknown {
		t.Fatalf("result = %#v", result)
	}
}

func TestChannelHistoryAppliesExactLocalSinceWithoutSendingIt(t *testing.T) {
	since := int64(100)
	pages := []mattermost.OrderedPostsPage{
		testPage(testPost("new", 101), testPost("edge", 100), testPost("old", 99)),
	}
	result, err := ChannelHistory(context.Background(), indexedPages(t, pages), "channel", ChannelHistoryOptions{Limit: 4, Since: &since})
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(ids(result.Posts)) != "[new edge]" || result.Completeness != CompletenessComplete {
		t.Fatalf("result = %#v", result)
	}
}

func TestChannelHistorySafeBeforeFallbackPreservesBoundary(t *testing.T) {
	var requests []mattermost.ChannelPostsOptions
	result, err := ChannelHistory(context.Background(), pageSourceFunc(func(_ context.Context, _ string, options mattermost.ChannelPostsOptions) (mattermost.OrderedPostsPage, error) {
		requests = append(requests, options)
		if options.Before != "" {
			return mattermost.OrderedPostsPage{RawCount: 0}, nil
		}
		return testPage(testPost("peer", 200), testPost("older", 100)), nil
	}), "channel", ChannelHistoryOptions{Limit: 2, Boundary: &Boundary{CreateAt: 200, ID: "anchor"}, SafeBeforePostID: "gone"})
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 || requests[0].Page != 0 || requests[1].Page != 0 || requests[1].Before != "" {
		t.Fatalf("requests = %#v", requests)
	}
	if result.SafeBeforeValid || result.Completeness != CompletenessComplete || fmt.Sprint(ids(result.Posts)) != "[peer older]" {
		t.Fatalf("result = %#v", result)
	}
}

func TestChannelHistorySafeBeforeRemainsOnDeepPages(t *testing.T) {
	pages := []mattermost.OrderedPostsPage{
		testPage(testPost("newer", 300), testPost("anchor", 200), testPost("c", 200)),
		testPage(testPost("b", 200), testPost("older", 100)),
	}
	index := 0
	result, err := ChannelHistory(context.Background(), pageSourceFunc(func(_ context.Context, _ string, options mattermost.ChannelPostsOptions) (mattermost.OrderedPostsPage, error) {
		if options.Before != "safe" || options.Page != index {
			t.Fatalf("options = %#v, index = %d", options, index)
		}
		page := pages[index]
		index++
		return page, nil
	}), "channel", ChannelHistoryOptions{Limit: 2, Boundary: &Boundary{CreateAt: 200, ID: "anchor"}, SafeBeforePostID: "safe"})
	if err != nil || !result.SafeBeforeValid || fmt.Sprint(ids(result.Posts)) != "[b c]" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestChannelHistoryUnknownOnTwoStagnantFullPages(t *testing.T) {
	calls := 0
	result, err := ChannelHistory(context.Background(), pageSourceFunc(func(_ context.Context, _ string, options mattermost.ChannelPostsOptions) (mattermost.OrderedPostsPage, error) {
		calls++
		return mattermost.OrderedPostsPage{Posts: []mattermost.Post{testPost("a", 2), testPost("b", 1), testPost("a", 2), testPost("b", 1)}, RawCount: options.PerPage}, nil
	}), "channel", ChannelHistoryOptions{Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 3 || result.Completeness != CompletenessUnknown {
		t.Fatalf("calls=%d result=%#v", calls, result)
	}
}

func TestChannelHistoryIncompleteDeletedPageContinuesButCannotClaimComplete(t *testing.T) {
	pages := []mattermost.OrderedPostsPage{
		{RawCount: 3, Incomplete: true},
		{Posts: []mattermost.Post{testPost("live", 2)}, RawCount: 1},
	}
	result, err := ChannelHistory(context.Background(), indexedPages(t, pages), "channel", ChannelHistoryOptions{Limit: 2})
	if err != nil || fmt.Sprint(ids(result.Posts)) != "[live]" || result.Completeness != CompletenessUnknown {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestChannelHistoryEmptyHasNextAndInaccessibleAreUnknown(t *testing.T) {
	truth := true
	inaccessible := int64(1)
	for name, page := range map[string]mattermost.OrderedPostsPage{
		"has next":     {HasNext: &truth},
		"inaccessible": {Posts: []mattermost.Post{testPost("x", 1)}, RawCount: 1, FirstInaccessiblePostTime: &inaccessible},
	} {
		t.Run(name, func(t *testing.T) {
			result, err := ChannelHistory(context.Background(), pageSourceFunc(func(context.Context, string, mattermost.ChannelPostsOptions) (mattermost.OrderedPostsPage, error) {
				return page, nil
			}), "channel", ChannelHistoryOptions{Limit: 2})
			if err != nil || result.Completeness != CompletenessUnknown {
				t.Fatalf("result=%#v err=%v", result, err)
			}
		})
	}
}

func TestChannelHistoryStopsAtHardPageBoundAsUnknown(t *testing.T) {
	calls := 0
	result, err := ChannelHistory(context.Background(), pageSourceFunc(func(_ context.Context, _ string, options mattermost.ChannelPostsOptions) (mattermost.OrderedPostsPage, error) {
		calls++
		posts := make([]mattermost.Post, options.PerPage)
		for i := range posts {
			posts[i] = testPost(fmt.Sprintf("p%03d_%03d", options.Page, i), 300)
		}
		return mattermost.OrderedPostsPage{Posts: posts, RawCount: options.PerPage}, nil
	}), "channel", ChannelHistoryOptions{Limit: 200, Boundary: &Boundary{CreateAt: 200, ID: "anchor"}})
	if err != nil || calls != MaxChannelHistoryPages || result.Completeness != CompletenessUnknown || len(result.Posts) != 0 {
		t.Fatalf("calls=%d result=%#v error=%v", calls, result, err)
	}
}

func TestChannelHistoryBudgetExhaustionDominatesOverLimitSameTimestampCandidates(t *testing.T) {
	budget := 1
	result, err := ChannelHistory(context.Background(), pageSourceFunc(func(_ context.Context, _ string, options mattermost.ChannelPostsOptions) (mattermost.OrderedPostsPage, error) {
		return mattermost.OrderedPostsPage{
			Posts:    []mattermost.Post{testPost("a", 200), testPost("b", 200), testPost("c", 200)},
			RawCount: options.PerPage,
		}, nil
	}), "channel", ChannelHistoryOptions{Limit: 2, RequestBudget: &budget})
	if err != nil || result.Completeness != CompletenessUnknown || fmt.Sprint(ids(result.Posts)) != "[a b]" || budget != 0 {
		t.Fatalf("budget=%d result=%#v err=%v", budget, result, err)
	}
}

func TestChannelHistoryRejectsValuesOutsideCursorDomain(t *testing.T) {
	source := pageSourceFunc(func(context.Context, string, mattermost.ChannelPostsOptions) (mattermost.OrderedPostsPage, error) {
		t.Fatal("source called for invalid request")
		return mattermost.OrderedPostsPage{}, nil
	})
	tooLate := int64(8_640_000_000_000_001)
	for name, options := range map[string]ChannelHistoryOptions{
		"since":       {Limit: 1, Since: &tooLate},
		"boundary id": {Limit: 1, Boundary: &Boundary{CreateAt: 1, ID: "bad/é"}},
		"boundary at": {Limit: 1, Boundary: &Boundary{CreateAt: tooLate, ID: "ok"}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ChannelHistory(context.Background(), source, "channel", options); !errors.Is(err, ErrInvalidChannelHistoryRequest) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func indexedPages(t *testing.T, pages []mattermost.OrderedPostsPage) pageSourceFunc {
	t.Helper()
	return func(_ context.Context, _ string, options mattermost.ChannelPostsOptions) (mattermost.OrderedPostsPage, error) {
		if options.Page >= len(pages) {
			t.Fatalf("unexpected page %d", options.Page)
		}
		return pages[options.Page], nil
	}
}

func ids(posts []mattermost.Post) []string {
	result := make([]string, len(posts))
	for i := range posts {
		result[i] = posts[i].ID
	}
	return result
}
