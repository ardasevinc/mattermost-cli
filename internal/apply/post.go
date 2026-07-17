package apply

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/ardasevinc/mattermost-cli/internal/mattermost"
	"github.com/ardasevinc/mattermost-cli/internal/stageinput"
	"github.com/ardasevinc/mattermost-cli/internal/stagestore"
	"github.com/ardasevinc/mattermost-cli/internal/staging"
)

func (s *Service) applyPost(ctx context.Context, attempt stagestore.ApplyAttempt, operation stagestore.Operation, currentUserID string, destination staging.Destination, body []byte, attachments []stagestore.Attachment) (stagestore.ApplyReceipt, error) {
	if len(attachments) > 0 {
		return s.applyPostWithAttachments(ctx, attempt, operation, currentUserID, destination, body, attachments)
	}
	switch operation {
	case stagestore.CreatePost, stagestore.Reply:
		rootID := ""
		if destination.RootPostID != nil {
			rootID = *destination.RootPostID
		}
		prepared, err := s.postWrites.PrepareCreate(mattermost.CreatePostMutationInput{
			ChannelID: destination.ChannelID, UserID: currentUserID, Message: string(body), RootID: rootID, PendingPostID: attempt.PendingPostID,
		})
		if err != nil {
			return stagestore.ApplyReceipt{}, s.abandon(ctx, attempt.ID, err)
		}
		if err = s.revalidatePostTarget(ctx, operation, currentUserID, destination); err != nil {
			return stagestore.ApplyReceipt{}, s.abandon(ctx, attempt.ID, errors.Join(ErrTargetDrift, err))
		}
		return executePrepared(s, ctx, attempt.ID, 1, prepared.Execute)
	case stagestore.EditPost:
		post, err := s.revalidateBoundPost(ctx, currentUserID, destination)
		if err != nil {
			return stagestore.ApplyReceipt{}, s.abandon(ctx, attempt.ID, errors.Join(ErrTargetDrift, err))
		}
		if post.Message == string(body) {
			return s.skip(ctx, attempt.ID)
		}
		prepared, err := s.postWrites.PrepareEdit(mattermost.EditPostMutationInput{
			PostID: post.ID, ChannelID: post.ChannelID, UserID: currentUserID, Message: string(body), RootID: post.RootID, FileIDs: post.FileIDs,
		})
		if err != nil {
			return stagestore.ApplyReceipt{}, s.abandon(ctx, attempt.ID, err)
		}
		return executePrepared(s, ctx, attempt.ID, 1, prepared.Execute)
	case stagestore.DeletePost:
		prepared, err := s.postWrites.PrepareDelete(mattermost.DeletePostMutationInput{PostID: *destination.PostID})
		if err != nil {
			return stagestore.ApplyReceipt{}, s.abandon(ctx, attempt.ID, err)
		}
		if _, err = s.revalidateBoundPost(ctx, currentUserID, destination); err != nil {
			return stagestore.ApplyReceipt{}, s.abandon(ctx, attempt.ID, errors.Join(ErrTargetDrift, err))
		}
		return executePrepared(s, ctx, attempt.ID, 1, prepared.Execute)
	default:
		return stagestore.ApplyReceipt{}, s.abandon(ctx, attempt.ID, ErrUnsupportedOperation)
	}
}

func executePrepared[T any](s *Service, ctx context.Context, attemptID string, ordinal int, execute func(context.Context) (T, error)) (stagestore.ApplyReceipt, error) {
	if err := s.store.BeginDispatch(ctx, attemptID, ordinal); err != nil {
		if ordinal > 1 {
			return s.stopCompoundBeforeDispatch(ctx, attemptID, err)
		}
		return stagestore.ApplyReceipt{}, s.abandon(ctx, attemptID, err)
	}
	result, remoteErr := execute(ctx)
	if remoteErr != nil {
		return s.recordRemoteFailure(ctx, attemptID, ordinal, remoteErr)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return stagestore.ApplyReceipt{}, &ConfirmedEffectError{err}
	}
	journalCtx := context.WithoutCancel(ctx)
	if err = s.store.MarkStepValidated(journalCtx, attemptID, ordinal, encoded); err != nil {
		return stagestore.ApplyReceipt{}, &ConfirmedEffectError{err}
	}
	receipt, err := s.finalizeReceipt(journalCtx, attemptID)
	if err != nil {
		return stagestore.ApplyReceipt{}, &ConfirmedEffectError{err}
	}
	return receipt, nil
}

