package mattermost

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
)

type userCall struct {
	method string
	path   string
	body   any
}

type fakeUsersTransport struct {
	mu        sync.Mutex
	responses []string
	calls     []userCall
}

func (f *fakeUsersTransport) respond(method, path string, body, out any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, userCall{method: method, path: path, body: body})
	if len(f.responses) == 0 {
		return errors.New("unexpected request")
	}
	response := f.responses[0]
	f.responses = f.responses[1:]
	return json.Unmarshal([]byte(response), out)
}

func (f *fakeUsersTransport) Get(_ context.Context, path string, out any) error {
	return f.respond("GET", path, nil, out)
}

func (f *fakeUsersTransport) PostRead(_ context.Context, path string, body, out any) error {
	return f.respond("POST_READ", path, body, out)
}

func TestCurrentRetainsOnlyNarrowValidatedFields(t *testing.T) {
	transport := &fakeUsersTransport{responses: []string{`{
		"id":"me","username":"arda","nickname":"a","first_name":"Arda",
		"last_name":42,"roles":"system_user","email":"private@example.test","props":{"secret":true}
	}`}}
	users := NewUsers(transport)
	got, err := users.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := User{ID: "me", Username: "arda", Nickname: "a", FirstName: "Arda", Roles: "system_user"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("user = %+v, want %+v", got, want)
	}
}

