package staging

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/ardasevinc/mattermost-cli/v2/internal/mattermost"
	"github.com/ardasevinc/mattermost-cli/v2/internal/messageinput"
	"github.com/ardasevinc/mattermost-cli/v2/internal/stageinput"
	"github.com/ardasevinc/mattermost-cli/v2/internal/stagestore"
)

type postOperation struct {
	operation stagestore.Operation
	postID    string
	emoji     string
}

func (s *Service) DryRunReply(ctx context.Context, in PostDryRunInput) (Preview, error) {
	return s.resolvePost(ctx, postOperation{operation: stagestore.Reply, postID: in.PostID})
}

func (s *Service) Reply(ctx context.Context, in ReplyInput) (CreatePostResult, error) {
	if nilDependency(s.store) || s.bind == nil || in.Body == nil || !validRequestID(in.RequestID) {
		return CreatePostResult{}, ErrInvalid
	}
	if contaminated(s.credentials, in.RequestID, in.PostID) || callerAttachmentsContaminated(s.credentials, in.Attachments) {
		return CreatePostResult{}, ErrCredential
	}
	attachmentIntent, err := stageinput.Preflight(in.Attachments)
	if err != nil {
		return CreatePostResult{}, ErrInput
	}
	if attachmentIntentContaminated(s.credentials, attachmentIntent) {
		return CreatePostResult{}, ErrCredential
	}
	op := postOperation{operation: stagestore.Reply, postID: in.PostID}
	if err := s.validatePostOperation(ctx, op); err != nil {
		return CreatePostResult{}, err
	}
	current, err := s.authenticate(ctx)
	if err != nil {
		return CreatePostResult{}, err
	}
	record, found, err := s.findCreate(ctx, current.ID, in.RequestID)
	if err != nil {
		return CreatePostResult{}, err
	}
	if found {
		if record.Stage.Operation != stagestore.Reply {
			return CreatePostResult{}, ErrConflict
		}
		body, readErr := messageinput.Read(in.Body)
		if readErr != nil {
			return CreatePostResult{}, ErrInput
		}
		if containsCredential(s.credentials, body) {
			return CreatePostResult{}, ErrCredential
		}
		return replayResult(record, intentDigest(stagestore.Reply, postIntent{in.PostID}, body, "", attachmentIntent), stagestore.Reply, s.serverURL, current.ID)
	}
	preview, err := s.resolvePostFor(ctx, op, current)
	if err != nil {
		return CreatePostResult{}, err
	}
	body, err := messageinput.Read(in.Body)
	if err != nil {
		return CreatePostResult{}, ErrInput
	}
	if containsCredential(s.credentials, body) {
		return CreatePostResult{}, ErrCredential
	}
	attachments, err := s.bind(ctx, in.Attachments, cloneCredentials(s.credentials))
	if err != nil {
		if errors.Is(err, stageinput.ErrCredential) {
			return CreatePostResult{}, ErrCredential
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return CreatePostResult{}, err
		}
		return CreatePostResult{}, ErrInput
	}
	if !validBoundAttachments(attachments) {
		return CreatePostResult{}, ErrInput
	}
	preview.Plan = attachmentPlan(len(attachments))
	return s.persistPost(ctx, in.RequestID, intentDigest(stagestore.Reply, postIntent{in.PostID}, body, "", attachmentIntent), stagestore.Reply, preview, body, attachments)
}

func (s *Service) DryRunEditPost(ctx context.Context, in PostDryRunInput) (Preview, error) {
	return s.resolvePost(ctx, postOperation{operation: stagestore.EditPost, postID: in.PostID})
}

