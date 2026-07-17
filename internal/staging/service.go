// Package staging resolves and validates mutation targets before admitting a
// canonical plan to the stage store.
package staging

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"

	"github.com/ardasevinc/mattermost-cli/internal/mattermost"
	"github.com/ardasevinc/mattermost-cli/internal/messageinput"
	"github.com/ardasevinc/mattermost-cli/internal/serverurl"
	"github.com/ardasevinc/mattermost-cli/internal/stageinput"
	"github.com/ardasevinc/mattermost-cli/internal/stagestore"
)

var (
	ErrInvalid    = errors.New("staging: invalid request")
	ErrTarget     = errors.New("staging: target could not be resolved")
	ErrCredential = errors.New("staging: protected credential present")
	ErrInput      = errors.New("staging: message or attachment input rejected")
	ErrStore      = errors.New("staging: stage could not be persisted")
	ErrConflict   = errors.New("staging: request conflict")
	ErrNotFound   = errors.New("staging: stage not found")
)

type Service struct {
	serverURL, serverID string
	users               Users
	channels            Channels
	teams               Teams
	posts               Posts
	store               Store
	bind                AttachmentBinder
	credentials         [][]byte
}

func New(serverBaseURL, serverID string, credentials []string, users Users, channels Channels, teams Teams, posts Posts, store Store) (*Service, error) {
	normalized, err := serverurl.Normalize(serverBaseURL)
	if err != nil || nilDependency(users) || nilDependency(channels) || nilDependency(teams) || nilDependency(posts) || (serverID != "" && !validSelectorValue(serverID)) {
		return nil, ErrInvalid
	}
	protected := credentialBytes(credentials)
	if len(protected) > 64 {
		return nil, ErrInvalid
	}
	total := 0
	for _, credential := range protected {
		total += len(credential)
		if len(credential) > 4096 || total > 64<<10 {
			return nil, ErrInvalid
		}
	}
	if contaminated(protected, normalized+"/api/v4", serverID) {
		return nil, ErrCredential
	}
	return &Service{serverURL: normalized + "/api/v4", serverID: serverID, users: users, channels: channels, teams: teams, posts: posts, store: store, bind: stageinput.Bind, credentials: protected}, nil
}

// WithAttachmentBinder is intended for narrow tests which must prove dry-run
// and early failures perform no filesystem I/O.
func (s *Service) WithAttachmentBinder(bind AttachmentBinder) *Service {
	copy := *s
	copy.bind = bind
	return &copy
}

func (s *Service) DryRunCreatePost(ctx context.Context, in DryRunInput) (Preview, error) {
	return s.resolveConversation(ctx, in.Target)
}

func (s *Service) CreatePost(ctx context.Context, in CreatePostInput) (CreatePostResult, error) {
	if nilDependency(s.store) || s.bind == nil || in.Body == nil || !validRequestID(in.RequestID) {
		return CreatePostResult{}, ErrInvalid
	}
	if !validTargetSyntax(in.Target) {
		return CreatePostResult{}, ErrInvalid
	}
	callerFields := append([]string{in.RequestID}, targetStrings(in.Target)...)
	if contaminated(s.credentials, callerFields...) || callerAttachmentsContaminated(s.credentials, in.Attachments) {
		return CreatePostResult{}, ErrCredential
	}
	attachmentIntent, err := stageinput.Preflight(in.Attachments)
	if err != nil {
		return CreatePostResult{}, ErrInput
	}
	if attachmentIntentContaminated(s.credentials, attachmentIntent) {
		return CreatePostResult{}, ErrCredential
	}
	current, err := s.authenticate(ctx)
	if err != nil {
		return CreatePostResult{}, err
	}
	var record stagestore.CreateRecord
	var found bool
	if in.RequestID != "" {
		record, found, err = s.store.FindCreate(ctx, s.serverURL, current.ID, in.RequestID)
		if err != nil {
			if errors.Is(err, stagestore.ErrConflict) {
				return CreatePostResult{}, ErrConflict
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return CreatePostResult{}, err
			}
			return CreatePostResult{}, ErrStore
		}
	}
	if found {
		if record.Stage.Operation != stagestore.CreatePost {
			return CreatePostResult{}, ErrConflict
		}
		body, readErr := messageinput.Read(in.Body)
		if readErr != nil {
			return CreatePostResult{}, ErrInput
		}
		if containsCredential(s.credentials, body) {
			return CreatePostResult{}, ErrCredential
		}
		digest := intentDigest(stagestore.CreatePost, conversationCallerIntent(in.Target), body, "", attachmentIntent)
		return replayResult(record, digest, stagestore.CreatePost, s.serverURL, current.ID)
	}
	preview, err := s.resolveConversationFor(ctx, in.Target, current)
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
	destination, plan, err := marshalSemantics(preview)
	if err != nil {
		return CreatePostResult{}, ErrInvalid
	}
	if contaminated(s.credentials, in.RequestID, preview.ServerURL, preview.ServerID, preview.UserID, string(destination), string(plan)) || containsCredential(s.credentials, body) || attachmentsContaminated(s.credentials, attachments) {
		return CreatePostResult{}, ErrCredential
	}
	stored, err := s.store.Create(ctx, stagestore.CreateInput{RequestID: in.RequestID, Operation: stagestore.CreatePost,
		RequestDigest: intentDigest(stagestore.CreatePost, conversationCallerIntent(in.Target), body, "", attachmentIntent),
		ServerURL:     preview.ServerURL, ServerID: preview.ServerID, UserID: preview.UserID,
		Content: stagestore.RevisionContent{Body: body, Destination: destination, Plan: plan, Attachments: attachments}})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return CreatePostResult{}, err
		}
		if errors.Is(err, stagestore.ErrConflict) {
			return CreatePostResult{}, ErrConflict
		}
		return CreatePostResult{}, ErrStore
	}
	return replayResult(stored, stored.RequestDigest, stagestore.CreatePost, preview.ServerURL, preview.UserID)
}

