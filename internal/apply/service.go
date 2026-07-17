// Package apply executes reviewed stage plans through the durable apply journal.
package apply

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"

	"github.com/ardasevinc/mattermost-cli/internal/api"
	"github.com/ardasevinc/mattermost-cli/internal/mattermost"
	"github.com/ardasevinc/mattermost-cli/internal/stagestore"
	"github.com/ardasevinc/mattermost-cli/internal/staging"
)

var (
	ErrInvalid              = errors.New("apply: invalid request")
	ErrTargetDrift          = errors.New("apply: staged target no longer matches Mattermost")
	ErrUnsupportedOperation = errors.New("apply: operation is not implemented")
	ErrJournal              = errors.New("apply: durable journal update failed")
	ErrCredential           = errors.New("apply: active credential in outbound content")
)

// ConfirmedEffectError means Mattermost confirmed the effect but its durable
// receipt could not be recorded. The caller must not retry automatically.
type ConfirmedEffectError struct{ err error }

func (e *ConfirmedEffectError) Error() string {
	return "apply: effect confirmed but receipt was not recorded; do not retry"
}
func (e *ConfirmedEffectError) Unwrap() error { return e.err }

type Store interface {
	Show(context.Context, string) (stagestore.StageDetail, error)
	FindApply(context.Context, string, string, string, [32]byte) (stagestore.ApplyAttempt, bool, error)
	ClaimApply(context.Context, stagestore.ApplyClaimInput) (stagestore.ApplyAttempt, error)
	AbandonApplyBeforeDispatch(context.Context, string) error
	BeginDispatch(context.Context, string, int) error
	MarkStepValidated(context.Context, string, int, json.RawMessage) error
	MarkStepRejected(context.Context, string, int, json.RawMessage) error
	MarkStepUnknown(context.Context, string, int) error
	MarkStepSkipped(context.Context, string, int, json.RawMessage) error
	FinalizeApply(context.Context, string) (stagestore.ApplyReceipt, error)
}

type CurrentUser interface {
	Current(context.Context) (mattermost.User, error)
}

type Conversations interface {
	ExistingDirect(context.Context, string, string) (mattermost.Channel, bool, error)
	ExistingGroup(context.Context, string, []string) (mattermost.Channel, bool, error)
	ByID(context.Context, string) (mattermost.Channel, error)
	Member(context.Context, string, string) (mattermost.ChannelMember, error)
}

type PostTargets interface {
	ByID(context.Context, string) (mattermost.Post, error)
	ReactionState(context.Context, string, string, string, string) (bool, error)
}

type Service struct {
	serverURL, serverID string
	store               Store
	users               CurrentUser
	channels            Conversations
	posts               PostTargets
	writes              *mattermost.ConversationMutations
	postWrites          *mattermost.PostMutations
	credentials         [][]byte
}

func New(serverURL, serverID string, credentials [][]byte, store Store, users CurrentUser, channels Conversations, posts PostTargets, writes *mattermost.ConversationMutations, postWrites *mattermost.PostMutations) (*Service, error) {
	protected, validCredentials := cloneCredentials(credentials)
	if serverURL == "" || !validCredentials || store == nil || users == nil || channels == nil || posts == nil || writes == nil || postWrites == nil {
		return nil, ErrInvalid
	}
	return &Service{serverURL: serverURL, serverID: serverID, store: store, users: users, channels: channels, posts: posts, writes: writes, postWrites: postWrites, credentials: protected}, nil
}

