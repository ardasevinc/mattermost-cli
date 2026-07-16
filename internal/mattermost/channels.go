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
	ID            string
	TeamID        string
	Type          string
	Name          string
	DisplayName   string
	LastPostAt    int64
	TotalMsgCount int64

	totalMsgCountPresent bool
}

func (c *Channel) UnmarshalJSON(data []byte) error {
	var raw struct {
		ID          json.RawMessage `json:"id"`
		TeamID      json.RawMessage `json:"team_id"`
		Type        json.RawMessage `json:"type"`
		Name        json.RawMessage `json:"name"`
		DisplayName json.RawMessage `json:"display_name"`
		LastPostAt  json.RawMessage `json:"last_post_at"`
		TotalCount  json.RawMessage `json:"total_msg_count"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return ErrInvalidChannelResponse
	}
	id, idOK := requiredString(raw.ID)
	typeCode, typeOK := requiredString(raw.Type)
	name, nameOK := requiredString(raw.Name)
	teamID, teamOK := strictString(raw.TeamID)
	displayName, displayOK := strictString(raw.DisplayName)
	lastPostAt, lastPostOK := optionalNonNegativeInt64(raw.LastPostAt)
	totalCount, totalCountOK := optionalNonNegativeInt64(raw.TotalCount)
	if !idOK || !typeOK || !nameOK || !teamOK || !displayOK || !lastPostOK || !totalCountOK {
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
	*c = Channel{
		ID: id, TeamID: teamID, Type: typeCode, Name: name, DisplayName: displayName,
		LastPostAt: lastPostAt, TotalMsgCount: totalCount, totalMsgCountPresent: len(raw.TotalCount) != 0,
	}
	return nil
}

func optionalNonNegativeInt64(raw json.RawMessage) (int64, bool) {
	if len(raw) == 0 {
		return 0, true
	}
	var value int64
	if string(raw) == "null" || json.Unmarshal(raw, &value) != nil || value < 0 {
		return 0, false
	}
	return value, true
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

type UnreadMember struct {
	ChannelID    string
	UserID       string
	MsgCount     int64
	MentionCount int64
	LastViewedAt int64
}

func (m *UnreadMember) UnmarshalJSON(data []byte) error {
	var raw struct {
		ChannelID json.RawMessage `json:"channel_id"`
		UserID    json.RawMessage `json:"user_id"`
		MsgCount  json.RawMessage `json:"msg_count"`
		Mentions  json.RawMessage `json:"mention_count"`
		ViewedAt  json.RawMessage `json:"last_viewed_at"`
	}
	if json.Unmarshal(data, &raw) != nil {
		return ErrInvalidChannelResponse
	}
	channelID, channelOK := requiredString(raw.ChannelID)
	userID, userOK := requiredString(raw.UserID)
	msgCount, msgOK := requiredNonNegativeInt64(raw.MsgCount)
	mentionCount, mentionOK := requiredNonNegativeInt64(raw.Mentions)
	lastViewedAt, viewedOK := requiredNonNegativeInt64(raw.ViewedAt)
	if !channelOK || !userOK || !msgOK || !mentionOK || !viewedOK {
		return ErrInvalidChannelResponse
	}
	*m = UnreadMember{ChannelID: channelID, UserID: userID, MsgCount: msgCount, MentionCount: mentionCount, LastViewedAt: lastViewedAt}
	return nil
}

func requiredNonNegativeInt64(raw json.RawMessage) (int64, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	return optionalNonNegativeInt64(raw)
}

type unreadMemberList []UnreadMember

func (l *unreadMemberList) UnmarshalJSON(data []byte) error {
	var members []UnreadMember
	if err := json.Unmarshal(data, &members); err != nil || members == nil {
		return ErrInvalidChannelsResponse
	}
	*l = members
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

type groupChannelList []Channel

func (l *groupChannelList) UnmarshalJSON(data []byte) error {
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil || raw == nil {
		return ErrInvalidChannelsResponse
	}
	groups := make([]Channel, 0)
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
		if typeCode != "G" {
			continue
		}
		var channel Channel
		if json.Unmarshal(item, &channel) != nil {
			return ErrInvalidChannelsResponse
		}
		groups = append(groups, channel)
	}
	*l = groups
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

func (s *Channels) UnreadMember(ctx context.Context, channelID, userID string) (UnreadMember, error) {
	if strings.TrimSpace(channelID) == "" || strings.TrimSpace(userID) == "" || userID == "me" {
		return UnreadMember{}, ErrInvalidChannelRequest
	}
	var member UnreadMember
	path := "/channels/" + url.PathEscape(channelID) + "/members/" + url.PathEscape(userID)
	if err := s.client.Get(ctx, path, &member); err != nil {
		return UnreadMember{}, err
	}
	if member.ChannelID != channelID || member.UserID != userID {
		return UnreadMember{}, ErrInvalidChannelResponse
	}
	return member, nil
}

// TeamMembers returns the bounded, complete membership snapshot used for team
// unread metrics. The endpoint is authoritative for the requested team, while
// every returned row is still bound to the requested canonical user ID.
func (s *Channels) TeamMembers(ctx context.Context, userID, teamID string) ([]UnreadMember, error) {
	if !canonicalChannelRequestID(userID) || !canonicalChannelRequestID(teamID) || userID == "me" {
		return nil, ErrInvalidChannelRequest
	}
	var decoded unreadMemberList
	path := "/users/" + url.PathEscape(userID) + "/teams/" + url.PathEscape(teamID) + "/channels/members"
	if err := s.client.Get(ctx, path, &decoded); err != nil {
		return nil, err
	}
	members := []UnreadMember(decoded)
	seen := make(map[string]struct{}, len(members))
	for _, member := range members {
		if member.UserID != userID || !canonicalChannelRequestID(member.ChannelID) {
			return nil, ErrInvalidChannelsResponse
		}
		if _, duplicate := seen[member.ChannelID]; duplicate {
			return nil, ErrInvalidChannelsResponse
		}
		seen[member.ChannelID] = struct{}{}
	}
	sort.Slice(members, func(i, j int) bool { return members[i].ChannelID < members[j].ChannelID })
	return members, nil
}

func canonicalChannelRequestID(value string) bool {
	return value != "" && value == strings.TrimSpace(value)
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
		// The canonical current-user listing is the bounded membership proof
		// for discovered G channels, as it is in GroupList. Explicit group
		// reads continue to prove membership through Member.
		switch channel.Type {
		case "O", "P":
			if !membership.contains(channel.TeamID) {
				return nil, ErrInvalidChannelsResponse
			}
		case "D":
			if !directChannelContains(channel.Name, userID) {
				return nil, ErrInvalidChannelResponse
			}
		}
		result = append(result, channel)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

// ListForUnread preserves List's bounded identity proof while refusing to
// interpret an absent total_msg_count as a provably empty unread state.
func (s *Channels) ListForUnread(ctx context.Context, userID string) ([]Channel, error) {
	channels, err := s.List(ctx, userID)
	if err != nil {
		return nil, err
	}
	for _, channel := range channels {
		if !channel.totalMsgCountPresent {
			return nil, ErrInvalidChannelsResponse
		}
	}
	return channels, nil
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

// GroupList returns only G channels from the canonical current-user channel
// listing. That authenticated, same-session endpoint is itself the membership
// proof for discovered channels; per-channel Member calls would turn one
// bounded discovery read into unbounded fan-out. Explicit channel selection
// still requires Member. Unrelated payloads are ignored after their channel
// discriminator is validated.
func (s *Channels) GroupList(ctx context.Context, userID string) ([]Channel, error) {
	if strings.TrimSpace(userID) == "" || userID == "me" {
		return nil, ErrInvalidChannelRequest
	}
	var decoded groupChannelList
	if err := s.client.Get(ctx, "/users/"+url.PathEscape(userID)+"/channels", &decoded); err != nil {
		return nil, err
	}
	seen := make(map[string]Channel)
	result := make([]Channel, 0)
	for _, channel := range []Channel(decoded) {
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
