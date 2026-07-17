package mattermost

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
)

type fakeChannelTransport struct {
	mu        sync.Mutex
	responses map[string]string
	paths     []string
}

func (f *fakeChannelTransport) Get(ctx context.Context, path string, out any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paths = append(f.paths, path)
	payload, ok := f.responses[path]
	if !ok {
		return errors.New("unexpected request")
	}
	return json.Unmarshal([]byte(payload), out)
}

func TestChannelMetadataRequiresExactBoundedJSONIntegers(t *testing.T) {
	valid := `{"id":"x","team_id":"team","type":"O","name":"general","display_name":"General","last_post_at":9223372036854775807,"total_msg_count":0}`
	got, err := NewChannels(&fakeChannelTransport{responses: map[string]string{"/channels/x": valid}}).ByID(context.Background(), "x")
	if err != nil || got.LastPostAt != int64(9223372036854775807) || got.TotalMsgCount != 0 {
		t.Fatalf("channel = %+v, error = %v", got, err)
	}

	badValues := []string{`null`, `"1"`, `1.5`, `1e3`, `-1`, `9223372036854775808`}
	for _, field := range []string{"last_post_at", "total_msg_count"} {
		for _, value := range badValues {
			payload := `{"id":"x","team_id":"team","type":"O","name":"general","display_name":"General","last_post_at":0,"total_msg_count":0}`
			if field == "last_post_at" {
				payload = strings.Replace(payload, `"last_post_at":0`, `"last_post_at":`+value, 1)
			} else {
				payload = strings.Replace(payload, `"total_msg_count":0`, `"total_msg_count":`+value, 1)
			}
			f := &fakeChannelTransport{responses: map[string]string{"/channels/x": payload}}
			if _, err := NewChannels(f).ByID(context.Background(), "x"); !errors.Is(err, ErrInvalidChannelResponse) {
				t.Fatalf("%s=%s: error = %v", field, value, err)
			}
		}
	}
}

func TestChannelMetadataDefaultsOnlyWhenAbsent(t *testing.T) {
	payload := `{"id":"x","team_id":"team","type":"O","name":"general","display_name":"General"}`
	got, err := NewChannels(&fakeChannelTransport{responses: map[string]string{"/channels/x": payload}}).ByID(context.Background(), "x")
	if err != nil || got.LastPostAt != 0 || got.TotalMsgCount != 0 {
		t.Fatalf("channel = %+v, error = %v", got, err)
	}
}

func TestChannelLookupsEncodeAndRequireExactIdentity(t *testing.T) {
	f := &fakeChannelTransport{responses: map[string]string{
		"/channels/channel%2Fone":                        `{"id":"channel/one","team_id":"team/one","type":"P","name":"release/name","display_name":"Release","last_post_at":7,"total_msg_count":8}`,
		"/teams/team%2Fone/channels/name/release%2Fname": `{"id":"channel/one","team_id":"team/one","type":"P","name":"release/name","display_name":"Release","last_post_at":7,"total_msg_count":8}`,
	}}
	channels := NewChannels(f)
	if _, err := channels.ByID(context.Background(), "channel/one"); err != nil {
		t.Fatal(err)
	}
	if _, err := channels.ByName(context.Background(), "team/one", "#release/name"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.paths, []string{"/channels/channel%2Fone", "/teams/team%2Fone/channels/name/release%2Fname"}) {
		t.Fatalf("paths = %v", f.paths)
	}
}