func (s *Service) Apply(ctx context.Context, in stagestore.ApplyClaimInput) (stagestore.ApplyReceipt, error) {
	if ctx == nil || s == nil {
		return stagestore.ApplyReceipt{}, ErrInvalid
	}
	if s.containsCredential(in.RequestID) {
		return stagestore.ApplyReceipt{}, ErrCredential
	}
	detail, err := s.store.Show(ctx, in.StageID)
	if err != nil {
		return stagestore.ApplyReceipt{}, err
	}
	if detail.ServerURL != s.serverURL || detail.ServerID != s.serverID {
		return stagestore.ApplyReceipt{}, ErrTargetDrift
	}
	if detail.Operation != stagestore.ResolveDM && detail.Operation != stagestore.ResolveGroupDM && detail.Operation != stagestore.React && detail.Operation != stagestore.Unreact {
		return stagestore.ApplyReceipt{}, ErrUnsupportedOperation
	}
	var destination staging.Destination
	if detail.Operation == stagestore.ResolveDM || detail.Operation == stagestore.ResolveGroupDM {
		destination, err = decodeResolveDestination(detail.Operation, detail.Destination, detail.UserID)
	} else {
		destination, err = decodeReactionDestination(detail.Destination)
	}
	if err != nil {
		return stagestore.ApplyReceipt{}, err
	}
	if s.destinationContainsCredential(destination, detail.UserID) {
		return stagestore.ApplyReceipt{}, ErrCredential
	}
	if in.RequestID != "" {
		replay, found, findErr := s.store.FindApply(ctx, detail.ServerURL, detail.UserID, in.RequestID, in.RequestDigest)
		if findErr != nil {
			return stagestore.ApplyReceipt{}, findErr
		}
		if found {
			if replay.StageID != in.StageID || replay.Revision != in.Revision || replay.SemanticDigest != in.ExpectedDigest || replay.RecoveryMode != in.RecoveryMode {
				return stagestore.ApplyReceipt{}, stagestore.ErrConflict
			}
			return s.finalizeReceipt(ctx, replay.ID)
		}
	}
	if detail.Revision != in.Revision || detail.SemanticDigest != in.ExpectedDigest {
		return stagestore.ApplyReceipt{}, stagestore.ErrConflict
	}
	attempt, err := s.store.ClaimApply(ctx, in)
	if err != nil {
		return stagestore.ApplyReceipt{}, err
	}
	if attempt.Replay {
		return s.finalizeReceipt(ctx, attempt.ID)
	}
	current, err := s.users.Current(ctx)
	if err != nil {
		return stagestore.ApplyReceipt{}, s.abandon(ctx, attempt.ID, err)
	}
	if current.ID != detail.UserID {
		return stagestore.ApplyReceipt{}, s.abandon(ctx, attempt.ID, ErrTargetDrift)
	}
	if detail.Operation == stagestore.React || detail.Operation == stagestore.Unreact {
		return s.applyReaction(ctx, attempt, detail.Operation, current.ID, destination)
	}
	return s.applyConversation(ctx, attempt, detail.Operation, current.ID, destination)
}

func (s *Service) applyConversation(ctx context.Context, attempt stagestore.ApplyAttempt, operation stagestore.Operation, currentUserID string, destination staging.Destination) (stagestore.ApplyReceipt, error) {
	var prepared *mattermost.PreparedResolveConversation
	var err error
	input := mattermost.ResolveConversationMutationInput{CurrentUserID: currentUserID, ParticipantIDs: destination.ParticipantIDs}
	if operation == stagestore.ResolveDM {
		prepared, err = s.writes.PrepareDirect(input)
	} else {
		prepared, err = s.writes.PrepareGroup(input)
	}
	if err != nil {
		return stagestore.ApplyReceipt{}, s.abandon(ctx, attempt.ID, err)
	}
	_, found, err := s.findConversation(ctx, operation, currentUserID, destination.ParticipantIDs)
	if err != nil {
		return stagestore.ApplyReceipt{}, s.abandon(ctx, attempt.ID, errors.Join(ErrTargetDrift, err))
	}
	if found {
		return s.skip(ctx, attempt.ID)
	}
	if err = s.store.BeginDispatch(ctx, attempt.ID, 1); err != nil {
		return stagestore.ApplyReceipt{}, s.abandon(ctx, attempt.ID, err)
	}
	result, remoteErr := prepared.Execute(ctx)
	if remoteErr != nil {
		return s.recordRemoteFailure(ctx, attempt.ID, remoteErr)
	}
	validated, found, validationErr := s.findConversation(ctx, operation, currentUserID, destination.ParticipantIDs)
	if validationErr != nil || !found || validated.ID != result.ChannelID {
		if err = s.store.MarkStepUnknown(context.WithoutCancel(ctx), attempt.ID, 1); err != nil {
			return stagestore.ApplyReceipt{}, errors.Join(&api.OutcomeUnknownError{}, fmt.Errorf("%w: %v", ErrJournal, err))
		}
		receipt, finalizeErr := s.finalizeReceipt(context.WithoutCancel(ctx), attempt.ID)
		if finalizeErr != nil {
			return stagestore.ApplyReceipt{}, errors.Join(&api.OutcomeUnknownError{}, fmt.Errorf("%w: %v", ErrJournal, finalizeErr))
		}
		return receipt, nil
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return stagestore.ApplyReceipt{}, &ConfirmedEffectError{err}
	}
	if err = s.store.MarkStepValidated(context.WithoutCancel(ctx), attempt.ID, 1, encoded); err != nil {
		return stagestore.ApplyReceipt{}, &ConfirmedEffectError{err}
	}
	receipt, err := s.finalizeReceipt(context.WithoutCancel(ctx), attempt.ID)
	if err != nil {
		return stagestore.ApplyReceipt{}, &ConfirmedEffectError{err}
	}
	return receipt, nil
}

