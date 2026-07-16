package output_test

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/ardasevinc/mattermost-cli/internal/output"
	"github.com/ardasevinc/mattermost-cli/internal/presentation"
	"github.com/ardasevinc/mattermost-cli/internal/schema"
)

func TestIdentityDocumentsGoldenSchemaAndCredentialPresentation(t *testing.T) {
	options := presentation.Options{Credentials: []string{"token-secret"}, DisableHeuristics: true}
	values := []struct {
		id   string
		doc  output.MachineDocument
		want string
	}{
		{"mm/v2/whoami", mustWho(t, output.RawIdentity{ID: "u", Username: "arda", Roles: nil}, options), `{"schema":"mm/v2/whoami","data":{"id":"u","username":"arda","displayName":null,"nickname":null,"roles":[]}}`},
		{"mm/v2/teams", mustTeams(t, []output.RawTeam{{ID: "b", Name: "z", Type: "I"}, {ID: "a", Name: "a", DisplayName: "token-secret", Type: "O"}}, options), `{"schema":"mm/v2/teams","teams":[{"id":"a","name":"a","displayName":"[REDACTED:mattermost_credential]","type":"open"},{"id":"b","name":"z","displayName":null,"type":"invite_only"}]}`},
		{"mm/v2/users", mustUsers(t, nil, output.UsersRetrievalProof{RequestedLimit: 20}, options), `{"schema":"mm/v2/users","users":[],"retrieval":{"selectedCount":0,"requestedLimit":20,"query":null,"teamId":null,"truncated":false}}`},
		{"mm/v2/channels", mustChannels(t, []output.RawChannel{{ID: "old", Type: "G", Name: "group", LastPostAt: 0}, {ID: "new", Type: "D", Name: "raw", DirectUsername: "arda", LastPostAt: 1784197230123, TotalMsgCount: 7}}, options), `{"schema":"mm/v2/channels","channels":[{"id":"new","type":"dm","name":"@arda","displayName":null,"team":null,"lastPost":"2026-07-16T10:20:30.123Z","messageCount":7},{"id":"old","type":"group","name":"group","displayName":null,"team":null,"lastPost":null,"messageCount":0}]}`},
	}
	registry, err := schema.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range values {
		t.Run(test.id, func(t *testing.T) {
			var wire bytes.Buffer
			if _, err := output.WriteMachineJSON(&wire, test.doc); err != nil {
				t.Fatal(err)
			}
			if got := strings.TrimSuffix(wire.String(), "\n"); got != test.want {
				t.Fatalf("got %s\nwant %s", got, test.want)
			}
			if err := registry.Validate(test.id, bytes.NewReader(wire.Bytes())); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDirectNilAndMutationFailBeforeWrite(t *testing.T) {
	direct := []output.MachineDocument{output.WhoAmIEnvelope{Schema: "mm/v2/whoami"}, output.TeamsEnvelope{Schema: "mm/v2/teams"}, output.UsersEnvelope{Schema: "mm/v2/users"}, output.ChannelsEnvelope{Schema: "mm/v2/channels"}}
	for i, doc := range direct {
		w := &countingWriter{}
		if _, err := output.WriteMachineJSON(w, doc); err == nil || w.calls != 0 {
			t.Fatalf("direct %d err=%v writes=%d", i, err, w.calls)
		}
	}
	doc := mustTeams(t, []output.RawTeam{{ID: "t", Name: "core", Type: "O"}}, presentation.Options{})
	doc.Teams = nil
	w := &countingWriter{}
	if _, err := output.WriteMachineJSON(w, doc); err == nil || w.calls != 0 {
		t.Fatalf("mutation err=%v writes=%d", err, w.calls)
	}
}

func TestRawPresentationCannotBeFalselyBoundAndCollisionsRemainDeterministic(t *testing.T) {
	options := presentation.Options{Credentials: []string{"secret-a", "secret-b"}, DisableHeuristics: true}
	doc, err := output.NewUsersEnvelope([]output.RawUser{{ID: "secret-b", Username: "z"}, {ID: "secret-a", Username: "a"}}, output.UsersRetrievalProof{RequestedLimit: 2, ProbeCount: 2}, options)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Users[0].Username != "a" || doc.Users[0].ID != "[REDACTED:mattermost_credential]" || doc.Users[1].ID != doc.Users[0].ID {
		t.Fatalf("presentation/order=%+v", doc.Users)
	}
	doc.Users[0].ID = "unrelated"
	w := &countingWriter{}
	if _, err := output.WriteMachineJSON(w, doc); err == nil || w.calls != 0 {
		t.Fatalf("false binding survived err=%v writes=%d", err, w.calls)
	}
}

func TestDeterministicRawOrdering(t *testing.T) {
	teams := mustTeams(t, []output.RawTeam{{ID: "b", Name: "same", Type: "O"}, {ID: "a", Name: "same", Type: "O"}}, presentation.Options{})
	if teams.Teams[0].ID != "a" {
		t.Fatalf("teams=%+v", teams.Teams)
	}
	users := mustUsers(t, []output.RawUser{{ID: "b", Username: "same"}, {ID: "a", Username: "same"}}, output.UsersRetrievalProof{RequestedLimit: 2, ProbeCount: 2}, presentation.Options{})
	if users.Users[0].ID != "a" {
		t.Fatalf("users=%+v", users.Users)
	}
	channels := mustChannels(t, []output.RawChannel{{ID: "z", Type: "G", Name: "z", LastPostAt: 0}, {ID: "b", Type: "G", Name: "b", LastPostAt: 1}, {ID: "a", Type: "G", Name: "a", LastPostAt: 1}}, presentation.Options{})
	if channels.Channels[0].ID != "a" || channels.Channels[1].ID != "b" || channels.Channels[2].ID != "z" {
		t.Fatalf("channels=%+v", channels.Channels)
	}
}

func TestUsersRetrievalTruthTable(t *testing.T) {
	users := func(n int) []output.RawUser {
		result := make([]output.RawUser, n)
		for i := range result {
			value := fmt.Sprintf("user-%04d", i)
			result[i] = output.RawUser{ID: value, Username: value}
		}
		return result
	}
	tests := []struct {
		name  string
		n     int
		proof output.UsersRetrievalProof
		want  *bool
		ok    bool
	}{{"empty false", 0, output.UsersRetrievalProof{RequestedLimit: 20}, boolp(false), true}, {"proved more", 2, output.UsersRetrievalProof{RequestedLimit: 2, ProbeCount: 3}, boolp(true), true}, {"list ceiling unknown", 200, output.UsersRetrievalProof{RequestedLimit: 300, ProbeCount: 200}, nil, true}, {"query ceiling unknown", 1000, output.UsersRetrievalProof{RequestedLimit: 1000, ProbeCount: 1000, Query: "q"}, nil, true}, {"cannot claim true at ceiling", 200, output.UsersRetrievalProof{RequestedLimit: 200, ProbeCount: 200}, nil, true}, {"selected over limit", 2, output.UsersRetrievalProof{RequestedLimit: 1, ProbeCount: 2}, nil, false}, {"unsupported probe gap", 1, output.UsersRetrievalProof{RequestedLimit: 20, ProbeCount: 2}, nil, false}}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			doc, err := output.NewUsersEnvelope(users(test.n), test.proof, presentation.Options{})
			if (err == nil) != test.ok {
				t.Fatalf("err=%v", err)
			}
			if err == nil && !equalBool(doc.Retrieval.Truncated, test.want) {
				t.Fatalf("truncated=%v want=%v", doc.Retrieval.Truncated, test.want)
			}
		})
	}
}

func TestWhitespaceAndDMLabelsRejected(t *testing.T) {
	if _, err := output.NewWhoAmIEnvelope(output.RawIdentity{ID: " ", Username: "a"}, presentation.Options{}); err == nil {
		t.Fatal("accepted whitespace ID")
	}
	users, err := output.NewUsersEnvelope(nil, output.UsersRetrievalProof{RequestedLimit: 20, Query: " ", TeamID: "\t"}, presentation.Options{})
	if err != nil || users.Retrieval.Query != nil || users.Retrieval.TeamID != nil {
		t.Fatalf("blank optional retrieval values were not null: %+v err=%v", users.Retrieval, err)
	}
	for _, username := range []string{"", " ", "\t"} {
		if _, err := output.NewChannelsEnvelope([]output.RawChannel{{ID: "d", Type: "D", Name: "raw", DirectUsername: username}}, presentation.Options{}); err == nil {
			t.Fatalf("accepted DM username %q", username)
		}
	}
	doc := mustChannels(t, []output.RawChannel{{ID: "d", Type: "D", Name: "raw", DirectUsername: "arda"}}, presentation.Options{})
	if doc.Channels[0].Name != "@arda" || doc.Channels[0].DisplayName != nil || doc.Channels[0].Team != nil {
		t.Fatalf("dm=%+v", doc.Channels[0])
	}
}

func TestIdentityPresentationMakesControlsVisibleAndInvalidTimestampsNull(t *testing.T) {
	doc := mustTeams(t, []output.RawTeam{{ID: "t\nvalue", Name: "core\tname", DisplayName: "bad\u202ename", Type: "O"}}, presentation.Options{})
	if doc.Teams[0].ID != `t\nvalue` || doc.Teams[0].Name != `core\tname` || *doc.Teams[0].DisplayName != `bad\u202ename` {
		t.Fatalf("labels were not made visible: %+v", doc.Teams[0])
	}
	channels := mustChannels(t, []output.RawChannel{{ID: "negative", Type: "G", Name: "g", LastPostAt: -1}, {ID: "overflow", Type: "G", Name: "g", LastPostAt: 1 << 62}}, presentation.Options{})
	for _, channel := range channels.Channels {
		if channel.LastPost != nil {
			t.Fatalf("invalid timestamp retained: %+v", channel)
		}
	}
}

func TestChannelTimestampNormalizationPrecedesOrdering(t *testing.T) {
	doc := mustChannels(t, []output.RawChannel{
		{ID: "overflow", Type: "G", Name: "overflow", LastPostAt: 1 << 62},
		{ID: "valid", Type: "G", Name: "valid", LastPostAt: 1784197230123},
		{ID: "negative", Type: "G", Name: "negative", LastPostAt: -1},
		{ID: "zero", Type: "G", Name: "zero"},
	}, presentation.Options{})
	want := []string{"valid", "negative", "overflow", "zero"}
	for index, id := range want {
		if doc.Channels[index].ID != id {
			t.Fatalf("channels=%+v", doc.Channels)
		}
	}
	if doc.Channels[0].LastPost == nil {
		t.Fatal("valid timestamp became null")
	}
	for _, channel := range doc.Channels[1:] {
		if channel.LastPost != nil {
			t.Fatalf("invalid timestamp retained: %+v", channel)
		}
	}
}

func TestRawChannelTeamBindingAndGroupDisplayFallback(t *testing.T) {
	team := &output.RawTeam{ID: "team", Name: "core", Type: "O"}
	if _, err := output.NewChannelsEnvelope([]output.RawChannel{{ID: "c", Type: "O", Name: "general", TeamID: "other", Team: team}}, presentation.Options{}); err == nil {
		t.Fatal("accepted mismatched raw team binding")
	}
	doc := mustChannels(t, []output.RawChannel{{ID: "display", Type: "G", Name: "opaque", DisplayName: "Crew"}, {ID: "fallback", Type: "G", Name: "opaque"}}, presentation.Options{})
	if doc.Channels[0].Name != "Crew" || doc.Channels[0].DisplayName != nil || doc.Channels[1].Name != "opaque" {
		t.Fatalf("groups=%+v", doc.Channels)
	}
}

func TestUsersQueryIsTrimmedBeforeCeilingAndPresentation(t *testing.T) {
	doc := mustUsers(t, nil, output.UsersRetrievalProof{RequestedLimit: 20, Query: "  dev  "}, presentation.Options{})
	if doc.Retrieval.Query == nil || *doc.Retrieval.Query != "dev" || doc.Retrieval.Truncated == nil || *doc.Retrieval.Truncated {
		t.Fatalf("retrieval=%+v", doc.Retrieval)
	}
}

func TestWhoAmIRawRequiredFieldsRejectWhitespaceControls(t *testing.T) {
	for _, raw := range []output.RawIdentity{{ID: "\n", Username: "arda"}, {ID: "u", Username: "\t"}, {ID: "u", Username: "arda", Roles: []string{"\n\t"}}} {
		if _, err := output.NewWhoAmIEnvelope(raw, presentation.Options{}); err == nil {
			t.Fatalf("accepted raw identity %+v", raw)
		}
	}
}

func TestRawRequiredRejectsControlOnlyAcrossIdentityConsumers(t *testing.T) {
	for _, hazard := range []string{"\x00", "\x1b", "\u0085", "\u061c", "\u200e", "\u200f", "\u202e", "\u2066"} {
		t.Run(fmt.Sprintf("%x", []byte(hazard)), func(t *testing.T) {
			for _, raw := range []output.RawIdentity{{ID: hazard, Username: "arda"}, {ID: "u", Username: hazard}, {ID: "u", Username: "arda", Roles: []string{hazard}}} {
				if _, err := output.NewWhoAmIEnvelope(raw, presentation.Options{}); err == nil {
					t.Fatalf("accepted identity %+v", raw)
				}
			}
			if _, err := output.NewTeamsEnvelope([]output.RawTeam{{ID: hazard, Name: "core", Type: "O"}}, presentation.Options{}); err == nil {
				t.Fatal("accepted team ID")
			}
			if _, err := output.NewTeamsEnvelope([]output.RawTeam{{ID: "t", Name: hazard, Type: "O"}}, presentation.Options{}); err == nil {
				t.Fatal("accepted team name")
			}
			if _, err := output.NewUsersEnvelope([]output.RawUser{{ID: "u", Username: hazard}}, output.UsersRetrievalProof{RequestedLimit: 1, ProbeCount: 1}, presentation.Options{}); err == nil {
				t.Fatal("accepted username")
			}
			if _, err := output.NewChannelsEnvelope([]output.RawChannel{{ID: hazard, Type: "G", Name: "group"}}, presentation.Options{}); err == nil {
				t.Fatal("accepted channel ID")
			}
			if _, err := output.NewChannelsEnvelope([]output.RawChannel{{ID: "g", Type: "G", Name: hazard}}, presentation.Options{}); err == nil {
				t.Fatal("accepted channel name")
			}
			if _, err := output.NewChannelsEnvelope([]output.RawChannel{{ID: "d", Type: "D", Name: "raw", DirectUsername: hazard}}, presentation.Options{}); err == nil {
				t.Fatal("accepted DM username")
			}
		})
	}
}

func TestRawRequiredAllowsEmbeddedControlsAndSanitizesThem(t *testing.T) {
	doc := mustWho(t, output.RawIdentity{ID: "u\x00id", Username: "ar\x1bda", Roles: []string{"system\u202euser"}}, presentation.Options{})
	if doc.Data.ID != `u\u0000id` || doc.Data.Username != `ar\u001bda` || doc.Data.Roles[0] != `system\u202euser` {
		t.Fatalf("identity=%+v", doc.Data)
	}
	dm := mustChannels(t, []output.RawChannel{{ID: "d", Type: "D", Name: "raw", DirectUsername: "ar\u0085da"}}, presentation.Options{})
	if dm.Channels[0].Name != `@ar\u0085da` {
		t.Fatalf("DM label=%q", dm.Channels[0].Name)
	}
}

type countingWriter struct{ calls int }

func (w *countingWriter) Write(p []byte) (int, error) { w.calls++; return len(p), nil }
func boolp(v bool) *bool                              { return &v }
func equalBool(a, b *bool) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
func mustWho(t *testing.T, v output.RawIdentity, o presentation.Options) output.WhoAmIEnvelope {
	t.Helper()
	d, e := output.NewWhoAmIEnvelope(v, o)
	if e != nil {
		t.Fatal(e)
	}
	return d
}
func mustTeams(t *testing.T, v []output.RawTeam, o presentation.Options) output.TeamsEnvelope {
	t.Helper()
	d, e := output.NewTeamsEnvelope(v, o)
	if e != nil {
		t.Fatal(e)
	}
	return d
}
func mustUsers(t *testing.T, v []output.RawUser, p output.UsersRetrievalProof, o presentation.Options) output.UsersEnvelope {
	t.Helper()
	d, e := output.NewUsersEnvelope(v, p, o)
	if e != nil {
		t.Fatal(e)
	}
	return d
}
func mustChannels(t *testing.T, v []output.RawChannel, o presentation.Options) output.ChannelsEnvelope {
	t.Helper()
	d, e := output.NewChannelsEnvelope(v, o)
	if e != nil {
		t.Fatal(e)
	}
	return d
}
