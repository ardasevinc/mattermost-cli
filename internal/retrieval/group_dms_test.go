package retrieval

import (
	"context"
	"fmt"
	"testing"

	"github.com/ardasevinc/mattermost-cli/internal/mattermost"
)

func TestGroupDMHistoryUsesGlobalDeterministicCapAndUnknownDominance(t *testing.T) {
	channels := make([]string, MaxDMHistoryRequests+1)
	for index := range channels {
		channels[index] = fmt.Sprintf("group-%03d", index)
	}
	calls := 0
	result, err := GroupDMHistory(context.Background(), pageSourceFunc(func(_ context.Context, channelID string, _ mattermost.ChannelPostsOptions) (mattermost.OrderedPostsPage, error) {
		calls++
		if calls == 1 {
			posts := []mattermost.Post{{ID: "new", ChannelID: channelID, CreateAt: 2}, {ID: "old", ChannelID: channelID, CreateAt: 1}}
			return mattermost.OrderedPostsPage{Posts: posts, RawCount: len(posts), HasNext: dmBoolPointer(false)}, nil
		}
		return mattermost.OrderedPostsPage{HasNext: dmBoolPointer(true)}, nil
	}), channels, GroupDMHistoryOptions{Limit: 1})
	if err != nil || calls != MaxDMHistoryRequests || result.Completeness != CompletenessUnknown || fmt.Sprint(ids(result.Posts)) != "[new]" {
		t.Fatalf("calls=%d result=%#v err=%v", calls, result, err)
	}
}
