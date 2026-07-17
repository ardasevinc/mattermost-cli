package apply

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/ardasevinc/mattermost-cli/internal/api"
	"github.com/ardasevinc/mattermost-cli/internal/mattermost"
	"github.com/ardasevinc/mattermost-cli/internal/stagestore"
	"github.com/ardasevinc/mattermost-cli/internal/staging"
)

type preparedReaction interface {
	Execute(context.Context) (mattermost.ReactionMutationResult, error)
}

func (s *Service) applyReaction(ctx context.Context, attempt stagestore.ApplyAttempt, operation stagestore.Operation, currentUserID string, destination staging.Destination) (stagestore.ApplyReceipt, error) {
	input := mattermost.ReactionMutationInput{PostID: *destination.PostID, ChannelID: destination.ChannelID, UserID: currentUserID, Emoji: *destination.Emoji}
	var prepared preparedReaction
	var err error
	if operation == stagestore.React {
		prepared, err = s.postWrites.PrepareAddReaction(input)
	} else {
		prepared, err = s.postWrites.PrepareRemoveReaction(input)
	}
	if err != nil {
		return stagestore.ApplyReceipt{}, s.abandon(ctx, attempt.ID, err)
	}
	present, err := s.revalidateReaction(ctx, currentUserID, destination)
	if err != nil {
		return stagestore.ApplyReceipt{}, s.abandon(ctx, attempt.ID, errors.Join(ErrTargetDrift, err))
	}
	if operation == stagestore.React && present || operation == stagestore.Unreact && !present {
		return s.skip(ctx, attempt.ID)
	}
	if err = s.store.BeginDispatch(ctx, attempt.ID, 1); err != nil {
		return stagestore.ApplyReceipt{}, s.abandon(ctx, attempt.ID, err)
	}
	result, remoteErr := prepared.Execute(ctx)
	if remoteErr != nil {
		return s.recordRemoteFailure(ctx, attempt.ID, remoteErr)
	}
	present, validationErr := s.revalidateReaction(ctx, currentUserID, destination)
	if validationErr != nil || operation == stagestore.React && !present || operation == stagestore.Unreact && present {
		return s.recordUnvalidatedSuccess(ctx, attempt.ID)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return stagestore.ApplyReceipt{}, &ConfirmedEffectError{err}
	}
	journalCtx := context.WithoutCancel(ctx)
	if err = s.store.MarkStepValidated(journalCtx, attempt.ID, 1, encoded); err != nil {
		return stagestore.ApplyReceipt{}, &ConfirmedEffectError{err}
	}
	receipt, err := s.finalizeReceipt(journalCtx, attempt.ID)
	if err != nil {
		return stagestore.ApplyReceipt{}, &ConfirmedEffectError{err}
	}
	return receipt, nil
}

func (s *Service) recordUnvalidatedSuccess(ctx context.Context, attemptID string) (stagestore.ApplyReceipt, error) {
	journalCtx := context.WithoutCancel(ctx)
	if err := s.store.MarkStepUnknown(journalCtx, attemptID, 1); err != nil {
		return stagestore.ApplyReceipt{}, errors.Join(&api.OutcomeUnknownError{}, errors.Join(ErrJournal, err))
	}
	receipt, err := s.finalizeReceipt(journalCtx, attemptID)
	if err != nil {
		return stagestore.ApplyReceipt{}, errors.Join(&api.OutcomeUnknownError{}, errors.Join(ErrJournal, err))
	}
	return receipt, nil
}

