package mattermost

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strings"
)

var (
	ErrInvalidTeamResponse  = errors.New("Mattermost returned an invalid team response")
	ErrInvalidTeamsResponse = errors.New("Mattermost returned an invalid teams response")
	ErrInvalidTeamRequest   = errors.New("invalid Mattermost team request")
	ErrTeamNotFound         = errors.New("Mattermost team was not found")
	ErrAmbiguousTeam        = errors.New("Mattermost team selection is ambiguous")
)

type teamTransport interface {
	Get(context.Context, string, any) error
}

type Team struct {
	ID          string
	Name        string
	DisplayName string
	Type        string
}

// TeamMembership is an exact, validated snapshot returned by Mattermost for
// one canonical user ID. Callers can inspect but cannot forge its contents.
type TeamMembership struct {
	userID string
	teams  []Team
}

func (m TeamMembership) Items() []Team {
	return append([]Team(nil), m.teams...)
}

func (m TeamMembership) contains(teamID string) bool {
	for _, team := range m.teams {
		if team.ID == teamID {
			return true
		}
	}
	return false
}

func (t *Team) UnmarshalJSON(data []byte) error {
	var raw struct {
		ID          json.RawMessage `json:"id"`
		Name        json.RawMessage `json:"name"`
		DisplayName json.RawMessage `json:"display_name"`
		Type        json.RawMessage `json:"type"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return ErrInvalidTeamResponse
	}
	id, idOK := requiredString(raw.ID)
	name, nameOK := requiredString(raw.Name)
	displayName, displayOK := strictString(raw.DisplayName)
	typeCode, typeOK := requiredString(raw.Type)
	if !idOK || !nameOK || !displayOK || !typeOK || (typeCode != "O" && typeCode != "I") {
		return ErrInvalidTeamResponse
	}
	*t = Team{ID: id, Name: name, DisplayName: displayName, Type: typeCode}
	return nil
}

type Teams struct{ client teamTransport }

func NewTeams(client teamTransport) *Teams { return &Teams{client: client} }

type teamList []Team

func (l *teamList) UnmarshalJSON(data []byte) error {
	var teams []Team
	if err := json.Unmarshal(data, &teams); err != nil || teams == nil {
		return ErrInvalidTeamsResponse
	}
	*l = teams
	return nil
}

func (s *Teams) List(ctx context.Context, userID string) (TeamMembership, error) {
	if strings.TrimSpace(userID) == "" || userID == "me" {
		return TeamMembership{}, ErrInvalidTeamRequest
	}
	var decoded teamList
	if err := s.client.Get(ctx, "/users/"+url.PathEscape(userID)+"/teams", &decoded); err != nil {
		return TeamMembership{}, err
	}
	teams := []Team(decoded)
	seen := make(map[string]struct{}, len(teams))
	for _, team := range teams {
		if _, duplicate := seen[team.ID]; duplicate {
			return TeamMembership{}, ErrInvalidTeamsResponse
		}
		seen[team.ID] = struct{}{}
	}
	sort.Slice(teams, func(i, j int) bool {
		if teams[i].Name != teams[j].Name {
			return teams[i].Name < teams[j].Name
		}
		return teams[i].ID < teams[j].ID
	})
	return TeamMembership{userID: userID, teams: teams}, nil
}

// Resolve selects a team only when the complete membership list identifies it
// uniquely. An empty selector is accepted only for a single-team account.
func (s *Teams) Resolve(ctx context.Context, userID, selector string) (Team, error) {
	membership, err := s.List(ctx, userID)
	if err != nil {
		return Team{}, err
	}
	if selector == "" {
		if len(membership.teams) == 0 {
			return Team{}, ErrTeamNotFound
		}
		if len(membership.teams) != 1 {
			return Team{}, ErrAmbiguousTeam
		}
		return membership.teams[0], nil
	}
	var matches []Team
	for _, team := range membership.teams {
		if team.Name == selector || team.DisplayName == selector {
			matches = append(matches, team)
		}
	}
	if len(matches) == 0 {
		return Team{}, ErrTeamNotFound
	}
	if len(matches) != 1 {
		return Team{}, ErrAmbiguousTeam
	}
	return matches[0], nil
}
