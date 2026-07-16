package retrieval

import "context"

// GroupDMHistoryOptions intentionally shares the conversation-history
// contract with DMs: one command-wide request budget and one global seed cap.
type GroupDMHistoryOptions = DMHistoryOptions

type GroupDMHistoryResult = DMHistoryResult

func GroupDMHistory(ctx context.Context, source channelPageSource, channelIDs []string, options GroupDMHistoryOptions) (GroupDMHistoryResult, error) {
	return DMHistory(ctx, source, channelIDs, options)
}
