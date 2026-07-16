// Package retrieval implements bounded, deterministic read selection.
package retrieval

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/ardasevinc/mattermost-cli/internal/mattermost"
)

var ErrInvalidChannelHistoryRequest = errors.New("invalid channel history request")

const (
	MaxChannelHistoryPages = 100
	maxSafeInteger         = int64(9_007_199_254_740_991)
	maxDateMilliseconds    = int64(8_640_000_000_000_000)
)

type Completeness uint8

const (
	CompletenessUnknown Completeness = iota
	CompletenessComplete
	CompletenessTruncated
)

type Boundary struct {
	CreateAt int64
	ID       string
}

type ChannelHistoryOptions struct {
	Limit            int
	Since            *int64
	Boundary         *Boundary
	SafeBeforePostID string
	RequestBudget    *int
}

type ChannelHistoryResult struct {
	Posts           []mattermost.Post
	Completeness    Completeness
	SafeBeforeValid bool
}

type channelPageSource interface {
	ChannelPage(context.Context, string, mattermost.ChannelPostsOptions) (mattermost.OrderedPostsPage, error)
}

func ChannelHistory(ctx context.Context, source channelPageSource, channelID string, options ChannelHistoryOptions) (ChannelHistoryResult, error) {
	if source == nil || strings.TrimSpace(channelID) == "" || options.Limit <= 0 || int64(options.Limit) > maxSafeInteger ||
		(options.Since != nil && (*options.Since < 0 || *options.Since > maxDateMilliseconds)) ||
		(options.Boundary != nil && (options.Boundary.CreateAt <= 0 || options.Boundary.CreateAt > maxDateMilliseconds || !safeBoundaryID(options.Boundary.ID))) {
		return ChannelHistoryResult{}, ErrInvalidChannelHistoryRequest
	}
	target := options.Limit + 1
	pageSize := target
	if pageSize > mattermost.MaxPostsPage {
		pageSize = mattermost.MaxPostsPage
	}
	byID := make(map[string]mattermost.Post)
	seen := make(map[string]struct{})
	pageNumber, stagnantPages := 0, 0
	uncertain, exhausted := false, false
	activeBefore := options.SafeBeforePostID
	retriedWithoutAnchor := false

	for {
		if err := ctx.Err(); err != nil {
			return ChannelHistoryResult{}, err
		}
		if pageNumber >= MaxChannelHistoryPages {
			uncertain = true
			break
		}
		if options.RequestBudget != nil {
			if *options.RequestBudget <= 0 {
				uncertain = true
				break
			}
			*options.RequestBudget = *options.RequestBudget - 1
		}
		page, err := source.ChannelPage(ctx, channelID, mattermost.ChannelPostsOptions{
			PerPage: pageSize, Page: pageNumber, Before: activeBefore,
		})
		if err != nil {
			return ChannelHistoryResult{}, err
		}
		if page.Incomplete || page.FirstInaccessiblePostTime != nil {
			uncertain = true
		}
		if page.RawCount == 0 {
			if pageNumber == 0 && activeBefore != "" && !retriedWithoutAnchor {
				activeBefore = ""
				retriedWithoutAnchor = true
				stagnantPages = 0
				continue
			}
			if page.HasNext != nil && *page.HasNext {
				uncertain = true
			}
			exhausted = !uncertain
			break
		}

		madeProgress := false
		for _, post := range page.Posts {
			if _, duplicate := seen[post.ID]; duplicate {
				continue
			}
			seen[post.ID] = struct{}{}
			madeProgress = true
			if withinSelection(post, options) {
				byID[post.ID] = post
			}
		}
		pageNumber++
		if madeProgress {
			stagnantPages = 0
		} else {
			stagnantPages++
		}

		if options.Since != nil && len(page.Posts) > 0 && allOlderThan(page.Posts, *options.Since) {
			exhausted = !uncertain
			break
		}
		if len(byID) >= target {
			selected := mostRecent(byID, options.Limit)
			cutoff := selected[len(selected)-1].CreateAt
			if containsOlderThan(page.Posts, cutoff) {
				break
			}
		}
		if page.RawCount < pageSize && (page.HasNext == nil || !*page.HasNext) {
			exhausted = !uncertain
			break
		}
		if stagnantPages >= 2 {
			uncertain = true
			break
		}
	}

	result := ChannelHistoryResult{Posts: mostRecent(byID, options.Limit), SafeBeforeValid: options.SafeBeforePostID == "" || !retriedWithoutAnchor}
	switch {
	case uncertain || !exhausted:
		result.Completeness = CompletenessUnknown
	case len(byID) > options.Limit:
		result.Completeness = CompletenessTruncated
	default:
		result.Completeness = CompletenessComplete
	}
	return result, nil
}

func safeBoundaryID(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for i := range len(value) {
		c := value[i]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func withinSelection(post mattermost.Post, options ChannelHistoryOptions) bool {
	if options.Since != nil && post.CreateAt < *options.Since {
		return false
	}
	if options.Boundary == nil {
		return true
	}
	return post.CreateAt < options.Boundary.CreateAt ||
		(post.CreateAt == options.Boundary.CreateAt && post.ID > options.Boundary.ID)
}

func mostRecent(posts map[string]mattermost.Post, limit int) []mattermost.Post {
	result := make([]mattermost.Post, 0, len(posts))
	for _, post := range posts {
		result = append(result, post)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].CreateAt != result[j].CreateAt {
			return result[i].CreateAt > result[j].CreateAt
		}
		return result[i].ID < result[j].ID
	})
	if len(result) > limit {
		result = result[:limit]
	}
	return result
}

func allOlderThan(posts []mattermost.Post, since int64) bool {
	for _, post := range posts {
		if post.CreateAt >= since {
			return false
		}
	}
	return true
}

func containsOlderThan(posts []mattermost.Post, cutoff int64) bool {
	for _, post := range posts {
		if post.CreateAt < cutoff {
			return true
		}
	}
	return false
}