func TestChannelDecodingFailsClosedForRequiredShape(t *testing.T) {
	bad := []string{
		`null`, `{}`, `{"id":"remote-secret","team_id":"","type":"X","name":"x","display_name":"","last_post_at":0,"total_msg_count":0}`,
		`{"id":"x","team_id":"","type":"O","name":"x","display_name":"","last_post_at":0,"total_msg_count":0}`,
		`{"id":"x","team_id":"team","type":"G","name":"x","display_name":"","last_post_at":0,"total_msg_count":0}`,
		`{"id":"x","team_id":" ","type":"G","name":"x","display_name":"","last_post_at":0,"total_msg_count":0}`,
		`{"id":"x","team_id":" ","type":"D","name":"a__b","display_name":"","last_post_at":0,"total_msg_count":0}`,
		`{"id":"x","team_id":"","type":"D","name":"alice","display_name":"","last_post_at":0,"total_msg_count":0}`,
	}
	for _, payload := range bad {
		f := &fakeChannelTransport{responses: map[string]string{"/channels/x": payload}}
		_, err := NewChannels(f).ByID(context.Background(), "x")
		if !errors.Is(err, ErrInvalidChannelResponse) {
			t.Fatalf("payload %s: error = %v", payload, err)
		}
		if contains(err.Error(), "remote-secret") {
			t.Fatalf("error reflected remote data: %v", err)
		}
	}
}

func TestChannelListBindsTeamsAndDirectAndGroupParticipants(t *testing.T) {
	f := &fakeChannelTransport{responses: map[string]string{
		"/users/user/teams": `[{"id":"team","name":"core","display_name":"Core","type":"O"}]`,
		"/users/user/channels": `[
			{"id":"public","team_id":"team","type":"O","name":"general","display_name":"General","last_post_at":11,"total_msg_count":12},
			{"id":"dm","team_id":"","type":"D","name":"user__alice","display_name":"","last_post_at":0,"total_msg_count":0},
			{"id":"group","team_id":"","type":"G","name":"opaque","display_name":"Crew","last_post_at":1,"total_msg_count":2},
			{"id":"public","team_id":"team","type":"O","name":"general","display_name":"General","last_post_at":11,"total_msg_count":12}
		]`,
	}}
	got, err := NewChannels(f).List(context.Background(), "user")
	if err != nil {
		t.Fatal(err)
	}
	if ids := []string{got[0].ID, got[1].ID, got[2].ID}; !reflect.DeepEqual(ids, []string{"dm", "group", "public"}) {
		t.Fatalf("IDs = %v", ids)
	}
	if got[2].LastPostAt != 11 || got[2].TotalMsgCount != 12 {
		t.Fatalf("channel metadata was not preserved: %+v", got[2])
	}
	if !reflect.DeepEqual(f.paths, []string{"/users/user/channels", "/users/user/teams"}) {
		t.Fatalf("account-wide discovery fanned out: %v", f.paths)
	}
	for _, path := range f.paths {
		if path == "/channels/direct" {
			t.Fatal("read path attempted channel creation")
		}
	}
}

func TestChannelListRejectsIncompleteBindings(t *testing.T) {
	for name, payload := range map[string]string{
		"foreign team":               `[{"id":"x","team_id":"other","type":"P","name":"private","display_name":"","last_post_at":0,"total_msg_count":0}]`,
		"foreign direct participant": `[{"id":"x","team_id":"","type":"D","name":"alice__bob","display_name":"","last_post_at":0,"total_msg_count":0}]`,
		"conflicting duplicate":      `[{"id":"x","team_id":"team","type":"O","name":"one","display_name":"","last_post_at":0,"total_msg_count":0},{"id":"x","team_id":"team","type":"O","name":"two","display_name":"","last_post_at":0,"total_msg_count":0}]`,
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakeChannelTransport{responses: map[string]string{
				"/users/user/teams":    `[{"id":"team","name":"core","display_name":"Core","type":"O"}]`,
				"/users/user/channels": payload,
			}}
			_, err := NewChannels(f).List(context.Background(), "user")
			if err == nil {
				t.Fatal("expected binding error")
			}
		})
	}
}

func TestChannelListAndMemberRejectAlias(t *testing.T) {
	channels := NewChannels(&fakeChannelTransport{})
	if _, err := channels.List(context.Background(), "me"); !errors.Is(err, ErrInvalidChannelRequest) {
		t.Fatalf("me alias error = %v", err)
	}
	if _, err := channels.Member(context.Background(), "channel", "me"); !errors.Is(err, ErrInvalidChannelRequest) {
		t.Fatalf("member alias error = %v", err)
	}
}

