package mattermost

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
)

type fakeTeamTransport struct {
	mu      sync.Mutex
	payload string
	paths   []string
}

func (f *fakeTeamTransport) Get(_ context.Context, path string, out any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paths = append(f.paths, path)
	return json.Unmarshal([]byte(f.payload), out)
}

func TestTeamsListValidatesEncodesSortsAndNarrows(t *testing.T) {
	f := &fakeTeamTransport{payload: `[
		{"id":"z","name":"beta","display_name":"","type":"I","email":"secret"},
		{"id":"b","name":"alpha","display_name":"B","type":"O"},
		{"id":"a","name":"alpha","display_name":"A","type":"O"}
	]`}
	got, err := NewTeams(f).List(context.Background(), "user/one space")
	if err != nil {
		t.Fatal(err)
	}
	want := []Team{{ID: "a", Name: "alpha", DisplayName: "A", Type: "O"}, {ID: "b", Name: "alpha", DisplayName: "B", Type: "O"}, {ID: "z", Name: "beta", Type: "I"}}
	if !reflect.DeepEqual(got.Items(), want) {
		t.Fatalf("teams = %#v, want %#v", got.Items(), want)
	}
	if f.paths[0] != "/users/user%2Fone%20space/teams" {
		t.Fatalf("path = %q", f.paths[0])
	}
}

func TestTeamsFailClosedWithoutReflectingRemoteValues(t *testing.T) {
	for _, payload := range []string{`null`, `{}`, `[{"id":"remote-secret","name":"x","display_name":"X","type":"X"}]`, `[{"id":"a","name":"x","display_name":7,"type":"O"}]`, `[{"id":"a","name":"x","display_name":"X","type":"O"},{"id":"a","name":"y","display_name":"Y","type":"I"}]`} {
		_, err := NewTeams(&fakeTeamTransport{payload: payload}).List(context.Background(), "user")
		if !errors.Is(err, ErrInvalidTeamResponse) && !errors.Is(err, ErrInvalidTeamsResponse) {
			t.Fatalf("payload %s: error = %v", payload, err)
		}
		if err != nil && contains(err.Error(), "remote-secret") {
			t.Fatalf("error reflected remote data: %v", err)
		}
	}
}

func TestResolveRequiresUniqueCompleteSelection(t *testing.T) {
	teams := NewTeams(&fakeTeamTransport{payload: `[{"id":"a","name":"core","display_name":"Shared","type":"O"},{"id":"b","name":"eng","display_name":"Shared","type":"I"}]`})
	if _, err := teams.Resolve(context.Background(), "user", ""); !errors.Is(err, ErrAmbiguousTeam) {
		t.Fatalf("empty selector error = %v", err)
	}
	if _, err := teams.Resolve(context.Background(), "user", "Shared"); !errors.Is(err, ErrAmbiguousTeam) {
		t.Fatalf("duplicate display name error = %v", err)
	}
	got, err := teams.Resolve(context.Background(), "user", "eng")
	if err != nil || got.ID != "b" {
		t.Fatalf("resolved = %+v, error = %v", got, err)
	}
}

func TestTeamsReadsAreRaceSafe(t *testing.T) {
	f := &fakeTeamTransport{payload: `[{"id":"a","name":"core","display_name":"Core","type":"O"}]`}
	teams := NewTeams(f)
	var wg sync.WaitGroup
	for range 40 {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = teams.List(context.Background(), "user") }()
	}
	wg.Wait()
}

func TestTeamsRejectMeAlias(t *testing.T) {
	_, err := NewTeams(&fakeTeamTransport{}).List(context.Background(), "me")
	if !errors.Is(err, ErrInvalidTeamRequest) {
		t.Fatalf("error = %v", err)
	}
}

func contains(value, fragment string) bool {
	for i := 0; i+len(fragment) <= len(value); i++ {
		if value[i:i+len(fragment)] == fragment {
			return true
		}
	}
	return false
}
