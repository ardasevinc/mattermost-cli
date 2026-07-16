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
	ErrInvalidChannelResponse  = errors.New("Mattermost returned an invalid channel response")
	ErrInvalidChannelsResponse = errors.New("Mattermost returned an invalid channels response")
	ErrInvalidChannelRequest   = errors.New("invalid Mattermost channel request")
)

type channelTransport interface {
	Get(context.Context, string, any) error
}

type Channel struct {
	ID          string
	TeamID      string
	Type        string
	Name        string
	DisplayName string
}

func (c *Channel) UnmarshalJSON(data []byte) error {
	var raw struct {
		ID          json.RawMessage `json:"id"`
		TeamID      json.RawMessage `json:"team_id"`
		Type        json.RawMessage `json:"type"`
		Name        json.RawMessage `json:"name"`
		DisplayName json.RawMessage `json:"display_name"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return ErrInvalidChannelResponse
	}
	id, idOK := requiredString(raw.ID)
	typeCode, typeOK := requiredString(raw.Type)
	name, nameOK := requiredString(raw.Name)
	teamID, teamOK := strictString(raw.TeamID)
	displayName, displayOK := strictString(raw.DisplayName)
	if !idOK || !typeOK || !nameOK || !teamOK || !displayOK {
		return ErrInvalidChannelResponse
	}
	if typeCode != "O" && typeCode != "P" && typeCode != "D" && typeCode != "G" {
		return ErrInvalidChannelResponse
	}
	if typeCode == "O" || typeCode == "P" {
		if strings.TrimSpace(teamID) == "" {
			return ErrInvalidChannelResponse
		}
	} else if teamID != "" {
		return ErrInvalidChannelResponse
	}
	if typeCode == "D" {
		parts := strings.Split(name, "__")
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
			return ErrInvalidChannelResponse
		}
	}
	*c = Channel{ID: id, TeamID: teamID, Type: typeCode, Name: name, DisplayName: displayName}
	return nil
}

func strictString(raw json.RawMessage) (string, bool) {
	var value string
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
		return "", false
	}
	return value, true
}

type ChannelMember struct {
	ChannelID string
	UserID    string
}

func (m *ChannelMember) UnmarshalJSON(data []byte) error {
	var raw struct {
		ChannelID json.RawMessage `json:"channel_id"`
		UserID    json.RawMessage `json:"user_id"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return ErrInvalidChannelResponse
	}
	channelID, channelOK := requiredString(raw.ChannelID)
	userID, userOK := requiredString(raw.UserID)
	if !channelOK || !userOK {
		return ErrInvalidChannelResponse
	}
	*m = ChannelMember{ChannelID: channelID, UserID: userID}
	return nil
}

type Channels struct{ client channelTransport }

func NewChannels(client channelTransport) *Channels { return &Channels{client: client} }

type channelList []Channel

func (l *channelList) UnmarshalJSON(data []byte) error {
	var channels []Channel
	if err := json.Unmarshal(data, &channels); err != nil || channels == nil {
		return ErrInvalidChannelsResponse
	}
	*l = channels
	return nil
}

type directChannelList []Channel

func (l *directChannelList) UnmarshalJSON(data []byte) error {
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil || raw == nil {
		return ErrInvalidChannelsResponse
	}
	direct := make([]Channel, 0)
	for _, item := range raw {
		var discriminator struct {
			Type json.RawMessage `json:"type"`
		}
		if json.Unmarshal(item, &discriminator) != nil {
			return ErrInvalidChannelsResponse
		}
		typeCode, ok := requiredString(discriminator.Type)
		if !ok || (typeCode != "O" && typeCode != "P" && typeCode != "D" && typeCode != "G") {
			return ErrInvalidChannelsResponse
		}
		if typeCode != "D" {
			continue
		}
		var channel Channel
		if json.Unmarshal(item, &channel) != nil {
			return ErrInvalidChannelsResponse
		}
		direct = append(direct, channel)
	}
	*l = direct
	return nil
}

func (s *Channels) ByID(ctx context.Context, channelID string) (Channel, error) {
	if strings.TrimSpace(channelID) == "" {
		return Channel{}, ErrInvalidChannelRequest
	}
	var channel Channel
	if err := s.client.Get(ctx, "/channels/"+url.PathEscape(channelID), &channel); err != nil {
		return Channel{}, err
	}
	if channel.ID != channelID {
		return Channel{}, ErrInvalidChannelResponse
	}
	return channel, nil
}