func TestChannelMemberIsProofOnlyAndIgnoresMetricFields(t *testing.T) {
	responses := map[string]string{
		"/channels/channel/members/user": `{"channel_id":"channel","user_id":"user"}`,
	}
	got, err := NewChannels(&fakeChannelTransport{responses: responses}).Member(context.Background(), "channel", "user")
	if err != nil || got.ChannelID != "channel" || got.UserID != "user" {
		t.Fatalf("member = %+v, error = %v", got, err)
	}
	payload := `{"channel_id":"channel","user_id":"user","msg_count":-1,"mention_count":-1,"last_viewed_at":-1}`
	f := &fakeChannelTransport{responses: map[string]string{"/channels/channel/members/user": payload}}
	if _, err := NewChannels(f).Member(context.Background(), "channel", "user"); err != nil {
		t.Fatalf("sanitized proof-only member error = %v", err)
	}
}

func TestUnreadMemberRequiresCompleteStrictMetrics(t *testing.T) {
	valid := `{"channel_id":"channel","user_id":"user","msg_count":1,"mention_count":2,"last_viewed_at":3}`
	f := &fakeChannelTransport{responses: map[string]string{"/channels/channel/members/user": valid}}
	got, err := NewChannels(f).UnreadMember(context.Background(), "channel", "user")
	if err != nil || got.MsgCount != 1 || got.MentionCount != 2 || got.LastViewedAt != 3 {
		t.Fatalf("member = %+v, error = %v", got, err)
	}
	for name, payload := range map[string]string{
		"missing":  `{"channel_id":"channel","user_id":"user"}`,
		"partial":  `{"channel_id":"channel","user_id":"user","msg_count":1}`,
		"negative": `{"channel_id":"channel","user_id":"user","msg_count":-1,"mention_count":0,"last_viewed_at":0}`,
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakeChannelTransport{responses: map[string]string{"/channels/channel/members/user": payload}}
			if _, err := NewChannels(f).UnreadMember(context.Background(), "channel", "user"); !errors.Is(err, ErrInvalidChannelResponse) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestTeamMembersReturnsExactBoundedSortedSnapshot(t *testing.T) {
	f := &fakeChannelTransport{responses: map[string]string{
		"/users/user%2Fone/teams/team%2Fone/channels/members": `[
			{"channel_id":"z","user_id":"user/one","msg_count":4,"mention_count":2,"last_viewed_at":9},
			{"channel_id":"a","user_id":"user/one","msg_count":1,"mention_count":0,"last_viewed_at":3}
		]`,
	}}
	got, err := NewChannels(f).TeamMembers(context.Background(), "user/one", "team/one")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ChannelID != "a" || got[1].ChannelID != "z" || got[1].MentionCount != 2 {
		t.Fatalf("members = %+v", got)
	}
	if !reflect.DeepEqual(f.paths, []string{"/users/user%2Fone/teams/team%2Fone/channels/members"}) {
		t.Fatalf("paths = %v", f.paths)
	}
}

func TestTeamMembersAcceptsEmptyCompleteSnapshot(t *testing.T) {
	f := &fakeChannelTransport{responses: map[string]string{
		"/users/user/teams/team/channels/members": `[]`,
	}}
	got, err := NewChannels(f).TeamMembers(context.Background(), "user", "team")
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("members = %#v, error = %v", got, err)
	}
}

func TestTeamMembersRequiresCompleteBoundSnapshot(t *testing.T) {
	tests := map[string]string{
		"null list":            `null`,
		"object":               `{}`,
		"missing metric":       `[{"channel_id":"a","user_id":"user","msg_count":1,"mention_count":0}]`,
		"foreign user":         `[{"channel_id":"a","user_id":"other","msg_count":1,"mention_count":0,"last_viewed_at":0}]`,
		"noncanonical channel": `[{"channel_id":" a","user_id":"user","msg_count":1,"mention_count":0,"last_viewed_at":0}]`,
		"duplicate channel":    `[{"channel_id":"a","user_id":"user","msg_count":1,"mention_count":0,"last_viewed_at":0},{"channel_id":"a","user_id":"user","msg_count":1,"mention_count":0,"last_viewed_at":0}]`,
	}
	for name, payload := range tests {
		t.Run(name, func(t *testing.T) {
			f := &fakeChannelTransport{responses: map[string]string{"/users/user/teams/team/channels/members": payload}}
			_, err := NewChannels(f).TeamMembers(context.Background(), "user", "team")
			if !errors.Is(err, ErrInvalidChannelsResponse) {
				t.Fatalf("error = %v", err)
			}
			if err != nil && strings.Contains(err.Error(), "other") {
				t.Fatalf("error reflected remote data: %v", err)
			}
		})
	}
}

func TestTeamMembersRejectsNoncanonicalRequestsAndCancellation(t *testing.T) {
	channels := NewChannels(&fakeChannelTransport{})
	for _, ids := range [][2]string{{"", "team"}, {"me", "team"}, {" user", "team"}, {"user", "team "}} {
		if _, err := channels.TeamMembers(context.Background(), ids[0], ids[1]); !errors.Is(err, ErrInvalidChannelRequest) {
			t.Fatalf("ids=%q: error = %v", ids, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := &fakeChannelTransport{responses: map[string]string{"/users/user/teams/team/channels/members": `[]`}}
	if _, err := NewChannels(f).TeamMembers(ctx, "user", "team"); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	if len(f.paths) != 0 {
		t.Fatalf("canceled read reached transport: %v", f.paths)
	}
}

func TestTeamMembersIsRaceSafe(t *testing.T) {
	f := &fakeChannelTransport{responses: map[string]string{
		"/users/user/teams/team/channels/members": `[{"channel_id":"a","user_id":"user","msg_count":1,"mention_count":0,"last_viewed_at":0}]`,
	}}
	channels := NewChannels(f)
	var wg sync.WaitGroup
	for range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = channels.TeamMembers(context.Background(), "user", "team")
		}()
	}
	wg.Wait()
}

func TestChannelReadsPropagateCancellationWithoutFanout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := &fakeChannelTransport{responses: map[string]string{"/users/user/channels": `[]`}}
	if _, err := NewChannels(f).List(ctx, "user"); !errors.Is(err, context.Canceled) {
		t.Fatalf("List error = %v", err)
	}
	if len(f.paths) != 0 {
		t.Fatalf("canceled read reached transport paths: %v", f.paths)
	}
}

func TestChannelListDoesNotRequireTeamsForDirectOnlyDiscovery(t *testing.T) {
	f := &fakeChannelTransport{responses: map[string]string{
		"/users/user/channels": `[{"id":"dm","team_id":"","type":"D","name":"user__other","display_name":"","last_post_at":0,"total_msg_count":0}]`,
	}}
	got, err := NewChannels(f).List(context.Background(), "user")
	if err != nil || len(got) != 1 || got[0].ID != "dm" {
		t.Fatalf("channels = %#v, error = %v", got, err)
	}
	if !reflect.DeepEqual(f.paths, []string{"/users/user/channels"}) {
		t.Fatalf("paths = %v", f.paths)
	}
}

func TestListSelectedFiltersBeforeOneExactTeamProof(t *testing.T) {
	public := `{"id":"public","team_id":"team","type":"O","name":"general","display_name":"General"}`
	f := &fakeChannelTransport{responses: map[string]string{
		"/users/user/channels": `[` + public + `,{"type":"P","malformed":true},{"id":"dm","team_id":"","type":"D","name":"user__other","display_name":""}]`,
		"/users/user/teams":    `[{"id":"team","name":"core","display_name":"Core","type":"O"}]`,
	}}
	selection, err := NewChannels(f).ListSelected(context.Background(), "user", "O")
	if err != nil || len(selection.Channels) != 1 || selection.Channels[0].ID != "public" || len(selection.Membership.Items()) != 1 {
		t.Fatalf("selection=%+v err=%v", selection, err)
	}
	if !reflect.DeepEqual(f.paths, []string{"/users/user/channels", "/users/user/teams"}) {
		t.Fatalf("paths=%v", f.paths)
	}
}

func TestListSelectedEmptyOppositeFilterDoesNotReadTeams(t *testing.T) {
	f := &fakeChannelTransport{responses: map[string]string{"/users/user/channels": `[{"type":"P","malformed":true},{"id":"d","team_id":"","type":"D","name":"user__other","display_name":""}]`}}
	selection, err := NewChannels(f).ListSelected(context.Background(), "user", "O")
	if err != nil || selection.Channels == nil || len(selection.Channels) != 0 || len(selection.Membership.Items()) != 0 {
		t.Fatalf("selection=%+v err=%v", selection, err)
	}
	if !reflect.DeepEqual(f.paths, []string{"/users/user/channels"}) {
		t.Fatalf("paths=%v", f.paths)
	}
}

func TestListSelectedRejectsMalformedDiscardedDiscriminatorAndSelectedDuplicates(t *testing.T) {
	for name, payload := range map[string]string{
		"missing discriminator":          `[{"id":"discarded"}]`,
		"unknown discriminator":          `[{"type":"X"}]`,
		"conflicting selected duplicate": `[{"id":"d","team_id":"","type":"D","name":"user__a","display_name":""},{"id":"d","team_id":"","type":"D","name":"user__b","display_name":""}]`,
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakeChannelTransport{responses: map[string]string{"/users/user/channels": payload}}
			if _, err := NewChannels(f).ListSelected(context.Background(), "user", "D"); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestListSelectedAllUsesOneTeamProof(t *testing.T) {
	f := &fakeChannelTransport{responses: map[string]string{
		"/users/user/channels": `[{"id":"o","team_id":"team","type":"O","name":"general","display_name":""},{"id":"d","team_id":"","type":"D","name":"user__other","display_name":""}]`,
		"/users/user/teams":    `[{"id":"team","name":"core","type":"O"}]`,
	}}
	selection, err := NewChannels(f).ListSelected(context.Background(), "user", "O", "P", "D", "G")
	if err != nil || len(selection.Channels) != 2 || len(selection.Membership.Items()) != 1 {
		t.Fatalf("selection=%+v err=%v", selection, err)
	}
	if !reflect.DeepEqual(f.paths, []string{"/users/user/channels", "/users/user/teams"}) {
		t.Fatalf("paths=%v", f.paths)
	}
}

func TestListForUnreadRequiresPresentTotalsWithoutFanout(t *testing.T) {
	for name, payload := range map[string]string{
		"missing": `[{"id":"remote-secret","team_id":"","type":"D","name":"user__other","display_name":""}]`,
		"present": `[{"id":"dm","team_id":"","type":"D","name":"user__other","display_name":"","total_msg_count":0}]`,
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakeChannelTransport{responses: map[string]string{"/users/user/channels": payload}}
			got, err := NewChannels(f).ListForUnread(context.Background(), "user")
			if name == "missing" {
				if !errors.Is(err, ErrInvalidChannelsResponse) || strings.Contains(err.Error(), "remote-secret") {
					t.Fatalf("channels = %#v, error = %v", got, err)
				}
			} else if err != nil || len(got) != 1 || got[0].TotalMsgCount != 0 {
				t.Fatalf("channels = %#v, error = %v", got, err)
			}
			if !reflect.DeepEqual(f.paths, []string{"/users/user/channels"}) {
				t.Fatalf("paths = %v", f.paths)
			}
		})
	}
}

func TestListForUnreadAcceptsEmptyCompleteSnapshot(t *testing.T) {
	f := &fakeChannelTransport{responses: map[string]string{"/users/user/channels": `[]`}}
	got, err := NewChannels(f).ListForUnread(context.Background(), "user")
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("channels = %#v, error = %v", got, err)
	}
}