func (s *Service) EditPost(ctx context.Context, in EditPostInput) (CreatePostResult, error) {
	if nilDependency(s.store) || in.Body == nil || !validRequestID(in.RequestID) {
		return CreatePostResult{}, ErrInvalid
	}
	if contaminated(s.credentials, in.RequestID, in.PostID) {
		return CreatePostResult{}, ErrCredential
	}
	op := postOperation{operation: stagestore.EditPost, postID: in.PostID}
	if err := s.validatePostOperation(ctx, op); err != nil {
		return CreatePostResult{}, err
	}
	current, err := s.authenticate(ctx)
	if err != nil {
		return CreatePostResult{}, err
	}
	record, found, err := s.findCreate(ctx, current.ID, in.RequestID)
	if err != nil {
		return CreatePostResult{}, err
	}
	if found {
		if record.Stage.Operation != stagestore.EditPost {
			return CreatePostResult{}, ErrConflict
		}
		body, readErr := messageinput.Read(in.Body)
		if readErr != nil {
			return CreatePostResult{}, ErrInput
		}
		if containsCredential(s.credentials, body) {
			return CreatePostResult{}, ErrCredential
		}
		return replayResult(record, intentDigest(stagestore.EditPost, postIntent{in.PostID}, body, "", []stageinput.MetadataIntent{}), stagestore.EditPost, s.serverURL, current.ID)
	}
	preview, err := s.resolvePostFor(ctx, op, current)
	if err != nil {
		return CreatePostResult{}, err
	}
	body, err := messageinput.Read(in.Body)
	if err != nil {
		return CreatePostResult{}, ErrInput
	}
	if containsCredential(s.credentials, body) {
		return CreatePostResult{}, ErrCredential
	}
	return s.persistPost(ctx, in.RequestID, intentDigest(stagestore.EditPost, postIntent{in.PostID}, body, "", []stageinput.MetadataIntent{}), stagestore.EditPost, preview, body, nil)
}

func (s *Service) DryRunDeletePost(ctx context.Context, in PostDryRunInput) (Preview, error) {
	return s.resolvePost(ctx, postOperation{operation: stagestore.DeletePost, postID: in.PostID})
}

func (s *Service) DeletePost(ctx context.Context, in DeletePostInput) (CreatePostResult, error) {
	return s.persistContentless(ctx, in.RequestID, postOperation{operation: stagestore.DeletePost, postID: in.PostID})
}

func (s *Service) DryRunReact(ctx context.Context, in ReactionDryRunInput) (Preview, error) {
	if contaminated(s.credentials, in.Emoji) {
		return Preview{}, ErrCredential
	}
	return s.resolvePost(ctx, postOperation{operation: stagestore.React, postID: in.PostID, emoji: strings.ToLower(in.Emoji)})
}

func (s *Service) React(ctx context.Context, in ReactionInput) (CreatePostResult, error) {
	if contaminated(s.credentials, in.Emoji) {
		return CreatePostResult{}, ErrCredential
	}
	return s.persistContentless(ctx, in.RequestID, postOperation{operation: stagestore.React, postID: in.PostID, emoji: strings.ToLower(in.Emoji)})
}

func (s *Service) DryRunUnreact(ctx context.Context, in ReactionDryRunInput) (Preview, error) {
	if contaminated(s.credentials, in.Emoji) {
		return Preview{}, ErrCredential
	}
	return s.resolvePost(ctx, postOperation{operation: stagestore.Unreact, postID: in.PostID, emoji: strings.ToLower(in.Emoji)})
}

func (s *Service) Unreact(ctx context.Context, in ReactionInput) (CreatePostResult, error) {
	if contaminated(s.credentials, in.Emoji) {
		return CreatePostResult{}, ErrCredential
	}
	return s.persistContentless(ctx, in.RequestID, postOperation{operation: stagestore.Unreact, postID: in.PostID, emoji: strings.ToLower(in.Emoji)})
}

func (s *Service) persistContentless(ctx context.Context, requestID string, op postOperation) (CreatePostResult, error) {
	if nilDependency(s.store) || !validRequestID(requestID) {
		return CreatePostResult{}, ErrInvalid
	}
	if contaminated(s.credentials, requestID, op.postID, op.emoji) {
		return CreatePostResult{}, ErrCredential
	}
	if err := s.validatePostOperation(ctx, op); err != nil {
		return CreatePostResult{}, err
	}
	current, err := s.authenticate(ctx)
	if err != nil {
		return CreatePostResult{}, err
	}
	digest := intentDigest(op.operation, postIntent{op.postID}, nil, op.emoji, []stageinput.MetadataIntent{})
	record, found, err := s.findCreate(ctx, current.ID, requestID)
	if err != nil {
		return CreatePostResult{}, err
	}
	if found {
		return replayResult(record, digest, op.operation, s.serverURL, current.ID)
	}
	preview, err := s.resolvePostFor(ctx, op, current)
	if err != nil {
		return CreatePostResult{}, err
	}
	return s.persistPost(ctx, requestID, digest, op.operation, preview, nil, nil)
}