func (s *Service) revalidateReaction(ctx context.Context, currentUserID string, destination staging.Destination) (bool, error) {
	post, err := s.posts.ByID(ctx, *destination.PostID)
	if err != nil {
		return false, err
	}
	wantRoot := ""
	if destination.RootPostID != nil {
		wantRoot = *destination.RootPostID
	}
	if post.ID != *destination.PostID || post.ChannelID != destination.ChannelID || post.RootID != wantRoot {
		return false, ErrTargetDrift
	}
	if destination.ChannelType == "dm" {
		channel, found, directErr := s.channels.ExistingDirect(ctx, currentUserID, destination.ParticipantIDs[0])
		if directErr != nil {
			return false, directErr
		}
		if !found || channel.ID != destination.ChannelID {
			return false, ErrTargetDrift
		}
		return s.posts.ReactionState(ctx, *destination.PostID, destination.ChannelID, currentUserID, *destination.Emoji)
	}
	channel, err := s.channels.ByID(ctx, destination.ChannelID)
	if err != nil {
		return false, err
	}
	if !channelMatchesDestination(channel, destination, currentUserID) {
		return false, ErrTargetDrift
	}
	member, err := s.channels.Member(ctx, destination.ChannelID, currentUserID)
	if err != nil {
		return false, err
	}
	if member.ChannelID != destination.ChannelID || member.UserID != currentUserID {
		return false, ErrTargetDrift
	}
	return s.posts.ReactionState(ctx, *destination.PostID, destination.ChannelID, currentUserID, *destination.Emoji)
}

func channelMatchesDestination(channel mattermost.Channel, destination staging.Destination, currentUserID string) bool {
	if channel.ID != destination.ChannelID || map[string]string{"D": "dm", "G": "group", "O": "public", "P": "private"}[channel.Type] != destination.ChannelType {
		return false
	}
	if channel.Type == "O" || channel.Type == "P" {
		return destination.TeamID != nil && *destination.TeamID == channel.TeamID && len(destination.ParticipantIDs) == 0
	}
	if destination.TeamID != nil || channel.TeamID != "" {
		return false
	}
	if channel.Type != "D" {
		return len(destination.ParticipantIDs) == 0
	}
	parts := strings.Split(channel.Name, "__")
	if len(parts) != 2 {
		return false
	}
	participants := []string{}
	if parts[0] == currentUserID && parts[1] == currentUserID {
		participants = []string{currentUserID}
	} else if parts[0] == currentUserID {
		participants = []string{parts[1]}
	} else if parts[1] == currentUserID {
		participants = []string{parts[0]}
	}
	return slices.Equal(participants, destination.ParticipantIDs)
}

func decodeReactionDestination(raw json.RawMessage) (staging.Destination, error) {
	if !canonicalDestination(raw) {
		return staging.Destination{}, ErrInvalid
	}
	var destination staging.Destination
	if json.Unmarshal(raw, &destination) != nil || destination.Kind != "reaction" || !safeID(destination.ChannelID) || destination.PostID == nil || !safeID(*destination.PostID) || destination.Emoji == nil || !safeEmoji(*destination.Emoji) || destination.PostState != nil || destination.ReactionPresent == nil {
		return staging.Destination{}, ErrInvalid
	}
	if destination.RootPostID != nil && !safeID(*destination.RootPostID) {
		return staging.Destination{}, ErrInvalid
	}
	switch destination.ChannelType {
	case "dm":
		if destination.TeamID != nil || len(destination.ParticipantIDs) != 1 || !safeID(destination.ParticipantIDs[0]) {
			return staging.Destination{}, ErrInvalid
		}
	case "group":
		if destination.TeamID != nil || len(destination.ParticipantIDs) != 0 {
			return staging.Destination{}, ErrInvalid
		}
	case "public", "private":
		if destination.TeamID == nil || !safeID(*destination.TeamID) || len(destination.ParticipantIDs) != 0 {
			return staging.Destination{}, ErrInvalid
		}
	default:
		return staging.Destination{}, ErrInvalid
	}
	return destination, nil
}

func safeID(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for index := range len(value) {
		character := value[index]
		if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '_' || character == '-') {
			return false
		}
	}
	return true
}

func safeEmoji(value string) bool {
	if value != strings.ToLower(value) || len(value) == 0 || len(value) > 64 {
		return false
	}
	for index := range len(value) {
		character := value[index]
		if !((character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '_' || character == '-' || character == '+') {
			return false
		}
	}
	return true
}
