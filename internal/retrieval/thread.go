package retrieval

import (
	"context"
	"errors"
	"strings"

	"github.com/ardasevinc/mattermost-cli/internal/mattermost"
)

const MaxThreadPages = 100

type ThreadResult struct {
	Posts        []mattermost.Post
	Completeness Completeness
}

type threadPageSource interface {
	ThreadPage(context.Context, string, mattermost.ThreadPageOptions) (mattermost.OrderedPostsPage, error)
}

func Thread(ctx context.Context, source threadPageSource, rootID string) (ThreadResult, error) {
	if source == nil || strings.TrimSpace(rootID) == "" {
		return ThreadResult{}, mattermost.ErrInvalidPostsRequest
	}
	byID := make(map[string]mattermost.Post)
	order := make([]string, 0)
	var fromPost string
	var fromCreateAt *int64
	uncertain, stagnantPages := false, 0
	var canonicalRootID, canonicalChannelID string

	for pageNumber := 0; pageNumber < MaxThreadPages; pageNumber++ {
		if err := ctx.Err(); err != nil {
			return ThreadResult{}, err
		}
		page, err := source.ThreadPage(ctx, rootID, mattermost.ThreadPageOptions{
			PerPage: mattermost.MaxPostsPage, FromPost: fromPost, FromCreateAt: fromCreateAt,
		})
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
				return ThreadResult{}, contextError(ctx, err)
			}
			if pageNumber == 0 {
				return ThreadResult{}, err
			}
			return ThreadResult{Posts: orderedPosts(byID, order), Completeness: CompletenessUnknown}, nil
		}
		if page.Incomplete || page.FirstInaccessiblePostTime != nil {
			uncertain = true
		}
		if pageNumber == 0 && page.ThreadChannelID != "" && !page.ContainsRequestedPost {
			return ThreadResult{}, mattermost.ErrInvalidPostsResponse
		}
		if page.ThreadRootID != "" {
			if canonicalRootID == "" {
				canonicalRootID = page.ThreadRootID
			} else if canonicalRootID != page.ThreadRootID {
				return ThreadResult{Posts: orderedPosts(byID, order), Completeness: CompletenessUnknown}, nil
			}
		}
		if page.ThreadChannelID != "" {
			if canonicalChannelID == "" {
				canonicalChannelID = page.ThreadChannelID
			} else if canonicalChannelID != page.ThreadChannelID {
				return ThreadResult{Posts: orderedPosts(byID, order), Completeness: CompletenessUnknown}, nil
			}
		}
		added := 0
		for _, post := range page.Posts {
			if _, exists := byID[post.ID]; exists {
				continue
			}
			byID[post.ID] = post
			order = append(order, post.ID)
			added++
		}

		if page.HasNext == nil || !*page.HasNext {
			complete := page.HasNext != nil && !*page.HasNext
			if page.HasNext == nil && !page.Incomplete {
				completionRootID := canonicalRootID
				if completionRootID == "" {
					completionRootID = rootID
				}
				complete = legacyThreadComplete(byID, completionRootID)
			}
			completeness := CompletenessUnknown
			if !uncertain && complete {
				completeness = CompletenessComplete
			}
			return ThreadResult{Posts: orderedPosts(byID, order), Completeness: completeness}, nil
		}

		if page.Continuation == nil {
			return ThreadResult{Posts: orderedPosts(byID, order), Completeness: threadPartialCompleteness(uncertain)}, nil
		}
		advanced := page.Continuation.PostID != fromPost || fromCreateAt == nil || page.Continuation.CreateAt != *fromCreateAt
		if added > 0 && advanced {
			stagnantPages = 0
		} else {
			stagnantPages++
		}
		if stagnantPages >= 2 {
			return ThreadResult{Posts: orderedPosts(byID, order), Completeness: threadPartialCompleteness(uncertain)}, nil
		}
		fromPost = page.Continuation.PostID
		value := page.Continuation.CreateAt
		fromCreateAt = &value
	}
	return ThreadResult{Posts: orderedPosts(byID, order), Completeness: threadPartialCompleteness(uncertain)}, nil
}

func contextError(ctx context.Context, fallback error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return fallback
}

func threadPartialCompleteness(uncertain bool) Completeness {
	if uncertain {
		return CompletenessUnknown
	}
	return CompletenessTruncated
}

func legacyThreadComplete(posts map[string]mattermost.Post, rootID string) bool {
	root, ok := posts[rootID]
	if !ok || !root.ThreadShapeKnown || root.RootID != "" {
		return false
	}
	replies := 0
	for _, post := range posts {
		if post.RootID == rootID {
			replies++
		}
	}
	return replies >= root.ReplyCount
}

func orderedPosts(byID map[string]mattermost.Post, order []string) []mattermost.Post {
	posts := make([]mattermost.Post, 0, len(order))
	for _, id := range order {
		posts = append(posts, byID[id])
	}
	return posts
}
