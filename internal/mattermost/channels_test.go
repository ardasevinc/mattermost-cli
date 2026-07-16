package mattermost

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
)

type fakeChannelTransport struct {
	mu        sync.Mutex
	responses map[string]string
	paths     []string
}

func (f *fakeChannelTransport) Get(_ context.Context, path string, out any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paths = append(f.paths, path)
	payload, ok := f.responses[path]
	if !ok {
		return errors.New("unexpected request")
	}
	return json.Unmarshal([]byte(payload), out)
}

func TestChannelLookupsEncodeAndRequireExactIdentity(t *testing.T) {
	f := &fakeChannelTransport{responses: map[string]string{
		"/channels/channel%2Fone":                        `{"id":"channel/one","team_id":"team/one","type":"P","name":"release/name","display_name":"Release"}`,
		"/teams/team%2Fone/channels/name/release%2Fname": `{"id":"channel/one","team_id":"team/one","type":"P","name":"release/name","display_name":"Release"}`,
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
		`null`, `{}`, `{"id":"remote-secret","team_id":"","type":"X","name":"x","display_name":""}`,
		`{"id":"x","team_id":"","type":"O","name":"x","display_name":""}`,
		`{"id":"x","team_id":"team","type":"G","name":"x","display_name":""}`,
		`{"id":"x","team_id":" ","type":"G","name":"x","display_name":""}`,
		`{"id":"x","team_id":" ","type":"D","name":"a__b","display_name":""}`,
		`{"id":"x","team_id":"","type":"D","name":"alice","display_name":""}`,
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
			{"id":"public","team_id":"team","type":"O","name":"general","display_name":"General"},
			{"id":"dm","team_id":"","type":"D","name":"user__alice","display_name":""},
			{"id":"group","team_id":"","type":"G","name":"opaque","display_name":"Crew"},
			{"id":"public","team_id":"team","type":"O","name":"general","display_name":"General"}
		]`,
		"/channels/group/members/user": `{"channel_id":"group","user_id":"user","roles":"channel_user"}`,
	}}
	got, err := NewChannels(f).List(context.Background(), "user")
	if err != nil {
		t.Fatal(err)
	}
	if ids := []string{got[0].ID, got[1].ID, got[2].ID}; !reflect.DeepEqual(ids, []string{"dm", "group", "public"}) {
		t.Fatalf("IDs = %v", ids)
	}
	for _, path := range f.paths {
		if path == "/channels/direct" {
			t.Fatal("read path attempted channel creation")
		}
	}
}

func TestChannelListRejectsIncompleteBindings(t *testing.T) {
	for name, payload := range map[string]string{
		"foreign team":               `[{"id":"x","team_id":"other","type":"P","name":"private","display_name":""}]`,
		"foreign direct participant": `[{"id":"x","team_id":"","type":"D","name":"alice__bob","display_name":""}]`,
		"conflicting duplicate":      `[{"id":"x","team_id":"team","type":"O","name":"one","display_name":""},{"id":"x","team_id":"team","type":"O","name":"two","display_name":""}]`,
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
	f := &fakeChannelTransport{responses: map[string]string{
		"/users/user/channels":         `[{"id":"group","team_id":"","type":"G","name":"opaque","display_name":""}]`,
		"/channels/group/members/user": `{"channel_id":"other","user_id":"user"}`,
	}}
	if _, err := NewChannels(f).List(context.Background(), "user"); !errors.Is(err, ErrInvalidChannelResponse) {
		t.Fatalf("group binding error = %v", err)
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

func TestChannelListDoesNotRequireTeamsForDirectOnlyDiscovery(t *testing.T) {
	f := &fakeChannelTransport{responses: map[string]string{
		"/users/user/channels": `[{"id":"dm","team_id":"","type":"D","name":"user__other","display_name":""}]`,
	}}
	got, err := NewChannels(f).List(context.Background(), "user")
	if err != nil || len(got) != 1 || got[0].ID != "dm" {
		t.Fatalf("channels = %#v, error = %v", got, err)
	}
	if !reflect.DeepEqual(f.paths, []string{"/users/user/channels"}) {
		t.Fatalf("paths = %v", f.paths)
	}
}

func TestDirectListIgnoresUnrelatedTeamAndGroupBindings(t *testing.T) {
	f := &fakeChannelTransport{responses: map[string]string{
		"/users/user/channels": `[
			{"type":"P"},
			{"type":"G"},
			{"id":"dm","team_id":"","type":"D","name":"user__other","display_name":""}
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
		"foreign direct identity":   `[{"id":"dm","team_id":"","type":"D","name":"alice__bob","display_name":""}]`,
		"conflicting duplicate":     `[{"id":"dm","team_id":"","type":"D","name":"user__alice","display_name":""},{"id":"dm","team_id":"","type":"D","name":"user__bob","display_name":""}]`,
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
	channel := `{"id":"dm","team_id":"","type":"D","name":"user__other","display_name":""}`
	f := &fakeChannelTransport{responses: map[string]string{"/users/user/channels": `[` + channel + `,` + channel + `]`}}
	got, err := NewChannels(f).DirectList(context.Background(), "user")
	if err != nil || len(got) != 1 || got[0].ID != "dm" {
		t.Fatalf("channels=%#v err=%v", got, err)
	}
}

func TestChannelReadsAreRaceSafe(t *testing.T) {
	f := &fakeChannelTransport{responses: map[string]string{
		"/channels/x": `{"id":"x","team_id":"team","type":"O","name":"general","display_name":"General"}`,
	}}
	channels := NewChannels(f)
	var wg sync.WaitGroup
	for range 40 {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = channels.ByID(context.Background(), "x") }()
	}
	wg.Wait()
}
