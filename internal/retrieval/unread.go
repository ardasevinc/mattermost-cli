package retrieval

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"

	"github.com/ardasevinc/mattermost-cli/v2/internal/mattermost"
)

const (
	MaxUnreadMembershipFallbacks = 100
	MaxUnreadPeekRequests        = 100
	MaxUnreadPeekConcurrency     = 8
)

var (
	ErrInvalidUnreadRequest   = errors.New("invalid unread request")
	ErrIncompleteUnreadResult = errors.New("Mattermost did not provide a complete unread result")
)

type currentUserSource interface {
	Current(context.Context) (mattermost.User, error)
}

type teamResolveSource interface {
	Resolve(context.Context, string, string) (mattermost.Team, error)
}

type unreadChannelSource interface {
	ListForUnread(context.Context, string) ([]mattermost.Channel, error)
	TeamMembers(context.Context, string, string) ([]mattermost.UnreadMember, error)
	UnreadMember(context.Context, string, string) (mattermost.UnreadMember, error)
}

type UnreadOptions struct {
	TeamSelector      string
	PeekLimit         int
	PeekRequestBudget int
	PeekConcurrency   int
}

type UnreadEntry struct {
	Channel      mattermost.Channel
	UnreadCount  int64
	MentionCount int64
	LastViewedAt int64
	Peek         []mattermost.Post
	PeekState    Completeness
}

type UnreadResult struct {
	User    mattermost.User
	Team    mattermost.Team
	Entries []UnreadEntry
}

// Unread builds one complete unread snapshot. Team membership metrics are the
// primary bounded source; only D/G channels absent from that team-scoped
// snapshot use exact per-channel membership reads.
func Unread(
	ctx context.Context,
	users currentUserSource,
	teams teamResolveSource,
	channels unreadChannelSource,
	posts channelPageSource,
	options UnreadOptions,
) (UnreadResult, error) {
	if users == nil || teams == nil || channels == nil || !validUnreadOptions(options, posts) {
		return UnreadResult{}, ErrInvalidUnreadRequest
	}
	if err := ctx.Err(); err != nil {
		return UnreadResult{}, err
	}
	user, err := users.Current(ctx)
	if err != nil {
		return UnreadResult{}, err
	}
	team, err := teams.Resolve(ctx, user.ID, options.TeamSelector)
	if err != nil {
		return UnreadResult{}, err
	}
	allChannels, err := channels.ListForUnread(ctx, user.ID)
	if err != nil {
		return UnreadResult{}, err
	}
	candidates := make([]mattermost.Channel, 0, len(allChannels))
	for _, channel := range allChannels {
		if channel.Type == "D" || channel.Type == "G" || ((channel.Type == "O" || channel.Type == "P") && channel.TeamID == team.ID) {
			candidates = append(candidates, channel)
		}
	}
	members, err := channels.TeamMembers(ctx, user.ID, team.ID)
	if err != nil {
		return UnreadResult{}, err
	}
	memberByChannel := make(map[string]mattermost.UnreadMember, len(members))
	for _, member := range members {
		if member.UserID != user.ID || strings.TrimSpace(member.ChannelID) != member.ChannelID || member.ChannelID == "" ||
			member.MsgCount < 0 || member.MentionCount < 0 || member.LastViewedAt < 0 {
			return UnreadResult{}, ErrIncompleteUnreadResult
		}
		if _, duplicate := memberByChannel[member.ChannelID]; duplicate {
			return UnreadResult{}, ErrIncompleteUnreadResult
		}
		memberByChannel[member.ChannelID] = member
	}
	missingDirect := 0
	for _, channel := range candidates {
		if _, ok := memberByChannel[channel.ID]; !ok && (channel.Type == "D" || channel.Type == "G") {
			missingDirect++
		}
	}
	if missingDirect > MaxUnreadMembershipFallbacks {
		return UnreadResult{}, ErrIncompleteUnreadResult
	}

	entries := make([]UnreadEntry, 0, len(candidates))
	for _, channel := range candidates {
		if err := ctx.Err(); err != nil {
			return UnreadResult{}, err
		}
		member, ok := memberByChannel[channel.ID]
		if !ok && (channel.Type == "D" || channel.Type == "G") {
			member, err = channels.UnreadMember(ctx, channel.ID, user.ID)
			if err != nil {
				return UnreadResult{}, err
			}
			if member.ChannelID != channel.ID || member.UserID != user.ID || member.MsgCount < 0 || member.MentionCount < 0 || member.LastViewedAt < 0 {
				return UnreadResult{}, ErrIncompleteUnreadResult
			}
			ok = true
		}
		if !ok {
			return UnreadResult{}, ErrIncompleteUnreadResult
		}
		unreadCount := int64(0)
		if channel.TotalMsgCount > member.MsgCount {
			unreadCount = channel.TotalMsgCount - member.MsgCount
		}
		if unreadCount == 0 {
			continue
		}
		entries = append(entries, UnreadEntry{
			Channel: channel, UnreadCount: unreadCount, MentionCount: member.MentionCount,
			LastViewedAt: member.LastViewedAt, Peek: []mattermost.Post{}, PeekState: CompletenessComplete,
		})
	}
	sortUnreadEntries(entries)
	if options.PeekLimit > 0 && len(entries) > 0 {
		if err := addUnreadPeek(ctx, posts, entries, options); err != nil {
			return UnreadResult{}, err
		}
	}
	return UnreadResult{User: user, Team: team, Entries: entries}, nil
}

