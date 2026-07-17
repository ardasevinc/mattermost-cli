package mattermost

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/ardasevinc/mattermost-cli/v2/internal/api"
)

type ReactionMutationInput struct {
	PostID, ChannelID, UserID, Emoji string
}

type ReactionMutationResult struct {
	PostID string `json:"postId"`
}

type PreparedAddReaction struct {
	mutation                         *api.PreparedMutation
	postID, channelID, userID, emoji string
}

type PreparedRemoveReaction struct {
	mutation *api.PreparedMutation
	postID   string
}

func (m *PostMutations) PrepareAddReaction(in ReactionMutationInput) (*PreparedAddReaction, error) {
	if !validReactionMutation(m, in) {
		return nil, ErrInvalidMutationRequest
	}
	emoji := strings.ToLower(in.Emoji)
	body := struct {
		UserID string `json:"user_id"`
		PostID string `json:"post_id"`
		Emoji  string `json:"emoji_name"`
	}{in.UserID, in.PostID, emoji}
	prepared, err := m.client.PreparePostStatus("/reactions", body, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return &PreparedAddReaction{prepared, in.PostID, in.ChannelID, in.UserID, emoji}, nil
}

func (m *PostMutations) PrepareRemoveReaction(in ReactionMutationInput) (*PreparedRemoveReaction, error) {
	if !validReactionMutation(m, in) {
		return nil, ErrInvalidMutationRequest
	}
	path := "/users/" + url.PathEscape(in.UserID) + "/posts/" + url.PathEscape(in.PostID) + "/reactions/" + url.PathEscape(strings.ToLower(in.Emoji))
	prepared, err := m.client.PrepareDeleteStatus(path, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return &PreparedRemoveReaction{prepared, in.PostID}, nil
}

func (p *PreparedAddReaction) Execute(ctx context.Context) (ReactionMutationResult, error) {
	if p == nil || p.mutation == nil {
		return ReactionMutationResult{}, &api.OutcomeUnknownError{}
	}
	var response reactionMutationResponse
	if err := p.mutation.Execute(ctx, &response); err != nil {
		return ReactionMutationResult{}, err
	}
	if response.PostID != p.postID || response.ChannelID != p.channelID || response.UserID != p.userID || response.Emoji != p.emoji {
		return ReactionMutationResult{}, &api.OutcomeUnknownError{}
	}
	return ReactionMutationResult{p.postID}, nil
}

func (p *PreparedRemoveReaction) Execute(ctx context.Context) (ReactionMutationResult, error) {
	if p == nil || p.mutation == nil || !isSafePostID(p.postID) {
		return ReactionMutationResult{}, &api.OutcomeUnknownError{}
	}
	if err := p.mutation.Execute(ctx, new(statusOKResponse)); err != nil {
		return ReactionMutationResult{}, err
	}
	return ReactionMutationResult{p.postID}, nil
}

type reactionMutationResponse struct {
	UserID, PostID, ChannelID, Emoji string
}

func (r *reactionMutationResponse) UnmarshalJSON(data []byte) error {
	fields, ok := uniqueJSONObject(data)
	if !ok {
		return ErrInvalidReactionsResponse
	}
	userID, userOK := safePostID(fields["user_id"])
	postID, postOK := safePostID(fields["post_id"])
	channelID, channelOK := safePostID(fields["channel_id"])
	emoji, emojiOK := strictString(fields["emoji_name"])
	createAt, createOK := nonnegativeInteger(fields["create_at"])
	updateAt, updateOK := nonnegativeInteger(fields["update_at"])
	deleteAt, deleteOK := nonnegativeInteger(fields["delete_at"])
	remoteRaw, remotePresent := fields["remote_id"]
	remoteID, remoteOK := strictString(remoteRaw)
	remoteOK = remotePresent && !isJSONNull(remoteRaw) && remoteOK
	if !userOK || !postOK || !channelOK || !emojiOK || !validEmojiName(emoji) || !createOK || createAt == 0 || createAt > maxDateMilliseconds ||
		!updateOK || updateAt == 0 || updateAt > maxDateMilliseconds || updateAt < createAt || !deleteOK || deleteAt != 0 || !remoteOK || remoteID != "" {
		return ErrInvalidReactionsResponse
	}
	*r = reactionMutationResponse{userID, postID, channelID, emoji}
	return nil
}

func validReactionMutation(m *PostMutations, in ReactionMutationInput) bool {
	return m != nil && m.client != nil && isSafePostID(in.PostID) && isSafePostID(in.ChannelID) && isSafePostID(in.UserID) && validEmojiName(in.Emoji)
}
