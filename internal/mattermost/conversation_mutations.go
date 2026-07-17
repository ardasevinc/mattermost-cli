package mattermost

import (
	"context"
	"crypto/sha1" // Mattermost defines group channel names as SHA-1 over sorted member IDs.
	"encoding/hex"
	"encoding/json"
	"net/http"
	"slices"
	"strings"

	"github.com/ardasevinc/mattermost-cli/internal/api"
)

type ConversationMutations struct{ client *api.Client }

func NewConversationMutations(client *api.Client) *ConversationMutations {
	return &ConversationMutations{client: client}
}

type ResolveConversationMutationInput struct {
	CurrentUserID  string
	ParticipantIDs []string
}

type ResolveConversationMutationResult struct {
	ChannelID      string   `json:"channelId"`
	ParticipantIDs []string `json:"participantIds"`
}

type PreparedResolveConversation struct {
	mutation       *api.PreparedMutation
	channelType    string
	channelName    string
	participantIDs []string
}

func (m *ConversationMutations) PrepareDirect(in ResolveConversationMutationInput) (*PreparedResolveConversation, error) {
	return m.prepare(in, "D", 1, 1, "/channels/direct")
}

func (m *ConversationMutations) PrepareGroup(in ResolveConversationMutationInput) (*PreparedResolveConversation, error) {
	return m.prepare(in, "G", 2, 7, "/channels/group")
}

func (m *ConversationMutations) prepare(in ResolveConversationMutationInput, channelType string, minimum, maximum int, path string) (*PreparedResolveConversation, error) {
	if m == nil || m.client == nil || !isSafePostID(in.CurrentUserID) || len(in.ParticipantIDs) < minimum || len(in.ParticipantIDs) > maximum {
		return nil, ErrInvalidMutationRequest
	}
	peers := slices.Clone(in.ParticipantIDs)
	slices.Sort(peers)
	for index, id := range peers {
		if !isSafePostID(id) || id == in.CurrentUserID || index > 0 && id == peers[index-1] {
			return nil, ErrInvalidMutationRequest
		}
	}
	members := append(slices.Clone(peers), in.CurrentUserID)
	slices.Sort(members)
	name := strings.Join(members, "__")
	if channelType == "G" {
		name = canonicalGroupChannelName(members)
	}
	prepared, err := m.client.PreparePostStatus(path, members, http.StatusCreated)
	if err != nil {
		return nil, err
	}
	return &PreparedResolveConversation{prepared, channelType, name, peers}, nil
}

func canonicalGroupChannelName(memberIDs []string) string {
	members := slices.Clone(memberIDs)
	slices.Sort(members)
	digest := sha1.Sum([]byte(strings.Join(members, "")))
	return hex.EncodeToString(digest[:])
}

func (p *PreparedResolveConversation) Execute(ctx context.Context) (ResolveConversationMutationResult, error) {
	if p == nil || p.mutation == nil || (p.channelType != "D" && p.channelType != "G") || p.channelName == "" {
		return ResolveConversationMutationResult{}, &api.OutcomeUnknownError{}
	}
	var response conversationMutationResponse
	if err := p.mutation.Execute(ctx, &response); err != nil {
		return ResolveConversationMutationResult{}, err
	}
	if response.Type != p.channelType || response.Name != p.channelName {
		return ResolveConversationMutationResult{}, &api.OutcomeUnknownError{}
	}
	return ResolveConversationMutationResult{response.ID, slices.Clone(p.participantIDs)}, nil
}

type conversationMutationResponse struct {
	ID, Type, Name string
}

func (c *conversationMutationResponse) UnmarshalJSON(data []byte) error {
	fields, ok := uniqueJSONObject(data)
	if !ok {
		return ErrInvalidChannelResponse
	}
	id, idOK := safePostID(fields["id"])
	channelType, typeOK := strictString(fields["type"])
	name, nameOK := strictString(fields["name"])
	teamID, teamOK := strictString(fields["team_id"])
	displayName, displayOK := strictString(fields["display_name"])
	createAt, createOK := nonnegativeInteger(fields["create_at"])
	updateAt, updateOK := nonnegativeInteger(fields["update_at"])
	deleteAt, deleteOK := nonnegativeInteger(fields["delete_at"])
	if !idOK || !typeOK || channelType != "D" && channelType != "G" || !nameOK || name == "" || !teamOK || teamID != "" || !displayOK ||
		channelType == "D" && displayName != "" || !createOK || createAt == 0 || createAt > maxDateMilliseconds ||
		!updateOK || updateAt == 0 || updateAt > maxDateMilliseconds || updateAt < createAt || !deleteOK || deleteAt != 0 {
		return ErrInvalidChannelResponse
	}
	*c = conversationMutationResponse{id, channelType, name}
	return nil
}

var _ json.Unmarshaler = (*conversationMutationResponse)(nil)