func validUnreadOptions(options UnreadOptions, posts channelPageSource) bool {
	if strings.TrimSpace(options.TeamSelector) != options.TeamSelector || options.PeekLimit < 0 || int64(options.PeekLimit) > maxSafeInteger ||
		options.PeekRequestBudget < 0 || options.PeekRequestBudget > MaxUnreadPeekRequests ||
		options.PeekConcurrency < 0 || options.PeekConcurrency > MaxUnreadPeekConcurrency {
		return false
	}
	return options.PeekLimit == 0 || posts != nil
}

func sortUnreadEntries(entries []UnreadEntry) {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].MentionCount != entries[j].MentionCount {
			return entries[i].MentionCount > entries[j].MentionCount
		}
		if entries[i].UnreadCount != entries[j].UnreadCount {
			return entries[i].UnreadCount > entries[j].UnreadCount
		}
		return entries[i].Channel.ID < entries[j].Channel.ID
	})
}

func addUnreadPeek(ctx context.Context, posts channelPageSource, entries []UnreadEntry, options UnreadOptions) error {
	budget := options.PeekRequestBudget
	if budget == 0 {
		budget = MaxUnreadPeekRequests
	}
	if len(entries) > budget {
		return ErrIncompleteUnreadResult
	}
	concurrency := options.PeekConcurrency
	if concurrency == 0 {
		concurrency = MaxUnreadPeekConcurrency
	}
	if concurrency > len(entries) {
		concurrency = len(entries)
	}
	peekCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan int)
	var wg sync.WaitGroup
	var firstErr error
	var failOnce sync.Once
	fail := func(err error) {
		if err == nil {
			return
		}
		failOnce.Do(func() {
			firstErr = err
			cancel()
		})
	}
	for range concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				requestBudget := 1
				since := entries[index].LastViewedAt
				result, err := ChannelHistory(peekCtx, posts, entries[index].Channel.ID, ChannelHistoryOptions{
					Limit: options.PeekLimit, Since: &since, RequestBudget: &requestBudget,
				})
				if err != nil {
					fail(err)
					return
				}
				if result.Completeness == CompletenessUnknown {
					fail(ErrIncompleteUnreadResult)
					return
				}
				entries[index].Peek = result.Posts
				entries[index].PeekState = result.Completeness
			}
		}()
	}

launch:
	for i := range entries {
		select {
		case jobs <- i:
		case <-peekCtx.Done():
			break launch
		}
	}
	close(jobs)
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}