func (s *Service) resolvePost(ctx context.Context, op postOperation) (Preview, error) {
	if err := s.validatePostOperation(ctx, op); err != nil {
		return Preview{}, err
	}
	current, err := s.authenticate(ctx)
	if err != nil {
		return Preview{}, err
	}
	return s.resolvePostFor(ctx, op, current)
}

func (s *Service) validatePostOperation(ctx context.Context, op postOperation) error {
	allowed := op.operation == stagestore.Reply || op.operation == stagestore.EditPost || op.operation == stagestore.DeletePost ||
		op.operation == stagestore.React || op.operation == stagestore.Unreact
	if ctx == nil || !validPostID(op.postID) ||
		!allowed ||
		((op.operation == stagestore.React || op.operation == stagestore.Unreact) != (op.emoji != "")) ||
		(op.emoji != "" && !validEmoji(op.emoji)) || contaminated(s.credentials, op.postID, op.emoji) {
		if contaminated(s.credentials, op.postID, op.emoji) {
			return ErrCredential
		}
		return ErrInvalid
	}
	return nil
}

func (s *Service) resolvePostFor(ctx context.Context, op postOperation, current mattermost.User) (Preview, error) {
	post, err := s.posts.ByID(ctx, op.postID)
	if err != nil {
		return Preview{}, targetReadError(err)
	}
	if post.ID != op.postID || !validResolvedPost(post) {
		return Preview{}, ErrTarget
	}
	postIdentity := []string{post.ID, post.ChannelID, post.UserID, post.RootID}
	postIdentity = append(postIdentity, post.FileIDs...)
	if contaminated(s.credentials, postIdentity...) {
		return Preview{}, ErrCredential
	}
	if (op.operation == stagestore.EditPost || op.operation == stagestore.DeletePost) && (post.UserID != current.ID || post.Type != "") {
		return Preview{}, ErrTarget
	}
	canonicalRoot := post.RootID
	if op.operation == stagestore.Reply && post.RootID == "" {
		canonicalRoot = post.ID
	}
	if op.operation == stagestore.Reply && post.RootID != "" {
		root, rootErr := s.posts.ByID(ctx, post.RootID)
		if rootErr != nil {
			return Preview{}, targetReadError(rootErr)
		}
		if root.ID != post.RootID || root.RootID != "" || root.ChannelID != post.ChannelID || !validResolvedPost(root) {
			return Preview{}, ErrTarget
		}
		if contaminated(s.credentials, root.ID, root.ChannelID, root.UserID, root.RootID) {
			return Preview{}, ErrCredential
		}
		canonicalRoot = root.ID
	}
	channel, err := s.channels.ByID(ctx, post.ChannelID)
	if err != nil {
		return Preview{}, targetReadError(err)
	}
	if channel.ID != post.ChannelID || !validResolvedChannel(channel) {
		return Preview{}, ErrTarget
	}
	participants, ok := postParticipants(channel, current.ID)
	if !ok {
		return Preview{}, ErrTarget
	}
	if contaminated(s.credentials, channel.ID, channel.Name, channel.TeamID) || contaminated(s.credentials, participants...) {
		return Preview{}, ErrCredential
	}
	member, err := s.channels.Member(ctx, channel.ID, current.ID)
	if err != nil {
		return Preview{}, targetReadError(err)
	}
	if member.ChannelID != channel.ID || member.UserID != current.ID {
		return Preview{}, ErrTarget
	}
	var teamID *string
	if channel.Type == "O" || channel.Type == "P" {
		value := channel.TeamID
		teamID = &value
	}
	postID := post.ID
	destination := Destination{Kind: "post", ChannelID: channel.ID, ChannelType: channelType(channel.Type), TeamID: teamID,
		PostID: &postID, ParticipantIDs: participants}
	if canonicalRoot != "" {
		destination.RootPostID = &canonicalRoot
	}
	switch op.operation {
	case stagestore.Reply:
	case stagestore.EditPost, stagestore.DeletePost:
		destination.PostState = &PostState{AuthorUserID: post.UserID, UpdateAt: post.UpdateAt, ContentDigest: digestPost(post, s.credentials)}
	case stagestore.React, stagestore.Unreact:
		present, reactionErr := s.posts.ReactionState(ctx, post.ID, channel.ID, current.ID, op.emoji)
		if reactionErr != nil {
			return Preview{}, targetReadError(reactionErr)
		}
		destination.Kind, destination.Emoji, destination.ReactionPresent = "reaction", &op.emoji, &present
	}
	preview := Preview{ServerURL: s.serverURL, ServerID: s.serverID, UserID: current.ID, Destination: destination, Plan: postPlan(op.operation)}
	destinationJSON, planJSON, err := marshalSemantics(preview)
	if err != nil {
		return Preview{}, ErrInvalid
	}
	if contaminated(s.credentials, preview.ServerURL, preview.ServerID, preview.UserID, string(destinationJSON), string(planJSON)) {
		return Preview{}, ErrCredential
	}
	return preview, nil
}

