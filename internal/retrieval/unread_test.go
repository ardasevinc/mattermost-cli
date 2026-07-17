package retrieval

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ardasevinc/mattermost-cli/v2/internal/mattermost"
)

type unreadUsersFake struct {
	user mattermost.User
	err  error
}

func (f unreadUsersFake) Current(context.Context) (mattermost.User, error) { return f.user, f.err }

type unreadTeamsFake struct {
	team       mattermost.Team
	userID     string
	selectTeam string
}

func (f *unreadTeamsFake) Resolve(_ context.Context, userID, selector string) (mattermost.Team, error) {
	f.userID, f.selectTeam = userID, selector
	return f.team, nil
}

type unreadChannelsFake struct {
	channels []mattermost.Channel
	members  []mattermost.UnreadMember
	fallback map[string]mattermost.UnreadMember
	mu       sync.Mutex
	calls    []string
}

func (f *unreadChannelsFake) ListForUnread(ctx context.Context, userID string) ([]mattermost.Channel, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "list:"+userID)
	return append([]mattermost.Channel(nil), f.channels...), nil
}
func (f *unreadChannelsFake) TeamMembers(ctx context.Context, userID, teamID string) ([]mattermost.UnreadMember, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "team:"+userID+":"+teamID)
	return append([]mattermost.UnreadMember(nil), f.members...), nil
}
func (f *unreadChannelsFake) UnreadMember(ctx context.Context, channelID, userID string) (mattermost.UnreadMember, error) {
	if err := ctx.Err(); err != nil {
		return mattermost.UnreadMember{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "fallback:"+channelID+":"+userID)
	member, ok := f.fallback[channelID]
	if !ok {
		return mattermost.UnreadMember{}, ErrIncompleteUnreadResult
	}
	return member, nil
}

type unreadPostsFake struct {
	mu        sync.Mutex
	pages     map[string]mattermost.OrderedPostsPage
	calls     int
	active    int
	maxActive int
}

type blockingUnreadPosts struct {
	started chan string
	exited  atomic.Int32
	calls   atomic.Int32
	failID  string
	failErr error
	barrier chan struct{}
	once    sync.Once
}

func (f *blockingUnreadPosts) ChannelPage(ctx context.Context, channelID string, _ mattermost.ChannelPostsOptions) (mattermost.OrderedPostsPage, error) {
	f.calls.Add(1)
	f.started <- channelID
	if f.barrier != nil {
		if f.calls.Load() >= 2 {
			f.once.Do(func() { close(f.barrier) })
		}
		select {
		case <-f.barrier:
		case <-ctx.Done():
			f.exited.Add(1)
			return mattermost.OrderedPostsPage{}, ctx.Err()
		}
	}
	if channelID == f.failID {
		f.exited.Add(1)
		return mattermost.OrderedPostsPage{}, f.failErr
	}
	<-ctx.Done()
	f.exited.Add(1)
	return mattermost.OrderedPostsPage{}, ctx.Err()
}

func (f *unreadPostsFake) ChannelPage(ctx context.Context, channelID string, _ mattermost.ChannelPostsOptions) (mattermost.OrderedPostsPage, error) {
	if err := ctx.Err(); err != nil {
		return mattermost.OrderedPostsPage{}, err
	}
	f.mu.Lock()
	f.calls++
	f.active++
	if f.active > f.maxActive {
		f.maxActive = f.active
	}
	page, ok := f.pages[channelID]
	f.active--
	f.mu.Unlock()
	if !ok {
		return mattermost.OrderedPostsPage{}, errors.New("missing page")
	}
	return page, nil
}

func unreadFixture() (unreadUsersFake, *unreadTeamsFake, *unreadChannelsFake) {
	users := unreadUsersFake{user: mattermost.User{ID: "user", Username: "arda"}}
	teams := &unreadTeamsFake{team: mattermost.Team{ID: "team", Name: "core", Type: "O"}}
	channels := &unreadChannelsFake{
		channels: []mattermost.Channel{
			{ID: "z", TeamID: "team", Type: "O", TotalMsgCount: 8},
			{ID: "a", TeamID: "team", Type: "P", TotalMsgCount: 8},
			{ID: "dm", Type: "D", TotalMsgCount: 4},
			{ID: "other", TeamID: "other-team", Type: "O", TotalMsgCount: 99},
		},
		members: []mattermost.UnreadMember{
			{ChannelID: "z", UserID: "user", MsgCount: 6, MentionCount: 1, LastViewedAt: 10},
			{ChannelID: "a", UserID: "user", MsgCount: 6, MentionCount: 1, LastViewedAt: 11},
		},
		fallback: map[string]mattermost.UnreadMember{"dm": {ChannelID: "dm", UserID: "user", MsgCount: 1, MentionCount: 2, LastViewedAt: 12}},
	}
	return users, teams, channels
}

func TestUnreadResolvesExactScopeFallsBackOnlyForDirectAndSortsDeterministically(t *testing.T) {
	users, teams, channels := unreadFixture()
	got, err := Unread(context.Background(), users, teams, channels, nil, UnreadOptions{TeamSelector: "core"})
	if err != nil {
		t.Fatal(err)
	}
	if teams.userID != "user" || teams.selectTeam != "core" {
		t.Fatalf("resolve = %q %q", teams.userID, teams.selectTeam)
	}
	ids := []string{got.Entries[0].Channel.ID, got.Entries[1].Channel.ID, got.Entries[2].Channel.ID}
	if !reflect.DeepEqual(ids, []string{"dm", "a", "z"}) {
		t.Fatalf("IDs = %v", ids)
	}
	if !reflect.DeepEqual(channels.calls, []string{"list:user", "team:user:team", "fallback:dm:user"}) {
		t.Fatalf("calls = %v", channels.calls)
	}
}

func TestUnreadHandlesMemberAheadOfTotalAsCaughtUp(t *testing.T) {
	users, teams, channels := unreadFixture()
	channels.members[0].MsgCount = 99
	channels.channels = channels.channels[:1]
	got, err := Unread(context.Background(), users, teams, channels, nil, UnreadOptions{})
	if err != nil || len(got.Entries) != 0 {
		t.Fatalf("result = %+v, error = %v", got, err)
	}
}

func TestUnreadRejectsMalformedDuplicateAndMissingMetrics(t *testing.T) {
	for name, mutate := range map[string]func(*unreadChannelsFake){
		"duplicate":               func(f *unreadChannelsFake) { f.members = append(f.members, f.members[0]) },
		"foreign user":            func(f *unreadChannelsFake) { f.members[0].UserID = "other" },
		"negative metric":         func(f *unreadChannelsFake) { f.members[0].MentionCount = -1 },
		"missing selected member": func(f *unreadChannelsFake) { f.members = f.members[1:] },
	} {
		t.Run(name, func(t *testing.T) {
			users, teams, channels := unreadFixture()
			mutate(channels)
			_, err := Unread(context.Background(), users, teams, channels, nil, UnreadOptions{})
			if !errors.Is(err, ErrIncompleteUnreadResult) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestUnreadRejectsMalformedFallbackBinding(t *testing.T) {
	users, teams, channels := unreadFixture()
	channels.fallback["dm"] = mattermost.UnreadMember{ChannelID: "other", UserID: "user", MsgCount: 1}
	_, err := Unread(context.Background(), users, teams, channels, nil, UnreadOptions{})
	if !errors.Is(err, ErrIncompleteUnreadResult) {
		t.Fatalf("error = %v", err)
	}
}

func TestUnreadEmptySnapshotIsComplete(t *testing.T) {
	users, teams, channels := unreadFixture()
	channels.channels, channels.members = []mattermost.Channel{}, []mattermost.UnreadMember{}
	got, err := Unread(context.Background(), users, teams, channels, nil, UnreadOptions{})
	if err != nil || got.Entries == nil || len(got.Entries) != 0 {
		t.Fatalf("result = %#v, error = %v", got, err)
	}
}

func TestUnreadPeekUsesGlobalBudgetAndFailsClosedOnPartialUnknown(t *testing.T) {
	users, teams, channels := unreadFixture()
	posts := &unreadPostsFake{pages: map[string]mattermost.OrderedPostsPage{
		"dm": {Posts: []mattermost.Post{{ID: "p1", ChannelID: "dm", CreateAt: 20}}, RawCount: 1, HasNext: dmBoolPointer(false)},
		"a":  {Incomplete: true},
		"z":  {Posts: []mattermost.Post{{ID: "p2", ChannelID: "z", CreateAt: 20}}, RawCount: 1, HasNext: dmBoolPointer(false)},
	}}
	_, err := Unread(context.Background(), users, teams, channels, posts, UnreadOptions{PeekLimit: 1, PeekRequestBudget: 3, PeekConcurrency: 2})
	if !errors.Is(err, ErrIncompleteUnreadResult) {
		t.Fatalf("error = %v", err)
	}
	if posts.calls > 3 || posts.maxActive > 2 {
		t.Fatalf("calls=%d maxActive=%d", posts.calls, posts.maxActive)
	}

	users, teams, channels = unreadFixture()
	posts = &unreadPostsFake{pages: map[string]mattermost.OrderedPostsPage{}}
	_, err = Unread(context.Background(), users, teams, channels, posts, UnreadOptions{PeekLimit: 1, PeekRequestBudget: 2})
	if !errors.Is(err, ErrIncompleteUnreadResult) || posts.calls != 0 {
		t.Fatalf("error=%v calls=%d", err, posts.calls)
	}
}

func TestUnreadPeekReturnsOnlyCompleteBoundedResults(t *testing.T) {
	users, teams, channels := unreadFixture()
	channels.channels = channels.channels[:1]
	channels.members = channels.members[:1]
	posts := &unreadPostsFake{pages: map[string]mattermost.OrderedPostsPage{
		"z": {Posts: []mattermost.Post{{ID: "p", ChannelID: "z", CreateAt: 20}}, RawCount: 1, HasNext: dmBoolPointer(false)},
	}}
	got, err := Unread(context.Background(), users, teams, channels, posts, UnreadOptions{PeekLimit: 1, PeekRequestBudget: 1, PeekConcurrency: 1})
	if err != nil || len(got.Entries) != 1 || len(got.Entries[0].Peek) != 1 || got.Entries[0].PeekState != CompletenessComplete {
		t.Fatalf("result = %+v, error = %v", got, err)
	}
}

func TestUnreadBoundsMembershipFallbackBeforeFanout(t *testing.T) {
	users, teams, channels := unreadFixture()
	channels.channels = make([]mattermost.Channel, MaxUnreadMembershipFallbacks+1)
	channels.members = []mattermost.UnreadMember{}
	for i := range channels.channels {
		channels.channels[i] = mattermost.Channel{ID: string(rune('a' + i)), Type: "G", TotalMsgCount: 1}
	}
	_, err := Unread(context.Background(), users, teams, channels, nil, UnreadOptions{})
	if !errors.Is(err, ErrIncompleteUnreadResult) {
		t.Fatalf("error = %v", err)
	}
	if !reflect.DeepEqual(channels.calls, []string{"list:user", "team:user:team"}) {
		t.Fatalf("calls = %v", channels.calls)
	}
}

func TestUnreadCancellationStopsBeforeReads(t *testing.T) {
	users, teams, channels := unreadFixture()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Unread(ctx, users, teams, channels, nil, UnreadOptions{})
	if !errors.Is(err, context.Canceled) || len(channels.calls) != 0 {
		t.Fatalf("error=%v calls=%v", err, channels.calls)
	}
}

func TestUnreadPeekCallerCancellationJoinsAllStartedWorkers(t *testing.T) {
	users, teams, channels := unreadFixture()
	posts := &blockingUnreadPosts{started: make(chan string, 3)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := Unread(ctx, users, teams, channels, posts, UnreadOptions{PeekLimit: 1, PeekRequestBudget: 3, PeekConcurrency: 2})
		done <- err
	}()
	<-posts.started
	<-posts.started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	if got := posts.exited.Load(); got != posts.calls.Load() || got != 2 {
		t.Fatalf("calls=%d exited=%d", posts.calls.Load(), got)
	}
}

func TestUnreadPeekFirstFailureCancelsSiblingsAndQueuedWork(t *testing.T) {
	users, teams, channels := unreadFixture()
	sentinel := errors.New("peek failed")
	posts := &blockingUnreadPosts{
		started: make(chan string, 3), failID: "dm", failErr: sentinel, barrier: make(chan struct{}),
	}
	_, err := Unread(context.Background(), users, teams, channels, posts, UnreadOptions{PeekLimit: 1, PeekRequestBudget: 3, PeekConcurrency: 2})
	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %v", err)
	}
	if calls, exited := posts.calls.Load(), posts.exited.Load(); calls != 2 || exited != calls {
		t.Fatalf("calls=%d exited=%d", calls, exited)
	}
}
