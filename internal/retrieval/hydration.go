package retrieval

import (
	"context"

	"github.com/ardasevinc/mattermost-cli/internal/mattermost"
)

type VisibleThreadsStatus uint8

const (
	VisibleThreadsNotRequested VisibleThreadsStatus = iota
	VisibleThreadsComplete
	VisibleThreadsPartial
)

type VisibleThreadsMetadata struct {
	Status            VisibleThreadsStatus
	HydratedRootCount int
	FailedRootIDs     []string
}

type HydrationResult struct {
	Posts          []mattermost.Post
	VisibleThreads VisibleThreadsMetadata
}

func HydrateVisibleThreads(ctx context.Context, source threadPageSource, seeds []mattermost.Post, requested bool) (HydrationResult, error) {
	if !requested {
		return HydrationResult{Posts: seeds, VisibleThreads: VisibleThreadsMetadata{Status: VisibleThreadsNotRequested, FailedRootIDs: []string{}}}, nil
	}
	rootIDs, unknownShape := visibleRootIDs(seeds)
	if len(rootIDs) == 0 {
		status := VisibleThreadsComplete
		if unknownShape {
			status = VisibleThreadsPartial
		}
		return HydrationResult{Posts: seeds, VisibleThreads: VisibleThreadsMetadata{Status: status, FailedRootIDs: []string{}}}, nil
	}
	type outcome struct {
		result ThreadResult
		err    error
		reused bool
	}
	outcomes := make([]outcome, len(rootIDs))
	jobs := make(chan int)
	worker := func() {
		for index := range jobs {
			if ctx.Err() != nil {
				return
			}
			rootID := rootIDs[index]
			if seedThreadComplete(seeds, rootID) {
				outcomes[index].reused = true
				continue
			}
			outcomes[index].result, outcomes[index].err = Thread(ctx, source, rootID)
		}
	}
	workers := len(rootIDs)
	if workers > 4 {
		workers = 4
	}
	done := make(chan struct{}, workers)
	for range workers {
		go func() { worker(); done <- struct{}{} }()
	}
	dispatchCanceled := false
dispatch:
	for index := range rootIDs {
		select {
		case jobs <- index:
		case <-ctx.Done():
			dispatchCanceled = true
			break dispatch
		}
	}
	close(jobs)
	for range workers {
		<-done
	}
	if dispatchCanceled || ctx.Err() != nil {
		return HydrationResult{}, ctx.Err()
	}

	posts := append([]mattermost.Post(nil), seeds...)
	seen := make(map[string]struct{}, len(seeds))
	for _, post := range seeds {
		seen[post.ID] = struct{}{}
	}
	metadata := VisibleThreadsMetadata{Status: VisibleThreadsComplete, FailedRootIDs: []string{}}
	if unknownShape {
		metadata.Status = VisibleThreadsPartial
	}
	for index, rootID := range rootIDs {
		outcome := outcomes[index]
		if !hydratedThreadMatchesSeedChannel(seeds, rootID, outcome.result.Posts) {
			outcome.result.Posts = nil
			outcome.err = mattermost.ErrInvalidPostsResponse
		}
		for _, post := range outcome.result.Posts {
			if _, exists := seen[post.ID]; exists {
				continue
			}
			seen[post.ID] = struct{}{}
			posts = append(posts, post)
		}
		rootPresent := false
		for _, post := range outcome.result.Posts {
			if post.ID == rootID && post.RootID == "" {
				rootPresent = true
				break
			}
		}
		if outcome.reused || (outcome.err == nil && outcome.result.Completeness == CompletenessComplete && rootPresent) {
			metadata.HydratedRootCount++
		} else {
			metadata.Status = VisibleThreadsPartial
			metadata.FailedRootIDs = append(metadata.FailedRootIDs, rootID)
		}
	}
	return HydrationResult{Posts: posts, VisibleThreads: metadata}, nil
}

func hydratedThreadMatchesSeedChannel(seeds []mattermost.Post, rootID string, hydrated []mattermost.Post) bool {
	expected := ""
	for _, post := range seeds {
		if !post.ThreadShapeKnown {
			continue
		}
		candidate := post.RootID
		if candidate == "" && post.ID == rootID {
			candidate = post.ID
		}
		if candidate != rootID || post.ChannelID == "" {
			continue
		}
		if expected == "" {
			expected = post.ChannelID
		} else if expected != post.ChannelID {
			return false
		}
	}
	if expected == "" {
		return false
	}
	for _, post := range hydrated {
		if post.ChannelID != expected {
			return false
		}
	}
	return true
}

func visibleRootIDs(posts []mattermost.Post) ([]string, bool) {
	seen := make(map[string]struct{})
	result := make([]string, 0)
	unknownShape := false
	for _, post := range posts {
		if !post.ThreadShapeKnown {
			unknownShape = true
			continue
		}
		rootID := post.RootID
		if rootID == "" && post.ReplyCount > 0 {
			rootID = post.ID
		}
		if rootID == "" {
			continue
		}
		if _, exists := seen[rootID]; exists {
			continue
		}
		seen[rootID] = struct{}{}
		result = append(result, rootID)
	}
	return result, unknownShape
}

func seedThreadComplete(posts []mattermost.Post, rootID string) bool {
	var root *mattermost.Post
	replies := 0
	for index := range posts {
		post := &posts[index]
		if post.ThreadShapeKnown && post.ID == rootID && post.RootID == "" {
			root = post
		}
		if post.ThreadShapeKnown && post.RootID == rootID {
			replies++
		}
	}
	return root != nil && replies >= root.ReplyCount
}