func TestDirectListIgnoresUnrelatedTeamAndGroupBindings(t *testing.T) {
	f := &fakeChannelTransport{responses: map[string]string{
		"/users/user/channels": `[
			{"type":"P"},
			{"type":"G"},
			{"id":"dm","team_id":"","type":"D","name":"user__other","display_name":"","last_post_at":0,"total_msg_count":0}
		]`,
	}}
	got, err := NewChannels(f).DirectList(context.Background(), "user")
	if err != nil || len(got) != 1 || got[0].ID != "dm" {
		t.Fatalf("channels = %#v, error = %v", got, err)
	}
	if !reflect.DeepEqual(f.paths, []string{"/users/user/channels"}) {
		t.Fatalf("paths = %v", f.paths)
	}
}

func TestDirectListRejectsMalformedOrUnboundDirectChannels(t *testing.T) {
	tests := map[string]string{
		"malformed direct identity": `[{"id":"dm","team_id":"","type":"D","display_name":""}]`,
		"foreign direct identity":   `[{"id":"dm","team_id":"","type":"D","name":"alice__bob","display_name":"","last_post_at":0,"total_msg_count":0}]`,
		"conflicting duplicate":     `[{"id":"dm","team_id":"","type":"D","name":"user__alice","display_name":"","last_post_at":0,"total_msg_count":0},{"id":"dm","team_id":"","type":"D","name":"user__bob","display_name":"","last_post_at":0,"total_msg_count":0}]`,
		"missing discriminator":     `[{"id":"dm"}]`,
		"unknown discriminator":     `[{"type":"X"}]`,
	}
	for name, payload := range tests {
		t.Run(name, func(t *testing.T) {
			f := &fakeChannelTransport{responses: map[string]string{"/users/user/channels": payload}}
			if _, err := NewChannels(f).DirectList(context.Background(), "user"); err == nil {
				t.Fatal("expected direct-list validation error")
			}
		})
	}
}