func (s *Service) findConversation(ctx context.Context, operation stagestore.Operation, currentUserID string, participantIDs []string) (mattermost.Channel, bool, error) {
	if operation == stagestore.ResolveDM {
		return s.channels.ExistingDirect(ctx, currentUserID, participantIDs[0])
	}
	return s.channels.ExistingGroup(ctx, currentUserID, participantIDs)
}

func (s *Service) recordRemoteFailure(ctx context.Context, attemptID string, remoteErr error) (stagestore.ApplyReceipt, error) {
	journalCtx := context.WithoutCancel(ctx)
	var rejected *api.APIError
	var err error
	if errors.As(remoteErr, &rejected) && rejected.Status >= 400 && rejected.Status <= 499 {
		result, marshalErr := json.Marshal(struct {
			Status int `json:"status"`
		}{rejected.Status})
		if marshalErr != nil {
			return stagestore.ApplyReceipt{}, fmt.Errorf("%w: %v", ErrJournal, marshalErr)
		}
		err = s.store.MarkStepRejected(journalCtx, attemptID, 1, result)
	} else {
		err = s.store.MarkStepUnknown(journalCtx, attemptID, 1)
	}
	if err != nil {
		return stagestore.ApplyReceipt{}, errors.Join(remoteErr, fmt.Errorf("%w: %v", ErrJournal, err))
	}
	receipt, finalizeErr := s.finalizeReceipt(journalCtx, attemptID)
	if finalizeErr != nil {
		return stagestore.ApplyReceipt{}, errors.Join(remoteErr, fmt.Errorf("%w: %v", ErrJournal, finalizeErr))
	}
	return receipt, nil
}

func (s *Service) abandon(ctx context.Context, attemptID string, cause error) error {
	if err := s.store.AbandonApplyBeforeDispatch(context.WithoutCancel(ctx), attemptID); err != nil {
		return fmt.Errorf("%w: %v", ErrJournal, err)
	}
	return cause
}

func (s *Service) skip(ctx context.Context, attemptID string) (stagestore.ApplyReceipt, error) {
	journalCtx := context.WithoutCancel(ctx)
	if err := s.store.MarkStepSkipped(journalCtx, attemptID, 1, json.RawMessage(`{"reason":"already_satisfied"}`)); err != nil {
		return stagestore.ApplyReceipt{}, s.abandon(journalCtx, attemptID, fmt.Errorf("%w: %v", ErrJournal, err))
	}
	return s.finalizeReceipt(journalCtx, attemptID)
}

func (s *Service) finalizeReceipt(ctx context.Context, attemptID string) (stagestore.ApplyReceipt, error) {
	receipt, err := s.store.FinalizeApply(ctx, attemptID)
	if err != nil {
		return stagestore.ApplyReceipt{}, err
	}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		return stagestore.ApplyReceipt{}, fmt.Errorf("%w: encode receipt: %v", ErrJournal, err)
	}
	if s.rawContainsCredentialValue(encoded) {
		return stagestore.ApplyReceipt{}, ErrCredential
	}
	return receipt, nil
}

