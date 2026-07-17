package retrieval

import (
	"context"
	"errors"
	"strings"

	"github.com/ardasevinc/mattermost-cli/v2/internal/mattermost"
)

var ErrInvalidDMHistoryRequest = errors.New("invalid direct-message history request")

const MaxDMHistoryRequests = 100

type DMHistoryOptions struct {
	Limit            int
	Since            *int64
	Boundary         *Boundary
	SafeBeforePostID string
}

type DMHistoryResult struct {
	Posts           []mattermost.Post
	Completeness    Completeness
	SafeBeforeValid bool
}

// DMHistory retrieves each selected direct channel independently, then caps
// the merged known candidates. Completeness is unknown if any channel could
// not be queried completely, because a global top-N cannot then be proven.
func DMHistory(ctx context.Context, source channelPageSource, channelIDs []string, options DMHistoryOptions) (DMHistoryResult, error) {
	if source == nil || len(channelIDs) == 0 || options.Limit <= 0 || int64(options.Limit) > maxSafeInteger ||
		(options.Boundary != nil && len(channelIDs) != 1) {
		return DMHistoryResult{}, ErrInvalidDMHistoryRequest
	}
	seenChannels := make(map[string]struct{}, len(channelIDs))
	all := make(map[string]mattermost.Post)
	unknown, truncated := false, false
	safeBeforeValid := true
	requestBudget := MaxDMHistoryRequests
	for _, channelID := range channelIDs {
		if err := ctx.Err(); err != nil {
			return DMHistoryResult{}, err
		}
		if strings.TrimSpace(channelID) == "" {
			return DMHistoryResult{}, ErrInvalidDMHistoryRequest
		}
		if _, duplicate := seenChannels[channelID]; duplicate {
			return DMHistoryResult{}, ErrInvalidDMHistoryRequest
		}
		seenChannels[channelID] = struct{}{}
		page, err := ChannelHistory(ctx, source, channelID, ChannelHistoryOptions{
			Limit: options.Limit, Since: options.Since, Boundary: options.Boundary, SafeBeforePostID: options.SafeBeforePostID,
			RequestBudget: &requestBudget,
		})
		if err != nil {
			return DMHistoryResult{}, err
		}
		if !page.SafeBeforeValid {
			safeBeforeValid = false
		}
		unknown = unknown || page.Completeness == CompletenessUnknown
		truncated = truncated || page.Completeness == CompletenessTruncated
		for _, post := range page.Posts {
			if post.ChannelID != channelID {
				return DMHistoryResult{}, ErrInvalidDMHistoryRequest
			}
			if _, duplicate := all[post.ID]; duplicate {
				return DMHistoryResult{}, ErrInvalidDMHistoryRequest
			}
			all[post.ID] = post
		}
	}
	completeness := CompletenessComplete
	if unknown {
		completeness = CompletenessUnknown
	} else if truncated || len(all) > options.Limit {
		completeness = CompletenessTruncated
	}
	return DMHistoryResult{Posts: mostRecent(all, options.Limit), Completeness: completeness, SafeBeforeValid: safeBeforeValid}, nil
}