func TestDirectListDedupesExactDirectChannelDuplicates(t *testing.T) {
	channel := `{"id":"dm","team_id":"","type":"D","name":"user__other","display_name":"","last_post_at":0,"total_msg_count":0}`
	f := &fakeChannelTransport{responses: map[string]string{"/users/user/channels": `[` + channel + `,` + channel + `]`}}
	got, err := NewChannels(f).DirectList(context.Background(), "user")
	if err != nil || len(got) != 1 || got[0].ID != "dm" {
		t.Fatalf("channels=%#v err=%v", got, err)
	}
}

func TestExistingDirectFindsExactPairInEitherOrder(t *testing.T) {
	for _, name := range []string{"user__peer", "peer__user"} {
		t.Run(name, func(t *testing.T) {
			payload := `[{"id":"other","team_id":"","type":"D","name":"user__someone","display_name":""},{"id":"wanted","team_id":"","type":"D","name":"` + name + `","display_name":""}]`
			f := &fakeChannelTransport{responses: map[string]string{"/users/user/channels": payload}}
			got, found, err := NewChannels(f).ExistingDirect(context.Background(), "user", "peer")
			if err != nil || !found || got.ID != "wanted" {
				t.Fatalf("channel=%#v found=%v error=%v", got, found, err)
			}
			if !reflect.DeepEqual(f.paths, []string{"/users/user/channels"}) {
				t.Fatalf("paths=%v", f.paths)
			}
		})
	}
}

