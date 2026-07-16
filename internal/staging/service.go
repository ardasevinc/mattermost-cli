// Package staging resolves and validates mutation targets before admitting a
// canonical plan to the stage store.
package staging

import (
	"context"
	"encoding/json"
	"errors"

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
)

type Service struct {
	serverURL, serverID string
	users               Users
	channels            Channels
	teams               Teams
	store               Store
	bind                AttachmentBinder
	credentials         [][]byte
}

func New(serverBaseURL, serverID string, credentials []string, users Users, channels Channels, teams Teams, store Store) (*Service, error) {
	normalized, err := serverurl.Normalize(serverBaseURL)
	if err != nil || users == nil || channels == nil || teams == nil || (serverID != "" && !validSelectorValue(serverID)) {
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
	return &Service{serverURL: normalized + "/api/v4", serverID: serverID, users: users, channels: channels, teams: teams, store: store, bind: stageinput.Bind, credentials: protected}, nil
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
	if s.store == nil || s.bind == nil || in.Body == nil || !validRequestID(in.RequestID) {
		return CreatePostResult{}, ErrInvalid
	}
	callerFields := append([]string{in.RequestID}, targetStrings(in.Target)...)
	if contaminated(s.credentials, callerFields...) {
		return CreatePostResult{}, ErrCredential
	}
	preview, err := s.resolveConversation(ctx, in.Target)
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
		ServerURL: preview.ServerURL, ServerID: preview.ServerID, UserID: preview.UserID,
		Content: stagestore.RevisionContent{Body: body, Destination: destination, Plan: plan, Attachments: attachments}})
	if err != nil {
		if errors.Is(err, stagestore.ErrConflict) {
			return CreatePostResult{}, ErrConflict
		}
		return CreatePostResult{}, ErrStore
	}
	return CreatePostResult{Preview: preview, Stored: stored}, nil
}

func attachmentPlan(count int) Plan {
	steps := make([]PlanStep, 0, count+1)
	for i := range count {
		steps = append(steps, PlanStep{i + 1, "upload_attachment", "always"})
	}
	steps = append(steps, PlanStep{count + 1, "create_post", "always"})
	return Plan{steps}
}

func marshalSemantics(preview Preview) ([]byte, []byte, error) {
	destination, err := json.Marshal(preview.Destination)
	if err != nil {
		return nil, nil, err
	}
	plan, err := json.Marshal(preview.Plan)
	return destination, plan, err
}
