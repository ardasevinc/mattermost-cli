package mattermost

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"unicode/utf8"

	"github.com/ardasevinc/mattermost-cli/internal/api"
)

const maxMutationMessageBytes = 65_535

var ErrInvalidMutationRequest = errors.New("invalid Mattermost mutation request")

type PostMutations struct{ client *api.Client }

func NewPostMutations(client *api.Client) *PostMutations { return &PostMutations{client: client} }

type CreatePostMutationInput struct {
	ChannelID     string
	UserID        string
	Message       string
	RootID        string
	FileIDs       []string
	PendingPostID string
}

type EditPostMutationInput struct {
	PostID    string
	ChannelID string
	UserID    string
	Message   string
	RootID    string
	FileIDs   []string
}

type DeletePostMutationInput struct{ PostID string }

type CreatePostMutationResult struct {
	PostID        string `json:"postId"`
	CreateAt      int64  `json:"createAt"`
	ChannelID     string `json:"channelId"`
	UserID        string `json:"userId"`
	PendingPostID string `json:"pendingPostId"`
}

type EditPostMutationResult struct {
	PostID   string `json:"postId"`
	UpdateAt int64  `json:"updateAt"`
}

type DeletePostMutationResult struct {
	PostID string `json:"postId"`
}

type PreparedCreatePost struct {
	mutation *api.PreparedMutation
	expected mutationPostExpectation
}

type PreparedEditPost struct {
	mutation *api.PreparedMutation
	expected mutationPostExpectation
}

type PreparedDeletePost struct {
	mutation *api.PreparedMutation
	postID   string
}

func (m *PostMutations) PrepareCreate(in CreatePostMutationInput) (*PreparedCreatePost, error) {
	if m == nil || m.client == nil || !validMutationPostInput(in.ChannelID, in.UserID, in.Message, in.RootID, in.FileIDs) || !isSafePostID(in.PendingPostID) {
		return nil, ErrInvalidMutationRequest
	}
	body := struct {
		ChannelID     string   `json:"channel_id"`
		Message       string   `json:"message"`
		RootID        string   `json:"root_id,omitempty"`
		FileIDs       []string `json:"file_ids,omitempty"`
		PendingPostID string   `json:"pending_post_id"`
	}{in.ChannelID, in.Message, in.RootID, slices.Clone(in.FileIDs), in.PendingPostID}
	prepared, err := m.client.PreparePostStatus("/posts", body, http.StatusCreated)
	if err != nil {
		return nil, err
	}
	return &PreparedCreatePost{prepared, mutationPostExpectation{channelID: in.ChannelID, userID: in.UserID, message: in.Message, rootID: in.RootID, fileIDs: slices.Clone(in.FileIDs), pendingPostID: in.PendingPostID}}, nil
}

func (m *PostMutations) PrepareEdit(in EditPostMutationInput) (*PreparedEditPost, error) {
	if m == nil || m.client == nil || !isSafePostID(in.PostID) || !validMutationPostInput(in.ChannelID, in.UserID, in.Message, in.RootID, in.FileIDs) {
		return nil, ErrInvalidMutationRequest
	}
	body := struct {
		Message string `json:"message"`
	}{in.Message}
	prepared, err := m.client.PreparePutStatus("/posts/"+url.PathEscape(in.PostID)+"/patch", body, http.StatusOK)
	if err != nil {
		return nil, err
	}
	return &PreparedEditPost{prepared, mutationPostExpectation{postID: in.PostID, channelID: in.ChannelID, userID: in.UserID, message: in.Message, rootID: in.RootID, fileIDs: slices.Clone(in.FileIDs)}}, nil
}

func (m *PostMutations) PrepareDelete(in DeletePostMutationInput) (*PreparedDeletePost, error) {
	if m == nil || m.client == nil || !isSafePostID(in.PostID) {
		return nil, ErrInvalidMutationRequest
	}
	prepared, err := m.client.PrepareDeleteStatus("/posts/"+url.PathEscape(in.PostID), http.StatusOK)
	if err != nil {
		return nil, err
	}
	return &PreparedDeletePost{prepared, in.PostID}, nil
}

func (p *PreparedCreatePost) Execute(ctx context.Context) (CreatePostMutationResult, error) {
	if p == nil || p.mutation == nil {
		return CreatePostMutationResult{}, &api.OutcomeUnknownError{}
	}
	var response mutationPostResponse
	if err := p.mutation.Execute(ctx, &response); err != nil {
		return CreatePostMutationResult{}, err
	}
	if !response.matches(p.expected) || response.PendingPostID != p.expected.pendingPostID {
		return CreatePostMutationResult{}, &api.OutcomeUnknownError{}
	}
	return CreatePostMutationResult{response.ID, response.CreateAt, response.ChannelID, response.UserID, response.PendingPostID}, nil
}