func TestExistingDirectReturnsCleanNone(t *testing.T) {
	f := &fakeChannelTransport{responses: map[string]string{"/users/user/channels": `[{"id":"other","team_id":"","type":"D","name":"user__someone","display_name":""}]`}}
	got, found, err := NewChannels(f).ExistingDirect(context.Background(), "user", "peer")
	if err != nil || found || got != (Channel{}) {
		t.Fatalf("channel=%#v found=%v error=%v", got, found, err)
	}
}

func TestExistingDirectPreservesCanonicalSelfDM(t *testing.T) {
	f := &fakeChannelTransport{responses: map[string]string{"/users/user/channels": `[{"id":"self","team_id":"","type":"D","name":"user__user","display_name":""}]`}}
	got, found, err := NewChannels(f).ExistingDirect(context.Background(), "user", "user")
	if err != nil || !found || got.ID != "self" {
		t.Fatalf("channel=%#v found=%v error=%v", got, found, err)
	}
}

func TestExistingDirectRejectsNonCanonicalMatchingChannelID(t *testing.T) {
	f := &fakeChannelTransport{responses: map[string]string{"/users/user/channels": `[{"id":" bad ","team_id":"","type":"D","name":"user__peer","display_name":""}]`}}
	if _, _, err := NewChannels(f).ExistingDirect(context.Background(), "user", "peer"); !errors.Is(err, ErrInvalidChannelsResponse) {
		t.Fatalf("error=%v", err)
	}
}