func (s *Service) applyPostWithAttachments(ctx context.Context, attempt stagestore.ApplyAttempt, operation stagestore.Operation, currentUserID string, destination staging.Destination, body []byte, attachments []stagestore.Attachment) (stagestore.ApplyReceipt, error) {
	if operation != stagestore.CreatePost && operation != stagestore.Reply || len(attachments) == 0 || len(attachments) > stageinput.MaxAttachments {
		return stagestore.ApplyReceipt{}, s.abandon(ctx, attempt.ID, ErrUnsupportedOperation)
	}
	if err := s.revalidatePostTarget(ctx, operation, currentUserID, destination); err != nil {
		return stagestore.ApplyReceipt{}, s.abandon(ctx, attempt.ID, errors.Join(ErrTargetDrift, err))
	}
	spools := make([]*stageinput.Spool, len(attachments))
	defer func() {
		for _, spool := range spools {
			if spool != nil {
				_ = spool.Close()
			}
		}
	}()
	for i, attachment := range attachments {
		spool, err := stageinput.Snapshot(ctx, attachment, s.credentials, s.spoolDirectory)
		if err != nil {
			return stagestore.ApplyReceipt{}, s.abandon(ctx, attempt.ID, errors.Join(ErrTargetDrift, err))
		}
		spools[i] = spool
	}

	fileIDs := make([]string, 0, len(spools))
	for i, spool := range spools {
		ordinal := i + 1
		prepared, err := s.fileWrites.PrepareUpload(mattermost.UploadMutationInput{
			ChannelID: destination.ChannelID, UserID: currentUserID, Filename: spool.RemoteFilename, MediaType: spool.MediaType, Length: spool.Length, Body: spool,
		})
		if err != nil {
			return s.stopCompoundBeforeDispatch(ctx, attempt.ID, err)
		}
		spools[i] = nil // ownership transferred to the prepared mutation
		if err = s.store.BeginDispatch(ctx, attempt.ID, ordinal); err != nil {
			_ = prepared.Close()
			return s.stopCompoundBeforeDispatch(ctx, attempt.ID, err)
		}
		result, remoteErr := prepared.Execute(ctx)
		if remoteErr != nil {
			return s.recordRemoteFailure(ctx, attempt.ID, ordinal, remoteErr)
		}
		if s.containsCredential(result.FileID) {
			journalCtx := context.WithoutCancel(ctx)
			if err = s.store.MarkStepUnknown(journalCtx, attempt.ID, ordinal); err != nil {
				return stagestore.ApplyReceipt{}, &ConfirmedEffectError{err}
			}
			return s.finalizeReceipt(journalCtx, attempt.ID)
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			return stagestore.ApplyReceipt{}, &ConfirmedEffectError{err}
		}
		if err = s.store.MarkStepValidated(context.WithoutCancel(ctx), attempt.ID, ordinal, encoded); err != nil {
			return stagestore.ApplyReceipt{}, &ConfirmedEffectError{err}
		}
		fileIDs = append(fileIDs, result.FileID)
	}

	if err := s.revalidatePostTarget(ctx, operation, currentUserID, destination); err != nil {
		if sealErr := s.store.SealRemainingNotDispatched(context.WithoutCancel(ctx), attempt.ID); sealErr != nil {
			return stagestore.ApplyReceipt{}, &ConfirmedEffectError{sealErr}
		}
		return s.finalizeReceipt(context.WithoutCancel(ctx), attempt.ID)
	}
	rootID := ""
	if destination.RootPostID != nil {
		rootID = *destination.RootPostID
	}
	prepared, err := s.postWrites.PrepareCreate(mattermost.CreatePostMutationInput{ChannelID: destination.ChannelID, UserID: currentUserID,
		Message: string(body), RootID: rootID, FileIDs: fileIDs, PendingPostID: attempt.PendingPostID})
	if err != nil {
		return s.stopCompoundBeforeDispatch(ctx, attempt.ID, err)
	}
	return executePrepared(s, ctx, attempt.ID, len(attachments)+1, prepared.Execute)
}

func (s *Service) stopCompoundBeforeDispatch(ctx context.Context, attemptID string, cause error) (stagestore.ApplyReceipt, error) {
	journalCtx := context.WithoutCancel(ctx)
	if err := s.store.SealRemainingNotDispatched(journalCtx, attemptID); err != nil {
		if errors.Is(err, stagestore.ErrNotEligible) {
			return stagestore.ApplyReceipt{}, s.abandon(ctx, attemptID, cause)
		}
		return stagestore.ApplyReceipt{}, &ConfirmedEffectError{err}
	}
	receipt, err := s.finalizeReceipt(journalCtx, attemptID)
	if err != nil {
		return stagestore.ApplyReceipt{}, &ConfirmedEffectError{err}
	}
	return receipt, nil
}