func decodeResolveDestination(operation stagestore.Operation, raw json.RawMessage, currentUserID string) (staging.Destination, error) {
	if !canonicalDestination(raw) {
		return staging.Destination{}, ErrInvalid
	}
	var wire struct {
		Kind            string             `json:"kind"`
		ChannelID       *string            `json:"channelId"`
		ChannelType     *string            `json:"channelType"`
		TeamID          *string            `json:"teamId"`
		PostID          *string            `json:"postId"`
		RootPostID      *string            `json:"rootPostId"`
		ParticipantIDs  []string           `json:"participantIds"`
		Emoji           *string            `json:"emoji"`
		PostState       *staging.PostState `json:"postState"`
		ReactionPresent *bool              `json:"reactionPresent"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&wire) != nil || decoder.Decode(new(any)) != io.EOF || wire.ChannelType == nil {
		return staging.Destination{}, ErrInvalid
	}
	destination := staging.Destination{Kind: wire.Kind, ChannelType: *wire.ChannelType, TeamID: wire.TeamID, PostID: wire.PostID, RootPostID: wire.RootPostID, ParticipantIDs: wire.ParticipantIDs, Emoji: wire.Emoji, PostState: wire.PostState, ReactionPresent: wire.ReactionPresent}
	if destination.Kind != "conversation" || wire.ChannelID != nil || destination.TeamID != nil || destination.PostID != nil || destination.RootPostID != nil || destination.Emoji != nil || destination.PostState != nil || destination.ReactionPresent != nil {
		return staging.Destination{}, ErrInvalid
	}
	wantType, wantCount := "dm", 1
	if operation == stagestore.ResolveGroupDM {
		wantType, wantCount = "group", 2
	}
	if destination.ChannelType != wantType || len(destination.ParticipantIDs) < wantCount || operation == stagestore.ResolveDM && len(destination.ParticipantIDs) != 1 || operation == stagestore.ResolveGroupDM && len(destination.ParticipantIDs) > 7 || !slices.IsSorted(destination.ParticipantIDs) {
		return staging.Destination{}, ErrInvalid
	}
	for index, id := range destination.ParticipantIDs {
		if id == "" || id == currentUserID || index > 0 && id == destination.ParticipantIDs[index-1] {
			return staging.Destination{}, ErrInvalid
		}
	}
	return destination, nil
}

func canonicalDestination(raw json.RawMessage) bool {
	var canonical map[string]any
	if json.Unmarshal(raw, &canonical) != nil || len(canonical) != 10 {
		return false
	}
	encoded, err := json.Marshal(canonical)
	return err == nil && bytes.Equal(encoded, raw)
}

func cloneCredentials(values [][]byte) ([][]byte, bool) {
	if len(values) == 0 {
		return nil, false
	}
	cloned := make([][]byte, len(values))
	for index, value := range values {
		if len(value) == 0 {
			return nil, false
		}
		cloned[index] = bytes.Clone(value)
	}
	return cloned, true
}

func (s *Service) destinationContainsCredential(destination staging.Destination, currentUserID string) bool {
	values := []string{currentUserID, destination.ChannelID, destination.ChannelType}
	values = append(values, destination.ParticipantIDs...)
	for _, optional := range []*string{destination.TeamID, destination.PostID, destination.RootPostID, destination.Emoji} {
		if optional != nil {
			values = append(values, *optional)
		}
	}
	return s.containsCredential(values...)
}

func (s *Service) containsCredential(values ...string) bool {
	for _, value := range values {
		for _, credential := range s.credentials {
			if bytes.Contains([]byte(value), credential) {
				return true
			}
		}
	}
	return false
}

func (s *Service) rawContainsCredentialValue(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return true
	}
	var visit func(any) bool
	visit = func(candidate any) bool {
		switch typed := candidate.(type) {
		case string:
			return s.containsCredential(typed)
		case []any:
			for _, item := range typed {
				if visit(item) {
					return true
				}
			}
		case map[string]any:
			for _, item := range typed {
				if visit(item) {
					return true
				}
			}
		}
		return false
	}
	return visit(value)
}
