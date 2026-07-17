package staging

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ardasevinc/mattermost-cli/internal/mattermost"
	"github.com/ardasevinc/mattermost-cli/internal/stagestore"
)

type directoryUsers struct {
	current mattermost.User
	byName  map[string]mattermost.User
	calls   atomic.Int64
}

func (d *directoryUsers) Current(context.Context) (mattermost.User, error) { return d.current, nil }
func (d *directoryUsers) ByUsernameFresh(_ context.Context, username string) (mattermost.User, error) {
	d.calls.Add(1)
	user, ok := d.byName[strings.ToLower(username)]
	if !ok {
		return mattermost.User{}, errors.New("missing")
	}
	return user, nil
}

func conversationStageService(t *testing.T, users Users, store Store, credentials []string) *Service {
	t.Helper()
	service, err := New("https://mattermost.example", "", credentials, users, &fakeChannels{}, emptyTeams{}, emptyPosts{}, store)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func TestResolveDMPersistsUnresolvedExactParticipantPlan(t *testing.T) {
	store := new(recordingStore)
	users := &directoryUsers{current: mattermost.User{ID: "self", Username: "arda"}, byName: map[string]mattermost.User{"hakan": {ID: "peer", Username: "Hakan"}}}
	service := conversationStageService(t, users, store, nil)
	result, err := service.ResolveDM(t.Context(), ResolveDMInput{RequestID: "dm-1", Target: Target{Conversation: Direct, Selector: ByUsername, Value: "hakan"}})
	if err != nil {
		t.Fatal(err)
	}
	if store.calls != 1 || store.in.Operation != stagestore.ResolveDM || result.Preview.UserID != "self" || result.Preview.Destination.ChannelID != "" || result.Preview.Destination.ChannelType != "dm" || !reflect.DeepEqual(result.Preview.Destination.ParticipantIDs, []string{"peer"}) {
		t.Fatalf("store=%+v preview=%+v", store.in, result.Preview)
	}
	wantDestination := `{"kind":"conversation","channelId":null,"channelType":"dm","teamId":null,"postId":null,"rootPostId":null,"participantIds":["peer"],"emoji":null,"postState":null,"reactionPresent":null}`
	wantPlan := `{"steps":[{"ordinal":1,"type":"resolve_conversation","condition":"if_missing"}]}`
	if string(store.in.Content.Destination) != wantDestination || string(store.in.Content.Plan) != wantPlan || store.in.Content.Body != nil || len(store.in.Content.Attachments) != 0 {
		t.Fatalf("destination=%s plan=%s", store.in.Content.Destination, store.in.Content.Plan)
	}
	var roundTrip Destination
	decoder := json.NewDecoder(bytes.NewReader(store.in.Content.Destination))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&roundTrip); err != nil || !reflect.DeepEqual(roundTrip, result.Preview.Destination) {
		t.Fatalf("roundtrip=%+v err=%v", roundTrip, err)
	}
}

func TestResolveGroupCanonicalizesParticipantIDsAndRejectsAmbiguity(t *testing.T) {
	users := &directoryUsers{current: mattermost.User{ID: "self", Username: "arda"}, byName: map[string]mattermost.User{
		"alice": {ID: "z-peer", Username: "alice"}, "bob": {ID: "a-peer", Username: "bob"},
	}}
	store := new(recordingStore)
	service := conversationStageService(t, users, store, nil)
	result, err := service.ResolveGroup(t.Context(), ResolveGroupInput{RequestID: "group-1", Usernames: []string{"alice", "bob"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Preview.Destination.ChannelType != "group" || !reflect.DeepEqual(result.Preview.Destination.ParticipantIDs, []string{"a-peer", "z-peer"}) || store.in.Operation != stagestore.ResolveGroupDM {
		t.Fatalf("preview=%+v operation=%s", result.Preview, store.in.Operation)
	}
	before := users.calls.Load()
	if _, err := service.ResolveGroup(t.Context(), ResolveGroupInput{Usernames: []string{"Alice", "alice"}}); !errors.Is(err, ErrInvalid) || users.calls.Load() != before {
		t.Fatalf("duplicate error=%v calls=%d->%d", err, before, users.calls.Load())
	}
	users.byName["alias"] = mattermost.User{ID: "a-peer", Username: "alias"}
	if _, err := service.DryRunResolveGroup(t.Context(), []string{"bob", "alias"}); !errors.Is(err, ErrTarget) {
		t.Fatalf("duplicate identity error=%v", err)
	}
}

func TestResolveGroupEnforcesMattermostPeerLimitsBeforeLookup(t *testing.T) {
	users := &directoryUsers{current: mattermost.User{ID: "self", Username: "arda"}, byName: make(map[string]mattermost.User)}
	seven := make([]string, 7)
	for index := range seven {
		username := fmt.Sprintf("peer-%d", index)
		seven[index] = username
		users.byName[username] = mattermost.User{ID: fmt.Sprintf("user-%d", index), Username: username}
	}
	service := conversationStageService(t, users, new(recordingStore), nil)
	if _, err := service.DryRunResolveGroup(t.Context(), seven); err != nil {
		t.Fatalf("seven peers rejected: %v", err)
	}
	before := users.calls.Load()
	tooMany := append(append([]string(nil), seven...), "peer-7")
	if _, err := service.DryRunResolveGroup(t.Context(), tooMany); !errors.Is(err, ErrInvalid) {
		t.Fatalf("eight peers error=%v", err)
	}
	if got := users.calls.Load(); got != before {
		t.Fatalf("invalid request performed lookups: %d -> %d", before, got)
	}
}

func TestConversationCreateCredentialAndSelfTargetsFailClosed(t *testing.T) {
	users := &directoryUsers{current: mattermost.User{ID: "self", Username: "arda"}, byName: map[string]mattermost.User{
		"arda": {ID: "self", Username: "arda"}, "token": {ID: "peer", Username: "active-token"},
	}}
	store := new(recordingStore)
	service := conversationStageService(t, users, store, []string{"active-token"})
	if _, err := service.ResolveDM(t.Context(), ResolveDMInput{Target: Target{Conversation: Direct, Selector: ByUsername, Value: "arda"}}); !errors.Is(err, ErrTarget) {
		t.Fatalf("self error=%v", err)
	}
	if _, err := service.ResolveDM(t.Context(), ResolveDMInput{Target: Target{Conversation: Direct, Selector: ByUsername, Value: "active-token"}}); !errors.Is(err, ErrCredential) {
		t.Fatalf("credential error=%v", err)
	}
	if store.calls != 0 {
		t.Fatalf("persisted=%d", store.calls)
	}
}

func TestDestinationRejectsMissingAndUnknownNullableFields(t *testing.T) {
	valid := `{"kind":"conversation","channelId":null,"channelType":"dm","teamId":null,"postId":null,"rootPostId":null,"participantIds":["peer"],"emoji":null,"postState":null,"reactionPresent":null}`
	for name, value := range map[string]string{
		"missing": strings.Replace(valid, `"channelId":null,`, "", 1),
		"unknown": strings.Replace(valid, `"kind":"conversation"`, `"kind":"conversation","extra":true`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			var destination Destination
			if err := json.Unmarshal([]byte(value), &destination); err == nil {
				t.Fatalf("accepted %s", value)
			}
		})
	}
}