func (s *Service) revalidatePostTarget(ctx context.Context, operation stagestore.Operation, currentUserID string, destination staging.Destination) error {
	if err := s.revalidateChannelAccess(ctx, currentUserID, destination); err != nil {
		return err
	}
	if operation != stagestore.Reply {
		return nil
	}
	post, err := s.posts.ByID(ctx, *destination.PostID)
	if err != nil {
		return err
	}
	if post.ID != *destination.PostID || post.ChannelID != destination.ChannelID {
		return ErrTargetDrift
	}
	if post.RootID == "" {
		if destination.RootPostID == nil || *destination.RootPostID != post.ID {
			return ErrTargetDrift
		}
	} else if destination.RootPostID == nil || *destination.RootPostID != post.RootID {
		return ErrTargetDrift
	}
	root, err := s.posts.ByID(ctx, *destination.RootPostID)
	if err != nil {
		return err
	}
	if root.ID != *destination.RootPostID || root.ChannelID != destination.ChannelID || root.RootID != "" {
		return ErrTargetDrift
	}
	return nil
}

func (s *Service) revalidateBoundPost(ctx context.Context, currentUserID string, destination staging.Destination) (mattermost.Post, error) {
	post, err := s.posts.ByID(ctx, *destination.PostID)
	if err != nil {
		return mattermost.Post{}, err
	}
	state := destination.PostState
	if post.ID != *destination.PostID || post.ChannelID != destination.ChannelID || post.UserID != currentUserID || post.Type != "" || state == nil ||
		post.UserID != state.AuthorUserID || post.UpdateAt != state.UpdateAt || staging.PostContentDigest(post, s.credentials) != state.ContentDigest {
		return mattermost.Post{}, ErrTargetDrift
	}
	wantRoot := ""
	if destination.RootPostID != nil {
		wantRoot = *destination.RootPostID
	}
	if post.RootID != wantRoot {
		return mattermost.Post{}, ErrTargetDrift
	}
	if err = s.revalidateChannelAccess(ctx, currentUserID, destination); err != nil {
		return mattermost.Post{}, err
	}
	return post, nil
}

func (s *Service) revalidateChannelAccess(ctx context.Context, currentUserID string, destination staging.Destination) error {
	if destination.ChannelType == "dm" {
		channel, found, err := s.channels.ExistingDirect(ctx, currentUserID, destination.ParticipantIDs[0])
		if err != nil {
			return err
		}
		if !found || channel.ID != destination.ChannelID {
			return ErrTargetDrift
		}
		return nil
	}
	channel, err := s.channels.ByID(ctx, destination.ChannelID)
	if err != nil {
		return err
	}
	if !channelMatchesDestination(channel, destination, currentUserID) {
		return ErrTargetDrift
	}
	member, err := s.channels.Member(ctx, destination.ChannelID, currentUserID)
	if err != nil {
		return err
	}
	if member.ChannelID != destination.ChannelID || member.UserID != currentUserID {
		return ErrTargetDrift
	}
	return nil
}

func decodePostDestination(operation stagestore.Operation, raw json.RawMessage) (staging.Destination, error) {
	if !canonicalDestination(raw) {
		return staging.Destination{}, ErrInvalid
	}
	var destination staging.Destination
	if json.Unmarshal(raw, &destination) != nil || !validDestinationChannel(destination) || destination.Emoji != nil || destination.ReactionPresent != nil {
		return staging.Destination{}, ErrInvalid
	}
	switch operation {
	case stagestore.CreatePost:
		if destination.Kind != "conversation" || destination.PostID != nil || destination.RootPostID != nil || destination.PostState != nil {
			return staging.Destination{}, ErrInvalid
		}
	case stagestore.Reply:
		if destination.Kind != "post" || destination.PostID == nil || !safeID(*destination.PostID) || destination.RootPostID == nil || !safeID(*destination.RootPostID) || destination.PostState != nil {
			return staging.Destination{}, ErrInvalid
		}
	case stagestore.EditPost, stagestore.DeletePost:
		if destination.Kind != "post" || destination.PostID == nil || !safeID(*destination.PostID) || destination.PostState == nil || !validPostState(*destination.PostState) {
			return staging.Destination{}, ErrInvalid
		}
		if destination.RootPostID != nil && !safeID(*destination.RootPostID) {
			return staging.Destination{}, ErrInvalid
		}
	default:
		return staging.Destination{}, ErrInvalid
	}
	return destination, nil
}

func validDestinationChannel(destination staging.Destination) bool {
	if !safeID(destination.ChannelID) {
		return false
	}
	switch destination.ChannelType {
	case "dm":
		return destination.TeamID == nil && len(destination.ParticipantIDs) == 1 && safeID(destination.ParticipantIDs[0])
	case "group":
		return destination.TeamID == nil && len(destination.ParticipantIDs) == 0
	case "public", "private":
		return destination.TeamID != nil && safeID(*destination.TeamID) && len(destination.ParticipantIDs) == 0
	}
	return false
}

func validPostState(state staging.PostState) bool {
	if !safeID(state.AuthorUserID) || state.UpdateAt <= 0 || len(state.ContentDigest) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(state.ContentDigest)
	return err == nil && len(decoded) == 32 && state.ContentDigest == strings.ToLower(state.ContentDigest)
}