func (s *Service) persistPost(ctx context.Context, requestID string, requestDigest [32]byte, operation stagestore.Operation, preview Preview, body []byte, attachments []stagestore.Attachment) (CreatePostResult, error) {
	destination, plan, err := marshalSemantics(preview)
	if err != nil {
		return CreatePostResult{}, ErrInvalid
	}
	if contaminated(s.credentials, requestID, string(destination), string(plan)) || containsCredential(s.credentials, body) || attachmentsContaminated(s.credentials, attachments) {
		return CreatePostResult{}, ErrCredential
	}
	stored, err := s.store.Create(ctx, stagestore.CreateInput{RequestID: requestID, RequestDigest: requestDigest, Operation: operation, ServerURL: preview.ServerURL,
		ServerID: preview.ServerID, UserID: preview.UserID, Content: stagestore.RevisionContent{Body: body, Destination: destination, Plan: plan, Attachments: attachments}})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return CreatePostResult{}, err
		}
		if errors.Is(err, stagestore.ErrConflict) {
			return CreatePostResult{}, ErrConflict
		}
		return CreatePostResult{}, ErrStore
	}
	return replayResult(stored, requestDigest, operation, preview.ServerURL, preview.UserID)
}

func (s *Service) findCreate(ctx context.Context, userID, requestID string) (stagestore.CreateRecord, bool, error) {
	if requestID == "" {
		return stagestore.CreateRecord{}, false, nil
	}
	record, found, err := s.store.FindCreate(ctx, s.serverURL, userID, requestID)
	if err == nil {
		return record, found, nil
	}
	if errors.Is(err, stagestore.ErrConflict) {
		return stagestore.CreateRecord{}, false, ErrConflict
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return stagestore.CreateRecord{}, false, err
	}
	return stagestore.CreateRecord{}, false, ErrStore
}

