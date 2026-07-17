package retrieval

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/ardasevinc/mattermost-cli/v2/internal/mattermost"
)

func TestDMHistoryAppliesOneGlobalDeterministicLimit(t *testing.T) {
	result, err := DMHistory(context.Background(), pageSourceFunc(func(_ context.Context, channelID string, _ mattermost.ChannelPostsOptions) (mattermost.OrderedPostsPage, error) {
		posts := map[string][]mattermost.Post{
			"alice": {{ID: "alice-new", ChannelID: "alice", CreateAt: 3}, {ID: "alice-old", ChannelID: "alice", CreateAt: 1}},
			"bob":   {{ID: "bob-new", ChannelID: "bob", CreateAt: 2}},
		}[channelID]
		return mattermost.OrderedPostsPage{Posts: posts, RawCount: len(posts), HasNext: dmBoolPointer(false)}, nil
	}), []string{"alice", "bob"}, DMHistoryOptions{Limit: 2})
	if err != nil || result.Completeness != CompletenessTruncated || fmt.Sprint(ids(result.Posts)) != "[alice-new bob-new]" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestDMHistoryPreservesUnknownEmpty(t *testing.T) {
	result, err := DMHistory(context.Background(), pageSourceFunc(func(context.Context, string, mattermost.ChannelPostsOptions) (mattermost.OrderedPostsPage, error) {
		truth := true
		return mattermost.OrderedPostsPage{HasNext: &truth}, nil
	}), []string{"dm"}, DMHistoryOptions{Limit: 2})
	if err != nil || len(result.Posts) != 0 || result.Completeness != CompletenessUnknown {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestDMHistorySharesOneCommandWideRequestBudget(t *testing.T) {
	channels := make([]string, MaxDMHistoryRequests+1)
	for index := range channels {
		channels[index] = fmt.Sprintf("dm-%03d", index)
	}
	calls := 0
	result, err := DMHistory(context.Background(), pageSourceFunc(func(_ context.Context, channelID string, _ mattermost.ChannelPostsOptions) (mattermost.OrderedPostsPage, error) {
		calls++
		post := mattermost.Post{ID: fmt.Sprintf("post-%03d", calls), ChannelID: channelID, CreateAt: int64(calls)}
		return mattermost.OrderedPostsPage{Posts: []mattermost.Post{post}, RawCount: 1, HasNext: dmBoolPointer(false)}, nil
	}), channels, DMHistoryOptions{Limit: 1})
	if err != nil || calls != MaxDMHistoryRequests || result.Completeness != CompletenessUnknown || len(result.Posts) != 1 {
		t.Fatalf("calls=%d result=%#v err=%v", calls, result, err)
	}
}

func TestDMHistoryUnknownDominatesEarlierTruncation(t *testing.T) {
	channels := make([]string, MaxDMHistoryRequests+1)
	for index := range channels {
		channels[index] = fmt.Sprintf("dm-%03d", index)
	}
	calls := 0
	result, err := DMHistory(context.Background(), pageSourceFunc(func(_ context.Context, channelID string, _ mattermost.ChannelPostsOptions) (mattermost.OrderedPostsPage, error) {
		calls++
		if calls == 1 {
			posts := []mattermost.Post{{ID: "new", ChannelID: channelID, CreateAt: 2}, {ID: "old", ChannelID: channelID, CreateAt: 1}}
			return mattermost.OrderedPostsPage{Posts: posts, RawCount: len(posts), HasNext: dmBoolPointer(false)}, nil
		}
		return mattermost.OrderedPostsPage{HasNext: dmBoolPointer(true)}, nil
	}), channels, DMHistoryOptions{Limit: 1})
	if err != nil || calls != MaxDMHistoryRequests || result.Completeness != CompletenessUnknown || fmt.Sprint(ids(result.Posts)) != "[new]" {
		t.Fatalf("calls=%d result=%#v err=%v", calls, result, err)
	}
}

func TestDMHistoryBusyFirstChannelExhaustsBudgetAndLeavesGlobalSelectionUnknown(t *testing.T) {
	calls := 0
	result, err := DMHistory(context.Background(), pageSourceFunc(func(_ context.Context, channelID string, options mattermost.ChannelPostsOptions) (mattermost.OrderedPostsPage, error) {
		calls++
		if channelID != "busy" {
			t.Fatalf("budget-exhausted channel %q was queried", channelID)
		}
		post := mattermost.Post{ID: fmt.Sprintf("post-%03d", calls), ChannelID: channelID, CreateAt: int64(calls)}
		return mattermost.OrderedPostsPage{Posts: []mattermost.Post{post}, RawCount: options.PerPage}, nil
	}), []string{"busy", "later"}, DMHistoryOptions{Limit: 1000})
	if err != nil || calls != MaxDMHistoryRequests || result.Completeness != CompletenessUnknown || len(result.Posts) != MaxDMHistoryRequests {
		t.Fatalf("calls=%d result=%#v err=%v", calls, result, err)
	}
}

func TestDMHistoryCancellationWinsBeforeBudgetShortCircuit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	_, err := DMHistory(ctx, pageSourceFunc(func(_ context.Context, channelID string, options mattermost.ChannelPostsOptions) (mattermost.OrderedPostsPage, error) {
		calls++
		if channelID != "busy" {
			t.Fatalf("canceled channel %q was queried", channelID)
		}
		if calls == MaxDMHistoryRequests {
			cancel()
		}
		post := mattermost.Post{ID: fmt.Sprintf("post-%03d", calls), ChannelID: channelID, CreateAt: int64(calls)}
		return mattermost.OrderedPostsPage{Posts: []mattermost.Post{post}, RawCount: options.PerPage}, nil
	}), []string{"busy", "later"}, DMHistoryOptions{Limit: 1000})
	if !errors.Is(err, context.Canceled) || calls != MaxDMHistoryRequests {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func dmBoolPointer(value bool) *bool { return &value }