func TestExistingDirectRejectsSelfInvalidAndDuplicateMatches(t *testing.T) {
	for _, ids := range [][2]string{{"me", "peer"}, {"user", "me"}, {" user", "peer"}, {"user", ""}} {
		f := &fakeChannelTransport{responses: map[string]string{}}
		if _, _, err := NewChannels(f).ExistingDirect(context.Background(), ids[0], ids[1]); !errors.Is(err, ErrInvalidChannelRequest) || len(f.paths) != 0 {
			t.Fatalf("ids=%q error=%v paths=%v", ids, err, f.paths)
		}
	}
	for name, payload := range map[string]string{
		"identical row": `[{"id":"one","team_id":"","type":"D","name":"user__peer","display_name":""},{"id":"one","team_id":"","type":"D","name":"user__peer","display_name":""}]`,
		"distinct IDs":  `[{"id":"one","team_id":"","type":"D","name":"user__peer","display_name":""},{"id":"two","team_id":"","type":"D","name":"peer__user","display_name":""}]`,
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakeChannelTransport{responses: map[string]string{"/users/user/channels": payload}}
			if _, _, err := NewChannels(f).ExistingDirect(context.Background(), "user", "peer"); !errors.Is(err, ErrInvalidChannelsResponse) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestGroupListUsesCanonicalListingAsBoundedMembershipProof(t *testing.T) {
	group := `{"id":"group","team_id":"","type":"G","name":"opaque","display_name":"Crew","last_post_at":0,"total_msg_count":0}`
	f := &fakeChannelTransport{responses: map[string]string{
		"/users/user/channels": `[{"type":"P"},{"type":"D"},` + group + `,` + group + `]`,
	}}
	got, err := NewChannels(f).GroupList(context.Background(), "user")
	if err != nil || len(got) != 1 || got[0].ID != "group" {
		t.Fatalf("channels=%#v err=%v", got, err)
	}
	if !reflect.DeepEqual(f.paths, []string{"/users/user/channels"}) {
		t.Fatalf("paths=%v", f.paths)
	}
}

func TestExistingGroupFindsExactCanonicalPeerSet(t *testing.T) {
	wanted := canonicalGroupChannelName([]string{"self", "a", "b"})
	f := &fakeChannelTransport{responses: map[string]string{
		"/users/self/channels":                       `[{"id":"other","team_id":"","type":"G","name":"opaque","display_name":"Other"},{"id":"wanted","team_id":"","type":"G","name":"` + wanted + `","display_name":"A, B"}]`,
		"/channels/wanted/members?page=0&per_page=9": `[{"channel_id":"wanted","user_id":"b"},{"channel_id":"wanted","user_id":"self"},{"channel_id":"wanted","user_id":"a"}]`,
	}}
	got, found, err := NewChannels(f).ExistingGroup(context.Background(), "self", []string{"b", "a"})
	if err != nil || !found || got.ID != "wanted" {
		t.Fatalf("channel=%#v found=%v error=%v", got, found, err)
	}
	if !reflect.DeepEqual(f.paths, []string{"/users/self/channels", "/channels/wanted/members?page=0&per_page=9"}) {
		t.Fatalf("paths=%v", f.paths)
	}
}

func TestExistingGroupRejectsStaleNameWithPartialLiveMembership(t *testing.T) {
	wanted := canonicalGroupChannelName([]string{"self", "a", "b"})
	f := &fakeChannelTransport{responses: map[string]string{
		"/users/self/channels":                       `[{"id":"wanted","team_id":"","type":"G","name":"` + wanted + `","display_name":"A, B"}]`,
		"/channels/wanted/members?page=0&per_page=9": `[{"channel_id":"wanted","user_id":"self"},{"channel_id":"wanted","user_id":"a"}]`,
	}}
	if _, _, err := NewChannels(f).ExistingGroup(context.Background(), "self", []string{"a", "b"}); !errors.Is(err, ErrInvalidChannelsResponse) {
		t.Fatalf("error=%v", err)
	}
}

func TestExistingGroupReturnsNoneAndRejectsAmbiguousOrInvalidSets(t *testing.T) {
	wanted := canonicalGroupChannelName([]string{"self", "a", "b"})
	for name, payload := range map[string]string{
		"none":      `[{"id":"other","team_id":"","type":"G","name":"opaque","display_name":"Other"}]`,
		"ambiguous": `[{"id":"one","team_id":"","type":"G","name":"` + wanted + `","display_name":"A"},{"id":"two","team_id":"","type":"G","name":"` + wanted + `","display_name":"B"}]`,
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakeChannelTransport{responses: map[string]string{"/users/self/channels": payload}}
			got, found, err := NewChannels(f).ExistingGroup(context.Background(), "self", []string{"a", "b"})
			if name == "none" && (err != nil || found || got != (Channel{})) {
				t.Fatalf("channel=%#v found=%v error=%v", got, found, err)
			}
			if name == "ambiguous" && !errors.Is(err, ErrInvalidChannelsResponse) {
				t.Fatalf("error=%v", err)
			}
		})
	}
	for _, peers := range [][]string{{"a"}, {"a", "a"}, {"self", "a"}, {"a", "../b"}, {"a", "b", "c", "d", "e", "f", "g", "h"}} {
		f := &fakeChannelTransport{}
		if _, _, err := NewChannels(f).ExistingGroup(context.Background(), "self", peers); !errors.Is(err, ErrInvalidChannelRequest) || len(f.paths) != 0 {
			t.Fatalf("peers=%q error=%v paths=%v", peers, err, f.paths)
		}
	}
}