func (s *Service) authenticate(ctx context.Context) (mattermost.User, error) {
	if ctx == nil {
		return mattermost.User{}, ErrInvalid
	}
	current, err := s.users.Current(ctx)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return mattermost.User{}, err
	}
	if err != nil || !validResolvedUser(current) {
		return mattermost.User{}, ErrTarget
	}
	if contaminated(s.credentials, current.ID, current.Username) {
		return mattermost.User{}, ErrCredential
	}
	return current, nil
}

func replayResult(record stagestore.CreateRecord, digest [32]byte, operation stagestore.Operation, serverURL, userID string) (CreatePostResult, error) {
	if record.RequestDigest != digest || record.Stage.Operation != operation {
		return CreatePostResult{}, ErrConflict
	}
	if record.Stage.ServerURL != serverURL || record.Stage.UserID != userID {
		return CreatePostResult{}, ErrStore
	}
	var destination Destination
	var plan Plan
	dd := json.NewDecoder(bytes.NewReader(record.Destination))
	dd.DisallowUnknownFields()
	pd := json.NewDecoder(bytes.NewReader(record.Plan))
	pd.DisallowUnknownFields()
	if dd.Decode(&destination) != nil || dd.Decode(new(any)) != io.EOF || pd.Decode(&plan) != nil || pd.Decode(new(any)) != io.EOF {
		return CreatePostResult{}, ErrStore
	}
	preview := Preview{ServerURL: record.Stage.ServerURL, ServerID: record.Stage.ServerID, UserID: record.Stage.UserID, Destination: destination, Plan: plan}
	destinationRaw, planRaw, err := marshalSemantics(preview)
	if err != nil || !bytes.Equal(destinationRaw, record.Destination) || !bytes.Equal(planRaw, record.Plan) {
		return CreatePostResult{}, ErrStore
	}
	return CreatePostResult{preview, record.MutationResult}, nil
}

func nilDependency(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	return (v.Kind() == reflect.Chan || v.Kind() == reflect.Func || v.Kind() == reflect.Interface || v.Kind() == reflect.Map || v.Kind() == reflect.Pointer || v.Kind() == reflect.Slice) && v.IsNil()
}

func attachmentPlan(count int) Plan {
	steps := make([]PlanStep, 0, count+1)
	for i := range count {
		steps = append(steps, PlanStep{i + 1, "upload_attachment", "always"})
	}
	steps = append(steps, PlanStep{count + 1, "create_post", "always"})
	return Plan{steps}
}

func compositionPlan(operation stagestore.Operation, attachmentCount int) Plan {
	if operation == stagestore.CreatePost || operation == stagestore.Reply {
		return attachmentPlan(attachmentCount)
	}
	return postPlan(operation)
}

func marshalSemantics(preview Preview) ([]byte, []byte, error) {
	destination, err := json.Marshal(preview.Destination)
	if err != nil {
		return nil, nil, err
	}
	plan, err := json.Marshal(preview.Plan)
	return destination, plan, err
}