func TestUserDecodingFailsClosedForMalformedRequiredFields(t *testing.T) {
	for _, payload := range []string{`null`, `{}`, `[]`, `{"id":"","username":"user"}`, `{"id":"id","username":7}`} {
		t.Run(payload, func(t *testing.T) {
			transport := &fakeUsersTransport{responses: []string{payload}}
			_, err := NewUsers(transport).Current(context.Background())
			if !errors.Is(err, ErrInvalidUserResponse) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestLookupsEncodePathsRequireExactIdentityAndCache(t *testing.T) {
	transport := &fakeUsersTransport{responses: []string{
		`{"id":"id/with space","username":"name"}`,
		`{"id":"other","username":"Name/With Space"}`,
	}}
	users := NewUsers(transport)
	if _, err := users.ByID(context.Background(), "id/with space"); err != nil {
		t.Fatal(err)
	}
	if _, err := users.ByID(context.Background(), "id/with space"); err != nil {
		t.Fatal(err)
	}
	if _, err := users.ByUsername(context.Background(), "name/with space"); err != nil {
		t.Fatal(err)
	}
	if len(transport.calls) != 2 {
		t.Fatalf("calls = %d", len(transport.calls))
	}
	if got := transport.calls[0].path; got != "/users/id%2Fwith%20space" {
		t.Fatalf("ID path = %q", got)
	}
	if got := transport.calls[1].path; got != "/users/username/name%2Fwith%20space" {
		t.Fatalf("username path = %q", got)
	}
	if _, err := users.ByUsername(context.Background(), "NAME/WITH SPACE"); err != nil {
		t.Fatal(err)
	}
}

func TestLookupRejectsMismatchedResponseWithoutCaching(t *testing.T) {
	transport := &fakeUsersTransport{responses: []string{
		`{"id":"different","username":"user"}`,
		`{"id":"id","username":"different"}`,
	}}
	users := NewUsers(transport)
	if _, err := users.ByID(context.Background(), "id"); !errors.Is(err, ErrInvalidUserResponse) {
		t.Fatalf("ByID error = %v", err)
	}
	if _, err := users.ByUsername(context.Background(), "user"); !errors.Is(err, ErrInvalidUserResponse) {
		t.Fatalf("ByUsername error = %v", err)
	}
}

func TestCacheReplacementCannotResolveAStaleUsername(t *testing.T) {
	transport := &fakeUsersTransport{responses: []string{
		`{"id":"id","username":"old"}`,
		`{"id":"id","username":"new"}`,
		`{"id":"different","username":"old"}`,
	}}
	users := NewUsers(transport)
	if _, err := users.ByUsername(context.Background(), "old"); err != nil {
		t.Fatal(err)
	}
	if _, err := users.Current(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := users.ByUsername(context.Background(), "old"); err != nil {
		t.Fatal(err)
	}
	if len(transport.calls) != 3 {
		t.Fatalf("calls = %d, stale username was served from cache", len(transport.calls))
	}
}

func TestByIDsDeduplicatesRequestPreservesOrderAndRequiresCompleteResult(t *testing.T) {
	transport := &fakeUsersTransport{responses: []string{`[
		{"id":"b","username":"bob"},{"id":"a","username":"alice"}
	]`}}
	users := NewUsers(transport)
	got, err := users.ByIDs(context.Background(), []string{"a", "b", "a"})
	if err != nil {
		t.Fatal(err)
	}
	if ids := []string{got[0].ID, got[1].ID, got[2].ID}; !reflect.DeepEqual(ids, []string{"a", "b", "a"}) {
		t.Fatalf("IDs = %v", ids)
	}
	if len(transport.calls) != 1 || transport.calls[0].method != "POST_READ" {
		t.Fatalf("calls = %+v", transport.calls)
	}
	if !reflect.DeepEqual(transport.calls[0].body, []string{"a", "b"}) {
		t.Fatalf("body = %#v", transport.calls[0].body)
	}

	incomplete := NewUsers(&fakeUsersTransport{responses: []string{`[{"id":"a","username":"alice"}]`}})
	if _, err := incomplete.ByIDs(context.Background(), []string{"a", "b"}); !errors.Is(err, ErrInvalidUsersResponse) {
		t.Fatalf("incomplete error = %v", err)
	}
}

func TestDirectoryUsesBoundedProbeAndHonestCoverage(t *testing.T) {
	transport := &fakeUsersTransport{responses: []string{
		`[{"id":"a","username":"a"},{"id":"b","username":"b"},{"id":"c","username":"c"}]`,
		`[]`,
	}}
	users := NewUsers(transport)
	result, err := users.Directory(context.Background(), " dev ", "team/one", 2)
	if err != nil {
		t.Fatal(err)
	}
	if result.Truncated == nil || !*result.Truncated || len(result.Users) != 2 {
		t.Fatalf("result = %+v", result)
	}
	wantBody := map[string]any{"term": "dev", "team_id": "team/one", "limit": 3, "allow_inactive": false}
	if !reflect.DeepEqual(transport.calls[0].body, wantBody) {
		t.Fatalf("body = %#v", transport.calls[0].body)
	}

	result, err = users.Directory(context.Background(), "", "team/one space", 2)
	if err != nil {
		t.Fatal(err)
	}
	if result.Truncated == nil || *result.Truncated {
		t.Fatalf("result = %+v", result)
	}
	if got := transport.calls[1].path; got != "/users?page=0&per_page=3&active=true&in_team=team%2Fone%20space" {
		t.Fatalf("path = %q", got)
	}
}

func TestDirectoryReportsUnknownAtEndpointCeilings(t *testing.T) {
	items := make([]map[string]string, maxUserListPage)
	for i := range items {
		items[i] = map[string]string{"id": "id" + string(rune(i+1)), "username": "u" + string(rune(i+1))}
	}
	payload, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	transport := &fakeUsersTransport{responses: []string{string(payload)}}
	result, err := NewUsers(transport).Directory(context.Background(), "", "", 300)
	if err != nil {
		t.Fatal(err)
	}
	if result.Truncated != nil {
		t.Fatalf("truncated = %v, want unknown", *result.Truncated)
	}
	if got := transport.calls[0].path; got != "/users?page=0&per_page=200&active=true" {
		t.Fatalf("path = %q", got)
	}
}

func TestUsersCacheIsRaceSafe(t *testing.T) {
	transport := &fakeUsersTransport{responses: []string{`{"id":"id","username":"user"}`}}
	users := NewUsers(transport)
	if _, err := users.ByID(context.Background(), "id"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(2)
		go func() { defer wg.Done(); _, _ = users.ByID(context.Background(), "id") }()
		go func() { defer wg.Done(); _, _ = users.ByUsername(context.Background(), "USER") }()
	}
	wg.Wait()
	if len(transport.calls) != 1 {
		t.Fatalf("calls = %d", len(transport.calls))
	}
}
