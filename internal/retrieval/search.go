package retrieval

import (
	"context"
	"errors"
	"strings"

	"github.com/ardasevinc/mattermost-cli/internal/mattermost"
)

const MaxSearchPages = 100

var ErrInvalidSearchRequest = errors.New("invalid search request")

type SearchOptions struct {
	Limit  int
	Accept func(mattermost.Post) bool
}

type SearchResult struct {
	Posts        []mattermost.Post
	Matches      map[string][]string
	Completeness Completeness
}

type searchPageSource interface {
	SearchPage(context.Context, string, mattermost.SearchPageOptions) (mattermost.SearchPage, error)
}

func Search(ctx context.Context, source searchPageSource, teamID, terms string, options SearchOptions) (SearchResult, error) {
	if source == nil || strings.TrimSpace(teamID) == "" || strings.TrimSpace(terms) == "" || options.Limit <= 0 || int64(options.Limit) > maxSafeInteger {
		return SearchResult{}, ErrInvalidSearchRequest
	}
	accept := options.Accept
	if accept == nil {
		accept = func(mattermost.Post) bool { return true }
	}
	target := options.Limit + 1
	perPage := target
	if perPage > mattermost.MaxSearchPage {
		perPage = mattermost.MaxSearchPage
	}
	byID := make(map[string]mattermost.Post)
	matches := make(map[string][]string)
	seen := make(map[string]struct{})
	uncertain, exhausted, stagnantPages := false, false, 0

	for pageNumber := 0; pageNumber < MaxSearchPages; pageNumber++ {
		if err := ctx.Err(); err != nil {
			return SearchResult{}, err
		}
		page, err := source.SearchPage(ctx, teamID, mattermost.SearchPageOptions{Terms: terms, Page: pageNumber, PerPage: perPage})
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
				return SearchResult{}, contextError(ctx, err)
			}
			return SearchResult{}, err
		}
		if page.Incomplete || page.FirstInaccessiblePostTime != nil {
			uncertain = true
		}
		madeProgress := false
		firstSeen := make(map[string]struct{}, len(page.OrderedIDs))
		for _, id := range page.OrderedIDs {
			if _, duplicate := seen[id]; duplicate {
				continue
			}
			seen[id] = struct{}{}
			firstSeen[id] = struct{}{}
			madeProgress = true
		}
		acceptedThisPage := make([]mattermost.Post, 0, len(page.Posts))
		for _, post := range page.Posts {
			if _, first := firstSeen[post.ID]; !first || !accept(post) {
				continue
			}
			byID[post.ID] = post
			acceptedThisPage = append(acceptedThisPage, post)
			if value, ok := page.Matches[post.ID]; ok {
				matches[post.ID] = append([]string(nil), value...)
			}
		}
		if madeProgress {
			stagnantPages = 0
		} else {
			stagnantPages++
		}

		if page.RawCount == 0 {
			if page.HasNext != nil && *page.HasNext {
				uncertain = true
			}
			exhausted = !uncertain
			break
		}
		if page.HasNext != nil && !*page.HasNext {
			exhausted = !uncertain
			break
		}
		if stagnantPages >= 2 {
			uncertain = true
			break
		}
		if len(byID) >= target {
			selected := mostRecent(byID, options.Limit)
			cutoff := selected[len(selected)-1].CreateAt
			if containsOlderThan(acceptedThisPage, cutoff) {
				break
			}
		}
		if pageNumber == MaxSearchPages-1 {
			uncertain = true
		}
	}

	selected := mostRecent(byID, options.Limit)
	selectedMatches := make(map[string][]string)
	for _, post := range selected {
		if value, ok := matches[post.ID]; ok {
			selectedMatches[post.ID] = value
		}
	}
	result := SearchResult{Posts: selected, Matches: selectedMatches}
	switch {
	case len(byID) > options.Limit:
		result.Completeness = CompletenessTruncated
	case uncertain || !exhausted:
		result.Completeness = CompletenessUnknown
	default:
		result.Completeness = CompletenessComplete
	}
	return result, nil
}