func TestGroupListRejectsMalformedFocusedChannelsAndMembership(t *testing.T) {
	for name, payload := range map[string]string{
		"malformed group":       `[{"id":"group","team_id":"","type":"G","display_name":"Crew"}]`,
		"conflicting duplicate": `[{"id":"group","team_id":"","type":"G","name":"one","display_name":"","last_post_at":0,"total_msg_count":0},{"id":"group","team_id":"","type":"G","name":"two","display_name":"","last_post_at":0,"total_msg_count":0}]`,
		"foreign team binding":  `[{"id":"group","team_id":"foreign","type":"G","name":"one","display_name":"","last_post_at":0,"total_msg_count":0}]`,
		"missing discriminator": `[{"id":"ignored"}]`,
		"unknown discriminator": `[{"type":"X"}]`,
	} {
		t.Run(name, func(t *testing.T) {
			f := &fakeChannelTransport{responses: map[string]string{"/users/user/channels": payload}}
			if _, err := NewChannels(f).GroupList(context.Background(), "user"); err == nil {
				t.Fatal("expected group-list validation error")
			}
		})
	}
}

func TestChannelReadsAreRaceSafe(t *testing.T) {
	f := &fakeChannelTransport{responses: map[string]string{
		"/channels/x":          `{"id":"x","team_id":"team","type":"O","name":"general","display_name":"General","last_post_at":0,"total_msg_count":0}`,
		"/users/user/channels": `[{"id":"dm","team_id":"","type":"D","name":"user__peer","display_name":""}]`,
	}}
	channels := NewChannels(f)
	var wg sync.WaitGroup
	for range 40 {
		wg.Add(2)
		go func() { defer wg.Done(); _, _ = channels.ByID(context.Background(), "x") }()
		go func() { defer wg.Done(); _, _, _ = channels.ExistingDirect(context.Background(), "user", "peer") }()
	}
	wg.Wait()
}