// PostContentDigest binds the exact mutable Mattermost post content while
// eliding active credentials so the digest cannot become an offline verifier.
func PostContentDigest(post mattermost.Post, credentials [][]byte) string {
	canonical := struct {
		Message any    `json:"message"`
		FileIDs []any  `json:"fileIds"`
		RootID  string `json:"rootId"`
		Type    string `json:"type"`
	}{credentialSafePostMessage(post.Message, credentials), credentialSafeStrings(post.FileIDs, credentials), post.RootID, post.Type}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(canonical)
	encoded := bytes.TrimSuffix(buffer.Bytes(), []byte{'\n'})
	encoded = restoreJSONLineSeparators(encoded)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func credentialSafeStrings(values []string, credentials [][]byte) []any {
	out := make([]any, len(values))
	for index, value := range values {
		out[index] = credentialSafePostMessage(value, credentials)
	}
	return out
}

func digestPost(post mattermost.Post, credentials [][]byte) string {
	return PostContentDigest(post, credentials)
}

func credentialSafePostMessage(message string, credentials [][]byte) any {
	source := []byte(message)
	protected := credentialsByLength(credentials)
	fragments := make([]string, 0)
	start := 0
	for cursor := 0; cursor < len(source); {
		matched := 0
		for _, credential := range protected {
			if len(credential) <= len(source)-cursor && bytes.Equal(source[cursor:cursor+len(credential)], credential) {
				matched = len(credential)
				break
			}
		}
		if matched == 0 {
			cursor++
			continue
		}
		fragments = append(fragments, string(source[start:cursor]))
		cursor += matched
		start = cursor
	}
	if len(fragments) == 0 {
		return message
	}
	fragments = append(fragments, string(source[start:]))
	return struct {
		Fragments []string `json:"credentialElidedFragments"`
	}{fragments}
}

func targetReadError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return ErrTarget
}

func postPlan(operation stagestore.Operation) Plan {
	typeName, condition := string(operation), "always"
	switch operation {
	case stagestore.Reply:
		typeName = "create_post"
	case stagestore.React:
		typeName, condition = "add_reaction", "if_missing"
	case stagestore.Unreact:
		typeName, condition = "remove_reaction", "if_missing"
	}
	return Plan{Steps: []PlanStep{{Ordinal: 1, Type: typeName, Condition: condition}}}
}

func postParticipants(channel mattermost.Channel, currentID string) ([]string, bool) {
	if channel.Type != "D" {
		return []string{}, true
	}
	parts := strings.Split(channel.Name, "__")
	if len(parts) != 2 || !validSelectorValue(parts[0]) || !validSelectorValue(parts[1]) {
		return nil, false
	}
	if parts[0] == currentID && parts[1] == currentID {
		return []string{currentID}, true
	}
	if parts[0] == currentID {
		return []string{parts[1]}, true
	}
	if parts[1] == currentID {
		return []string{parts[0]}, true
	}
	return nil, false
}

func validResolvedPost(post mattermost.Post) bool {
	authorOK := validPostID(post.UserID) || post.UserID == "" && strings.HasPrefix(post.Type, "system_")
	return validPostID(post.ID) && validPostID(post.ChannelID) && authorOK &&
		(post.RootID == "" || validPostID(post.RootID)) && post.UpdateAt > 0 && post.UpdateAt <= 8_640_000_000_000_000 && post.FileIDs != nil
}

func validPostID(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for i := range len(value) {
		c := value[i]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func restoreJSONLineSeparators(encoded []byte) []byte {
	result := make([]byte, 0, len(encoded))
	for i := 0; i < len(encoded); {
		if i+6 <= len(encoded) && (bytes.Equal(encoded[i:i+6], []byte(`\u2028`)) || bytes.Equal(encoded[i:i+6], []byte(`\u2029`))) {
			backslashes := 0
			for j := i - 1; j >= 0 && encoded[j] == '\\'; j-- {
				backslashes++
			}
			if backslashes%2 == 0 {
				if encoded[i+5] == '8' {
					result = append(result, "\u2028"...)
				} else {
					result = append(result, "\u2029"...)
				}
				i += 6
				continue
			}
		}
		result = append(result, encoded[i])
		i++
	}
	return result
}

func validEmoji(value string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	for i := range len(value) {
		c := value[i]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-' || c == '+') {
			return false
		}
	}
	return true
}

func callerAttachmentsContaminated(credentials [][]byte, values []Attachment) bool {
	for _, value := range values {
		if contaminated(credentials, value.Path, value.RemoteFilename, value.MediaType) {
			return true
		}
	}
	return false
}

func attachmentIntentContaminated(credentials [][]byte, values []stageinput.MetadataIntent) bool {
	for _, value := range values {
		mediaType := ""
		if value.MediaType != nil {
			mediaType = *value.MediaType
		}
		if contaminated(credentials, value.Path, value.RemoteFilename, mediaType) {
			return true
		}
	}
	return false
}