func (s *Channels) ByName(ctx context.Context, teamID, name string) (Channel, error) {
	name = strings.TrimPrefix(name, "#")
	if strings.TrimSpace(teamID) == "" || strings.TrimSpace(name) == "" {
		return Channel{}, ErrInvalidChannelRequest
	}
	var channel Channel
	path := "/teams/" + url.PathEscape(teamID) + "/channels/name/" + url.PathEscape(name)
	if err := s.client.Get(ctx, path, &channel); err != nil {
		return Channel{}, err
	}
	if channel.TeamID != teamID || channel.Name != name || (channel.Type != "O" && channel.Type != "P") {
		return Channel{}, ErrInvalidChannelResponse
	}
	return channel, nil
}

func (s *Channels) Member(ctx context.Context, channelID, userID string) (ChannelMember, error) {
	if strings.TrimSpace(channelID) == "" || strings.TrimSpace(userID) == "" || userID == "me" {
		return ChannelMember{}, ErrInvalidChannelRequest
	}
	var member ChannelMember
	path := "/channels/" + url.PathEscape(channelID) + "/members/" + url.PathEscape(userID)
	if err := s.client.Get(ctx, path, &member); err != nil {
		return ChannelMember{}, err
	}
	if member.ChannelID != channelID || member.UserID != userID {
		return ChannelMember{}, ErrInvalidChannelResponse
	}
	return member, nil
}

// List returns the authenticated user's channels with every identity binding
// checked before any result is released. Team membership is fetched through
// the same transport so proof cannot be mixed across sessions or servers.
func (s *Channels) List(ctx context.Context, userID string) ([]Channel, error) {
	if strings.TrimSpace(userID) == "" || userID == "me" {
		return nil, ErrInvalidChannelRequest
	}
	var decoded channelList
	if err := s.client.Get(ctx, "/users/"+url.PathEscape(userID)+"/channels", &decoded); err != nil {
		return nil, err
	}
	channels := []Channel(decoded)
	var membership TeamMembership
	for _, channel := range channels {
		if channel.Type == "O" || channel.Type == "P" {
			var err error
			membership, err = NewTeams(s.client).List(ctx, userID)
			if err != nil {
				return nil, err
			}
			break
		}
	}
	seen := make(map[string]Channel, len(channels))
	result := make([]Channel, 0, len(channels))
	for _, channel := range channels {
		if previous, duplicate := seen[channel.ID]; duplicate {
			if previous != channel {
				return nil, ErrInvalidChannelsResponse
			}
			continue
		}
		seen[channel.ID] = channel
		switch channel.Type {
		case "O", "P":
			if !membership.contains(channel.TeamID) {
				return nil, ErrInvalidChannelsResponse
			}
		case "D":
			if !directChannelContains(channel.Name, userID) {
				return nil, ErrInvalidChannelResponse
			}
		case "G":
			if _, err := s.Member(ctx, channel.ID, userID); err != nil {
				return nil, err
			}
		}
		result = append(result, channel)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

// DirectList returns only current-user-bound D channels. It deliberately does
// not resolve team membership or group membership for unrelated channel types.
func (s *Channels) DirectList(ctx context.Context, userID string) ([]Channel, error) {
	if strings.TrimSpace(userID) == "" || userID == "me" {
		return nil, ErrInvalidChannelRequest
	}
	var decoded directChannelList
	if err := s.client.Get(ctx, "/users/"+url.PathEscape(userID)+"/channels", &decoded); err != nil {
		return nil, err
	}
	seen := make(map[string]Channel)
	result := make([]Channel, 0)
	for _, channel := range []Channel(decoded) {
		if !directChannelContains(channel.Name, userID) {
			return nil, ErrInvalidChannelResponse
		}
		if previous, duplicate := seen[channel.ID]; duplicate {
			if previous != channel {
				return nil, ErrInvalidChannelsResponse
			}
			continue
		}
		seen[channel.ID] = channel
		result = append(result, channel)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

func directChannelContains(name, userID string) bool {
	parts := strings.Split(name, "__")
	return len(parts) == 2 && (parts[0] == userID || parts[1] == userID)
}
