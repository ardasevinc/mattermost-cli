// Package mattermost provides narrow, validated access to the Mattermost API.
package mattermost

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"sync"
)

const (
	maxUserListPage   = 200
	maxUserSearchPage = 1000
	maxUsersByID      = 200
)

var (
	ErrInvalidUserResponse  = errors.New("Mattermost returned an invalid user response")
	ErrInvalidUsersResponse = errors.New("Mattermost returned an invalid users response")
	ErrInvalidUserRequest   = errors.New("invalid Mattermost user request")
)

type userTransport interface {
	Get(context.Context, string, any) error
	PostRead(context.Context, string, any, any) error
}

// User is the deliberately narrow user profile exposed to the rest of v2.
// Sensitive and server-internal profile fields are never retained here.
type User struct {
	ID        string
	Username  string
	Nickname  string
	FirstName string
	LastName  string
	Roles     string
}

func (u *User) UnmarshalJSON(data []byte) error {
	var raw struct {
		ID        json.RawMessage `json:"id"`
		Username  json.RawMessage `json:"username"`
		Nickname  json.RawMessage `json:"nickname"`
		FirstName json.RawMessage `json:"first_name"`
		LastName  json.RawMessage `json:"last_name"`
		Roles     json.RawMessage `json:"roles"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return ErrInvalidUserResponse
	}
	id, ok := requiredString(raw.ID)
	if !ok {
		return ErrInvalidUserResponse
	}
	username, ok := requiredString(raw.Username)
	if !ok {
		return ErrInvalidUserResponse
	}
	*u = User{
		ID: id, Username: username,
		Nickname: optionalString(raw.Nickname), FirstName: optionalString(raw.FirstName),
		LastName: optionalString(raw.LastName), Roles: optionalString(raw.Roles),
	}
	return nil
}

func requiredString(raw json.RawMessage) (string, bool) {
	var value string
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil || strings.TrimSpace(value) == "" {
		return "", false
	}
	return value, true
}

func optionalString(raw json.RawMessage) string {
	var value string
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return value
}

// DirectoryResult reports whether more matching users are known to exist.
// Truncated is nil when an endpoint ceiling prevented an honest determination.
type DirectoryResult struct {
	Users     []User
	Truncated *bool
}

// Users provides concurrency-safe identity lookup and session-local caching.
type Users struct {
	client userTransport
	mu     sync.RWMutex
	byID   map[string]User
	byName map[string]string
}

func NewUsers(client userTransport) *Users {
	return &Users{client: client, byID: make(map[string]User), byName: make(map[string]string)}
}

func (s *Users) Current(ctx context.Context) (User, error) {
	var user User
	if err := s.client.Get(ctx, "/users/me", &user); err != nil {
		return User{}, err
	}
	s.cache(user)
	return user, nil
}

func (s *Users) ByID(ctx context.Context, id string) (User, error) {
	if strings.TrimSpace(id) == "" {
		return User{}, ErrInvalidUserRequest
	}
	if user, ok := s.cachedByID(id); ok {
		return user, nil
	}
	var user User
	if err := s.client.Get(ctx, "/users/"+url.PathEscape(id), &user); err != nil {
		return User{}, err
	}
	if user.ID != id {
		return User{}, ErrInvalidUserResponse
	}
	s.cache(user)
	return user, nil
}

func (s *Users) ByUsername(ctx context.Context, username string) (User, error) {
	if strings.TrimSpace(username) == "" {
		return User{}, ErrInvalidUserRequest
	}
	key := strings.ToLower(username)
	s.mu.RLock()
	id, ok := s.byName[key]
	user, present := s.byID[id]
	s.mu.RUnlock()
	if ok && present && strings.EqualFold(user.Username, username) {
		return user, nil
	}
	return s.ByUsernameFresh(ctx, username)
}

// ByUsernameFresh always performs an authenticated remote lookup. It is used
// by mutation staging so a username reassigned since an earlier read cannot
// bind the former account from the session cache.
func (s *Users) ByUsernameFresh(ctx context.Context, username string) (User, error) {
	if strings.TrimSpace(username) == "" {
		return User{}, ErrInvalidUserRequest
	}
	var fetched User
	if err := s.client.Get(ctx, "/users/username/"+url.PathEscape(username), &fetched); err != nil {
		return User{}, err
	}
	if !strings.EqualFold(fetched.Username, username) {
		return User{}, ErrInvalidUserResponse
	}
	s.cache(fetched)
	return fetched, nil
}

// ByIDs performs one bounded, retry-safe semantic read POST. The response must
// contain each requested user exactly once; incomplete results fail closed.
func (s *Users) ByIDs(ctx context.Context, ids []string) ([]User, error) {
	if len(ids) == 0 {
		return []User{}, nil
	}
	unique := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if strings.TrimSpace(id) == "" {
			return nil, ErrInvalidUserRequest
		}
		if _, ok := seen[id]; !ok {
			seen[id] = struct{}{}
			unique = append(unique, id)
		}
	}
	if len(unique) > maxUsersByID {
		return nil, ErrInvalidUserRequest
	}

	missing := make([]string, 0, len(unique))
	for _, id := range unique {
		if _, ok := s.cachedByID(id); !ok {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		var users []User
		if err := s.client.PostRead(ctx, "/users/ids", missing, &users); err != nil {
			return nil, err
		}
		if users == nil || len(users) != len(missing) {
			return nil, ErrInvalidUsersResponse
		}
		wanted := make(map[string]struct{}, len(missing))
		for _, id := range missing {
			wanted[id] = struct{}{}
		}
		got := make(map[string]struct{}, len(users))
		for _, user := range users {
			if _, ok := wanted[user.ID]; !ok {
				return nil, ErrInvalidUsersResponse
			}
			if _, duplicate := got[user.ID]; duplicate {
				return nil, ErrInvalidUsersResponse
			}
			got[user.ID] = struct{}{}
		}
		for _, user := range users {
			s.cache(user)
		}
	}

	result := make([]User, 0, len(ids))
	for _, id := range ids {
		user, ok := s.cachedByID(id)
		if !ok {
			return nil, ErrInvalidUsersResponse
		}
		result = append(result, user)
	}
	return result, nil
}

func (s *Users) Directory(ctx context.Context, query, teamID string, limit int) (DirectoryResult, error) {
	if limit <= 0 {
		return DirectoryResult{}, ErrInvalidUserRequest
	}
	query = strings.TrimSpace(query)
	ceiling := maxUserListPage
	if query != "" {
		ceiling = maxUserSearchPage
	}
	probe := limit
	if probe < ceiling {
		probe++
	} else {
		probe = ceiling
	}

	var users []User
	if query != "" {
		body := map[string]any{"term": query, "limit": probe, "allow_inactive": false}
		if teamID != "" {
			body["team_id"] = teamID
		}
		if err := s.client.PostRead(ctx, "/users/search", body, &users); err != nil {
			return DirectoryResult{}, err
		}
	} else {
		path := "/users?page=0&per_page=" + strconv.Itoa(probe) + "&active=true"
		if teamID != "" {
			path += "&in_team=" + encodeQueryComponent(teamID)
		}
		if err := s.client.Get(ctx, path, &users); err != nil {
			return DirectoryResult{}, err
		}
	}
	if users == nil || len(users) > probe {
		return DirectoryResult{}, ErrInvalidUsersResponse
	}
	for _, user := range users {
		s.cache(user)
	}
	truncated := len(users) > limit
	if truncated {
		users = users[:limit]
	}
	var coverage *bool
	if probe != ceiling || len(users) < ceiling || truncated {
		coverage = &truncated
	}
	return DirectoryResult{Users: users, Truncated: coverage}, nil
}

func (s *Users) cachedByID(id string) (User, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	user, ok := s.byID[id]
	return user, ok
}

func (s *Users) cache(user User) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if previous, ok := s.byID[user.ID]; ok && !strings.EqualFold(previous.Username, user.Username) {
		previousKey := strings.ToLower(previous.Username)
		if s.byName[previousKey] == user.ID {
			delete(s.byName, previousKey)
		}
	}
	key := strings.ToLower(user.Username)
	if previousID, ok := s.byName[key]; ok && previousID != user.ID {
		// Keep the previous profile addressable by immutable ID, but remove the
		// stale name edge before installing the reassigned owner.
		delete(s.byName, key)
	}
	s.byID[user.ID] = user
	s.byName[key] = user.ID
}

func encodeQueryComponent(value string) string {
	return strings.ReplaceAll(url.QueryEscape(value), "+", "%20")
}
