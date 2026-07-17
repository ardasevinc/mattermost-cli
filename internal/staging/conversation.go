package staging

import (
	"context"
	"strings"

	"github.com/ardasevinc/mattermost-cli/v2/internal/mattermost"
)

func validTargetSyntax(target Target) bool {
	if !validSelectorValue(target.Value) || target.Selector == ByName && strings.HasPrefix(target.Value, "#") {
		return false
	}
	switch target.Conversation {
	case Direct:
		return target.Selector == ByUsername && target.Team == nil
	case Group:
		return target.Selector == ByID && target.Team == nil
	case Channel:
		if target.Selector == ByID {
			return target.Team == nil
		}
		return target.Selector == ByName && target.Team != nil &&
			(target.Team.By == ByID || target.Team.By == ByName) && validSelectorValue(target.Team.Value)
	default:
		return false
	}
}

func (s *Service) resolveConversation(ctx context.Context, target Target) (Preview, error) {
	if ctx == nil || !validTargetSyntax(target) {
		return Preview{}, ErrInvalid
	}
	if contaminated(s.credentials, targetStrings(target)...) {
		return Preview{}, ErrCredential
	}
	current, err := s.users.Current(ctx)
	if err != nil {
		return Preview{}, targetReadError(err)
	}
	if !validResolvedUser(current) {
		return Preview{}, ErrTarget
	}
	if contaminated(s.credentials, current.ID, current.Username) {
		return Preview{}, ErrCredential
	}
	return s.resolveConversationFor(ctx, target, current)
}

func (s *Service) resolveConversationFor(ctx context.Context, target Target, current mattermost.User) (Preview, error) {
	channel, participants, err := s.resolveChannel(ctx, current, target)
	if err != nil {
		return Preview{}, err
	}
	var teamID *string
	if channel.Type == "O" || channel.Type == "P" {
		value := channel.TeamID
		teamID = &value
	}
	preview := Preview{
		ServerURL: s.serverURL,
		ServerID:  s.serverID,
		UserID:    current.ID,
		Destination: Destination{
			Kind: "conversation", ChannelID: channel.ID, ChannelType: channelType(channel.Type),
			TeamID: teamID, ParticipantIDs: participants,
		},
		Plan: attachmentPlan(0),
	}
	destination, plan, err := marshalSemantics(preview)
	if err != nil {
		return Preview{}, ErrInvalid
	}
	fields := append(targetStrings(target), preview.ServerURL, preview.ServerID, preview.UserID, string(destination), string(plan))
	if contaminated(s.credentials, fields...) {
		return Preview{}, ErrCredential
	}
	return preview, nil
}

func (s *Service) resolveChannel(ctx context.Context, current mattermost.User, target Target) (mattermost.Channel, []string, error) {
	switch target.Conversation {
	case Direct:
		peer, err := s.users.ByUsernameFresh(ctx, target.Value)
		if err != nil {
			return mattermost.Channel{}, nil, targetReadError(err)
		}
		if !validResolvedUser(peer) || !strings.EqualFold(peer.Username, target.Value) || peer.ID == current.ID {
			return mattermost.Channel{}, nil, ErrTarget
		}
		if contaminated(s.credentials, peer.ID, peer.Username) {
			return mattermost.Channel{}, nil, ErrCredential
		}
		channel, found, err := s.channels.ExistingDirect(ctx, current.ID, peer.ID)
		if err != nil {
			return mattermost.Channel{}, nil, targetReadError(err)
		}
		if !found || !validResolvedChannel(channel) || channel.Type != "D" {
			return mattermost.Channel{}, nil, ErrTarget
		}
		if contaminated(s.credentials, channel.ID, channel.Name) {
			return mattermost.Channel{}, nil, ErrCredential
		}
		return channel, []string{peer.ID}, nil
	case Group:
		channel, err := s.channels.ByID(ctx, target.Value)
		if err != nil {
			return mattermost.Channel{}, nil, targetReadError(err)
		}
		if !validResolvedChannel(channel) || channel.Type != "G" {
			return mattermost.Channel{}, nil, ErrTarget
		}
		if contaminated(s.credentials, channel.ID, channel.Name) {
			return mattermost.Channel{}, nil, ErrCredential
		}
		member, memberErr := s.channels.Member(ctx, channel.ID, current.ID)
		if memberErr != nil {
			return mattermost.Channel{}, nil, targetReadError(memberErr)
		}
		if member.ChannelID != channel.ID || member.UserID != current.ID {
			return mattermost.Channel{}, nil, ErrTarget
		}
		return channel, []string{}, nil
	case Channel:
		var channel mattermost.Channel
		var err error
		if target.Selector == ByID {
			channel, err = s.channels.ByID(ctx, target.Value)
		} else {
			team, teamErr := s.resolveTeam(ctx, current.ID, *target.Team)
			if teamErr != nil {
				return mattermost.Channel{}, nil, teamErr
			}
			channel, err = s.channels.ByName(ctx, team.ID, target.Value)
		}
		if err != nil {
			return mattermost.Channel{}, nil, targetReadError(err)
		}
		if !validResolvedChannel(channel) || (channel.Type != "O" && channel.Type != "P") {
			return mattermost.Channel{}, nil, ErrTarget
		}
		if contaminated(s.credentials, channel.ID, channel.Name, channel.TeamID) {
			return mattermost.Channel{}, nil, ErrCredential
		}
		member, memberErr := s.channels.Member(ctx, channel.ID, current.ID)
		if memberErr != nil {
			return mattermost.Channel{}, nil, targetReadError(memberErr)
		}
		if member.ChannelID != channel.ID || member.UserID != current.ID {
			return mattermost.Channel{}, nil, ErrTarget
		}
		return channel, []string{}, nil
	default:
		return mattermost.Channel{}, nil, ErrInvalid
	}
}

func (s *Service) resolveTeam(ctx context.Context, userID string, selector TeamSelector) (mattermost.Team, error) {
	membership, err := s.teams.List(ctx, userID)
	if err != nil {
		return mattermost.Team{}, targetReadError(err)
	}
	var match mattermost.Team
	count := 0
	for _, team := range membership.Items() {
		if !validResolvedTeam(team) {
			return mattermost.Team{}, ErrTarget
		}
		matched := selector.By == ByID && team.ID == selector.Value
		if selector.By == ByName {
			matched = team.Name == selector.Value || team.DisplayName == selector.Value
		}
		if matched {
			match, count = team, count+1
		}
	}
	if count != 1 {
		return mattermost.Team{}, ErrTarget
	}
	if contaminated(s.credentials, match.ID, match.Name, match.DisplayName) {
		return mattermost.Team{}, ErrCredential
	}
	return match, nil
}

func channelType(value string) string {
	return map[string]string{"D": "dm", "G": "group", "O": "public", "P": "private"}[value]
}
