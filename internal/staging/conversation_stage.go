package staging

import (
	"context"
	"sort"
	"strings"

	"github.com/ardasevinc/mattermost-cli/internal/stageinput"
	"github.com/ardasevinc/mattermost-cli/internal/stagestore"
)

type resolveDMIntent struct {
	Username string `json:"username"`
}

type resolveGroupIntent struct {
	Usernames []string `json:"usernames"`
}

func (s *Service) DryRunResolveDM(ctx context.Context, target Target) (Preview, error) {
	current, err := s.authenticate(ctx)
	if err != nil {
		return Preview{}, err
	}
	return s.resolveDMFor(ctx, current.ID, target)
}

func (s *Service) ResolveDM(ctx context.Context, in ResolveDMInput) (CreatePostResult, error) {
	if nilDependency(s.store) || !validRequestID(in.RequestID) || !validDMCreateTarget(in.Target) {
		return CreatePostResult{}, ErrInvalid
	}
	if contaminated(s.credentials, append([]string{in.RequestID}, targetStrings(in.Target)...)...) {
		return CreatePostResult{}, ErrCredential
	}
	current, err := s.authenticate(ctx)
	if err != nil {
		return CreatePostResult{}, err
	}
	digest := intentDigest(stagestore.ResolveDM, resolveDMIntent{in.Target.Value}, nil, "", []stageinput.MetadataIntent{})
	record, found, err := s.findCreate(ctx, current.ID, in.RequestID)
	if err != nil {
		return CreatePostResult{}, err
	}
	if found {
		return replayResult(record, digest, stagestore.ResolveDM, s.serverURL, current.ID)
	}
	preview, err := s.resolveDMFor(ctx, current.ID, in.Target)
	if err != nil {
		return CreatePostResult{}, err
	}
	return s.persistPost(ctx, in.RequestID, digest, stagestore.ResolveDM, preview, nil, nil)
}

func (s *Service) resolveDMFor(ctx context.Context, currentUserID string, target Target) (Preview, error) {
	if !validDMCreateTarget(target) || contaminated(s.credentials, targetStrings(target)...) {
		if contaminated(s.credentials, targetStrings(target)...) {
			return Preview{}, ErrCredential
		}
		return Preview{}, ErrInvalid
	}
	peer, err := s.users.ByUsernameFresh(ctx, target.Value)
	if err != nil {
		return Preview{}, targetReadError(err)
	}
	if !validResolvedUser(peer) || !strings.EqualFold(peer.Username, target.Value) || peer.ID == currentUserID {
		return Preview{}, ErrTarget
	}
	if contaminated(s.credentials, peer.ID, peer.Username) {
		return Preview{}, ErrCredential
	}
	return s.unresolvedConversationPreview(currentUserID, "dm", []string{peer.ID})
}

func validDMCreateTarget(target Target) bool {
	return target.Conversation == Direct && target.Selector == ByUsername && target.Team == nil && validSelectorValue(target.Value)
}

func (s *Service) DryRunResolveGroup(ctx context.Context, usernames []string) (Preview, error) {
	current, err := s.authenticate(ctx)
	if err != nil {
		return Preview{}, err
	}
	return s.resolveGroupFor(ctx, current.ID, usernames)
}

func (s *Service) ResolveGroup(ctx context.Context, in ResolveGroupInput) (CreatePostResult, error) {
	if nilDependency(s.store) || !validRequestID(in.RequestID) || !validGroupUsernames(in.Usernames) {
		return CreatePostResult{}, ErrInvalid
	}
	fields := append([]string{in.RequestID}, in.Usernames...)
	if contaminated(s.credentials, fields...) {
		return CreatePostResult{}, ErrCredential
	}
	current, err := s.authenticate(ctx)
	if err != nil {
		return CreatePostResult{}, err
	}
	digest := intentDigest(stagestore.ResolveGroupDM, resolveGroupIntent{append([]string(nil), in.Usernames...)}, nil, "", []stageinput.MetadataIntent{})
	record, found, err := s.findCreate(ctx, current.ID, in.RequestID)
	if err != nil {
		return CreatePostResult{}, err
	}
	if found {
		return replayResult(record, digest, stagestore.ResolveGroupDM, s.serverURL, current.ID)
	}
	preview, err := s.resolveGroupFor(ctx, current.ID, in.Usernames)
	if err != nil {
		return CreatePostResult{}, err
	}
	return s.persistPost(ctx, in.RequestID, digest, stagestore.ResolveGroupDM, preview, nil, nil)
}

func (s *Service) resolveGroupFor(ctx context.Context, currentUserID string, usernames []string) (Preview, error) {
	if !validGroupUsernames(usernames) {
		return Preview{}, ErrInvalid
	}
	if contaminated(s.credentials, usernames...) {
		return Preview{}, ErrCredential
	}
	participants := make([]string, 0, len(usernames))
	seenIDs := make(map[string]struct{}, len(usernames))
	for _, username := range usernames {
		user, err := s.users.ByUsernameFresh(ctx, username)
		if err != nil {
			return Preview{}, targetReadError(err)
		}
		if !validResolvedUser(user) || !strings.EqualFold(user.Username, username) || user.ID == currentUserID {
			return Preview{}, ErrTarget
		}
		if contaminated(s.credentials, user.ID, user.Username) {
			return Preview{}, ErrCredential
		}
		if _, exists := seenIDs[user.ID]; exists {
			return Preview{}, ErrTarget
		}
		seenIDs[user.ID] = struct{}{}
		participants = append(participants, user.ID)
	}
	sort.Strings(participants)
	return s.unresolvedConversationPreview(currentUserID, "group", participants)
}

func validGroupUsernames(usernames []string) bool {
	if len(usernames) < 2 || len(usernames) > 100 {
		return false
	}
	seen := make(map[string]struct{}, len(usernames))
	for _, username := range usernames {
		key := strings.ToLower(username)
		if !validSelectorValue(username) {
			return false
		}
		if _, exists := seen[key]; exists {
			return false
		}
		seen[key] = struct{}{}
	}
	return true
}

func (s *Service) unresolvedConversationPreview(currentUserID, channelType string, participantIDs []string) (Preview, error) {
	preview := Preview{ServerURL: s.serverURL, ServerID: s.serverID, UserID: currentUserID, Destination: Destination{
		Kind: "conversation", ChannelType: channelType, ParticipantIDs: append([]string(nil), participantIDs...),
	}, Plan: Plan{Steps: []PlanStep{{Ordinal: 1, Type: "resolve_conversation", Condition: "if_missing"}}}}
	destination, plan, err := marshalSemantics(preview)
	if err != nil {
		return Preview{}, ErrInvalid
	}
	if contaminated(s.credentials, preview.ServerURL, preview.ServerID, preview.UserID, string(destination), string(plan)) {
		return Preview{}, ErrCredential
	}
	return preview, nil
}