func (p *PreparedEditPost) Execute(ctx context.Context) (EditPostMutationResult, error) {
	if p == nil || p.mutation == nil {
		return EditPostMutationResult{}, &api.OutcomeUnknownError{}
	}
	var response mutationPostResponse
	if err := p.mutation.Execute(ctx, &response); err != nil {
		return EditPostMutationResult{}, err
	}
	if !response.matches(p.expected) {
		return EditPostMutationResult{}, &api.OutcomeUnknownError{}
	}
	return EditPostMutationResult{response.ID, response.UpdateAt}, nil
}

func (p *PreparedDeletePost) Execute(ctx context.Context) (DeletePostMutationResult, error) {
	if p == nil || p.mutation == nil || !isSafePostID(p.postID) {
		return DeletePostMutationResult{}, &api.OutcomeUnknownError{}
	}
	if err := p.mutation.Execute(ctx, new(statusOKResponse)); err != nil {
		return DeletePostMutationResult{}, err
	}
	return DeletePostMutationResult{p.postID}, nil
}

type mutationPostExpectation struct {
	postID, channelID, userID, message, rootID string
	fileIDs                                    []string
	pendingPostID                              string
}

type mutationPostResponse struct {
	ID, ChannelID, UserID, Message, MessageSource, RootID, PendingPostID string
	FileIDs                                                              []string
	CreateAt, UpdateAt                                                   int64
}

func (p *mutationPostResponse) UnmarshalJSON(data []byte) error {
	raw, ok := uniqueJSONObject(data)
	if !ok {
		return ErrInvalidPostResponse
	}
	id, idOK := safePostID(raw["id"])
	channelID, channelOK := safePostID(raw["channel_id"])
	userID, userOK := safePostID(raw["user_id"])
	message, messageOK := strictString(raw["message"])
	createAt, createOK := nonnegativeInteger(raw["create_at"])
	updateAt, updateOK := nonnegativeInteger(raw["update_at"])
	deleteAt, deleteOK := nonnegativeInteger(raw["delete_at"])
	rootID, rootOK := strictString(raw["root_id"])
	fileIDs, fileIDsOK := canonicalPostIDs(raw["file_ids"], 5)
	pendingPostID, pendingOK := strictString(raw["pending_post_id"])
	postType, typeOK := strictString(raw["type"])
	messageSource := ""
	if source, present := raw["message_source"]; present {
		var sourceOK bool
		messageSource, sourceOK = strictString(source)
		if !sourceOK {
			return ErrInvalidPostResponse
		}
	}
	if !idOK || !channelOK || !userOK || !messageOK || !createOK || createAt == 0 || createAt > maxDateMilliseconds ||
		!updateOK || updateAt == 0 || updateAt > maxDateMilliseconds || updateAt < createAt || !deleteOK || deleteAt != 0 ||
		!rootOK || rootID != "" && !isSafePostID(rootID) || !fileIDsOK || !pendingOK || pendingPostID != "" && !isSafePostID(pendingPostID) || !typeOK || postType != "" {
		return ErrInvalidPostResponse
	}
	*p = mutationPostResponse{id, channelID, userID, message, messageSource, rootID, pendingPostID, fileIDs, createAt, updateAt}
	return nil
}

func (p mutationPostResponse) matches(expected mutationPostExpectation) bool {
	return (expected.postID == "" || p.ID == expected.postID) && p.ChannelID == expected.channelID && p.UserID == expected.userID && (p.Message == expected.message || p.MessageSource == expected.message) &&
		p.RootID == expected.rootID && slices.Equal(p.FileIDs, expected.fileIDs)
}

type statusOKResponse struct{}

func (*statusOKResponse) UnmarshalJSON(data []byte) error {
	raw, ok := uniqueJSONObject(data)
	status, statusOK := strictString(raw["status"])
	if !ok || len(raw) != 1 || !statusOK || status != "OK" {
		return ErrInvalidPostResponse
	}
	return nil
}

func validMutationPostInput(channelID, userID, message, rootID string, fileIDs []string) bool {
	if !isSafePostID(channelID) || !isSafePostID(userID) || message == "" || len(message) > maxMutationMessageBytes || !utf8.ValidString(message) || utf8.RuneCountInString(message) > 16_383 || rootID != "" && !isSafePostID(rootID) || len(fileIDs) > 5 {
		return false
	}
	seen := make(map[string]struct{}, len(fileIDs))
	for _, id := range fileIDs {
		if !isSafePostID(id) {
			return false
		}
		if _, exists := seen[id]; exists {
			return false
		}
		seen[id] = struct{}{}
	}
	return true
}

var _ json.Unmarshaler = (*mutationPostResponse)(nil)
var _ json.Unmarshaler = (*statusOKResponse)(nil)
