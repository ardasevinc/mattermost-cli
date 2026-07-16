// Package staging resolves and validates mutation targets before admitting a
// canonical plan to the stage store.
package staging

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

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
)

type ConversationType uint8

const (
	Direct ConversationType = iota + 1
	Group
	Channel
)

type SelectorType uint8

const (
	ByUsername SelectorType = iota + 1
	ByID
	ByName
)

// Target is deliberately syntactic. Resolved IDs and serialized plans are not
// caller-settable.
type Target struct {
	Conversation ConversationType
	Selector     SelectorType
	Value        string
	Team         *TeamSelector
}

type TeamSelector struct {
	By    SelectorType // ByID or ByName
	Value string
}

type Attachment = stageinput.Attachment

type CreatePostInput struct {
	RequestID   string
	Target      Target
	Body        io.Reader
	Attachments []Attachment
}

type DryRunInput struct{ Target Target }

type Destination struct {
	Kind           string   `json:"kind"`
	ChannelID      string   `json:"channelId"`
	ChannelType    string   `json:"channelType"`
	TeamID         *string  `json:"teamId"`
	PostID         *string  `json:"postId"`
	RootPostID     *string  `json:"rootPostId"`
	ParticipantIDs []string `json:"participantIds"`
	Emoji          *string  `json:"emoji"`
}

type Plan struct {
	Steps []PlanStep `json:"steps"`
}
type PlanStep struct {
	Ordinal   int    `json:"ordinal"`
	Type      string `json:"type"`
	Condition string `json:"condition"`
}

type Preview struct {
	ServerURL   string
	ServerID    string
	UserID      string
	Destination Destination
	Plan        Plan
}

type CreatePostResult struct {
	Preview Preview
	Stored  stagestore.MutationResult
}

type Users interface {
	Current(context.Context) (mattermost.User, error)
	ByUsernameFresh(context.Context, string) (mattermost.User, error)
}
type Channels interface {
	ExistingDirect(context.Context, string, string) (mattermost.Channel, bool, error)
	ByID(context.Context, string) (mattermost.Channel, error)
	ByName(context.Context, string, string) (mattermost.Channel, error)
	Member(context.Context, string, string) (mattermost.ChannelMember, error)
}
type Teams interface {
	List(context.Context, string) (mattermost.TeamMembership, error)
}
type Store interface {
	Create(context.Context, stagestore.CreateInput) (stagestore.MutationResult, error)
}
type AttachmentBinder func(context.Context, []stageinput.Attachment, [][]byte) ([]stagestore.Attachment, error)

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
	if err != nil || users == nil || channels == nil || teams == nil || (serverID != "" && !validIdentity(serverID)) {
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
	return s.resolve(ctx, in.Target)
}

func (s *Service) CreatePost(ctx context.Context, in CreatePostInput) (CreatePostResult, error) {
	if s.store == nil || s.bind == nil || in.Body == nil {
		return CreatePostResult{}, ErrInvalid
	}
	if in.RequestID == "" || !validRequestID(in.RequestID) {
		return CreatePostResult{}, ErrInvalid
	}
	callerFields := append([]string{in.RequestID}, targetStrings(in.Target)...)
	if contaminated(s.credentials, callerFields...) {
		return CreatePostResult{}, ErrCredential
	}
	preview, err := s.resolve(ctx, in.Target)
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

func (s *Service) resolve(ctx context.Context, target Target) (Preview, error) {
	if ctx == nil || !validTargetSyntax(target) {
		return Preview{}, ErrInvalid
	}
	if contaminated(s.credentials, targetStrings(target)...) {
		return Preview{}, ErrCredential
	}
	current, err := s.users.Current(ctx)
	if err != nil || !validResolvedUser(current) {
		return Preview{}, ErrTarget
	}
	if contaminated(s.credentials, current.ID, current.Username) {
		return Preview{}, ErrCredential
	}
	channel, participants, err := s.resolveChannel(ctx, current, target)
	if err != nil {
		return Preview{}, err
	}
	var teamID *string
	if channel.Type == "O" || channel.Type == "P" {
		value := channel.TeamID
		teamID = &value
	}
	preview := Preview{s.serverURL, s.serverID, current.ID, Destination{"conversation", channel.ID, channelType(channel.Type), teamID, nil, nil, participants, nil}, createPostPlan()}
	destination, plan, err := marshalSemantics(preview)
	if err != nil {
		return Preview{}, ErrInvalid
	}
	fields := append(targetStrings(target), preview.ServerURL, preview.ServerID, preview.UserID, string(destination), string(plan))
	if contaminated(s.credentials, fields...) {
		return Preview{}, ErrCredential
	}
	return preview, nil
}

func (s *Service) resolveChannel(ctx context.Context, current mattermost.User, target Target) (mattermost.Channel, []string, error) {
	switch target.Conversation {
	case Direct:
		if target.Selector != ByUsername || target.Team != nil {
			return mattermost.Channel{}, nil, ErrInvalid
		}
		peer, err := s.users.ByUsernameFresh(ctx, target.Value)
		if err != nil || !validResolvedUser(peer) || !strings.EqualFold(peer.Username, target.Value) || peer.ID == current.ID {
			return mattermost.Channel{}, nil, ErrTarget
		}
		if contaminated(s.credentials, peer.ID, peer.Username) {
			return mattermost.Channel{}, nil, ErrCredential
		}
		channel, found, err := s.channels.ExistingDirect(ctx, current.ID, peer.ID)
		if err != nil || !found || !validResolvedChannel(channel) || channel.Type != "D" {
			return mattermost.Channel{}, nil, ErrTarget
		}
		if contaminated(s.credentials, channel.ID, channel.Name) {
			return mattermost.Channel{}, nil, ErrCredential
		}
		return channel, []string{peer.ID}, nil
	case Group:
		if target.Selector != ByID || target.Team != nil {
			return mattermost.Channel{}, nil, ErrInvalid
		}
		channel, err := s.channels.ByID(ctx, target.Value)
		if err != nil || !validResolvedChannel(channel) || channel.Type != "G" {
			return mattermost.Channel{}, nil, ErrTarget
		}
		if contaminated(s.credentials, channel.ID, channel.Name) {
			return mattermost.Channel{}, nil, ErrCredential
		}
		if _, err = s.channels.Member(ctx, channel.ID, current.ID); err != nil {
			return mattermost.Channel{}, nil, ErrTarget
		}
		return channel, []string{}, nil
	case Channel:
		var channel mattermost.Channel
		var err error
		if target.Selector == ByID && target.Team == nil {
			channel, err = s.channels.ByID(ctx, target.Value)
		} else if target.Selector == ByName && target.Team != nil {
			team, teamErr := s.resolveTeam(ctx, current.ID, *target.Team)
			if teamErr != nil {
				return mattermost.Channel{}, nil, teamErr
			}
			channel, err = s.channels.ByName(ctx, team.ID, target.Value)
		} else {
			return mattermost.Channel{}, nil, ErrInvalid
		}
		if err != nil || !validResolvedChannel(channel) || (channel.Type != "O" && channel.Type != "P") {
			return mattermost.Channel{}, nil, ErrTarget
		}
		if contaminated(s.credentials, channel.ID, channel.Name, channel.TeamID) {
			return mattermost.Channel{}, nil, ErrCredential
		}
		if _, err = s.channels.Member(ctx, channel.ID, current.ID); err != nil {
			return mattermost.Channel{}, nil, ErrTarget
		}
		return channel, []string{}, nil
	default:
		return mattermost.Channel{}, nil, ErrInvalid
	}
}

func (s *Service) resolveTeam(ctx context.Context, userID string, selector TeamSelector) (mattermost.Team, error) {
	if (selector.By != ByID && selector.By != ByName) || !validSelectorValue(selector.Value) {
		return mattermost.Team{}, ErrInvalid
	}
	membership, err := s.teams.List(ctx, userID)
	if err != nil {
		return mattermost.Team{}, ErrTarget
	}
	var match mattermost.Team
	count := 0
	for _, team := range membership.Items() {
		if !validResolvedTeam(team) {
			return mattermost.Team{}, ErrTarget
		}
		matched := selector.By == ByID && team.ID == selector.Value
		if selector.By == ByName {
			matched = team.Name == selector.Value || team.DisplayName == selector.Value
		}
		if matched {
			match, count = team, count+1
		}
	}
	if count != 1 {
		return mattermost.Team{}, ErrTarget
	}
	if contaminated(s.credentials, match.ID, match.Name, match.DisplayName) {
		return mattermost.Team{}, ErrCredential
	}
	return match, nil
}

func channelType(value string) string {
	return map[string]string{"D": "dm", "G": "group", "O": "public", "P": "private"}[value]
}
func createPostPlan() Plan {
	return Plan{[]PlanStep{{1, "create_post", "always"}}}
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
func targetStrings(t Target) []string {
	v := []string{t.Value}
	if t.Team != nil {
		v = append(v, t.Team.Value)
	}
	return v
}
func credentialBytes(values []string) [][]byte {
	out := make([][]byte, 0, len(values))
	for _, v := range values {
		if v != "" {
			out = append(out, []byte(v))
		}
	}
	return out
}
func cloneCredentials(values [][]byte) [][]byte {
	out := make([][]byte, len(values))
	for i := range values {
		out[i] = bytes.Clone(values[i])
	}
	return out
}
func containsCredential(credentials [][]byte, value []byte) bool {
	for _, c := range credentials {
		if bytes.Contains(value, c) {
			return true
		}
	}
	return false
}
func contaminated(credentials [][]byte, values ...string) bool {
	for _, v := range values {
		if containsCredential(credentials, []byte(v)) {
			return true
		}
	}
	return false
}
func attachmentsContaminated(credentials [][]byte, values []stagestore.Attachment) bool {
	for _, v := range values {
		if contaminated(credentials, v.SuppliedPath, v.CanonicalPath, v.RemoteFilename, v.MediaType) {
			return true
		}
	}
	return false
}

func validSelectorValue(value string) bool {
	if value == "" || len(value) > 256 || !utf8.ValidString(value) || value != strings.TrimSpace(value) {
		return false
	}
	for _, r := range value {
		if unsafeIdentityRune(r) || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

func validIdentity(value string) bool {
	return validSelectorValue(value)
}

func validOptionalText(value string) bool {
	return value == "" || validSafeText(value, 256)
}

func validResolvedUser(user mattermost.User) bool {
	return validIdentity(user.ID) && validSelectorValue(user.Username)
}

func validResolvedChannel(channel mattermost.Channel) bool {
	if !validIdentity(channel.ID) || !validSelectorValue(channel.Name) || !validOptionalText(channel.DisplayName) {
		return false
	}
	switch channel.Type {
	case "D", "G":
		return channel.TeamID == ""
	case "O", "P":
		return validIdentity(channel.TeamID)
	default:
		return false
	}
}

func validResolvedTeam(team mattermost.Team) bool {
	return validIdentity(team.ID) && validSelectorValue(team.Name) && validOptionalText(team.DisplayName) && (team.Type == "O" || team.Type == "I")
}

func validBoundAttachments(values []stagestore.Attachment) bool {
	if len(values) > 100 {
		return false
	}
	for _, value := range values {
		if !validBoundText(value.SuppliedPath, 4096) || !validBoundText(value.CanonicalPath, 4096) ||
			!validBoundText(value.RemoteFilename, 255) || (value.MediaType != "" && !validBoundText(value.MediaType, 255)) ||
			value.ByteLength < 0 || value.ContentDigest == ([32]byte{}) {
			return false
		}
	}
	return true
}

func validBoundText(value string, maximum int) bool {
	if value == "" || len(value) > maximum || !utf8.ValidString(value) || value != strings.TrimSpace(value) {
		return false
	}
	for _, r := range value {
		if unsafeRune(r) {
			return false
		}
	}
	return true
}

func validSafeText(value string, maximum int) bool {
	return value != "" && len(value) <= maximum && utf8.ValidString(value) && value == strings.TrimSpace(value) && !strings.ContainsFunc(value, unsafeRune)
}

func unsafeRune(r rune) bool {
	return unicode.IsControl(r) || r == '\u061c' || r == '\u200e' || r == '\u200f' ||
		r >= '\u202a' && r <= '\u202e' || r >= '\u2066' && r <= '\u2069'
}

func unsafeIdentityRune(r rune) bool {
	return unsafeRune(r) || r >= '\u200b' && r <= '\u200d' || r == '\ufeff'
}

func validTargetSyntax(target Target) bool {
	if !validSelectorValue(target.Value) || target.Selector == ByName && strings.HasPrefix(target.Value, "#") {
		return false
	}
	switch target.Conversation {
	case Direct:
		return target.Selector == ByUsername && target.Team == nil
	case Group:
		return target.Selector == ByID && target.Team == nil
	case Channel:
		if target.Selector == ByID {
			return target.Team == nil
		}
		return target.Selector == ByName && target.Team != nil &&
			(target.Team.By == ByID || target.Team.By == ByName) && validSelectorValue(target.Team.Value)
	default:
		return false
	}
}

func validRequestID(value string) bool {
	if value == "" {
		return true
	}
	if len(value) > 256 || !requestCharacter(value[0], true) {
		return false
	}
	for i := 1; i < len(value); i++ {
		if !requestCharacter(value[i], false) {
			return false
		}
	}
	return true
}

func requestCharacter(value byte, first bool) bool {
	if value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z' || value >= '0' && value <= '9' {
		return true
	}
	return !first && strings.ContainsRune("._~:-", rune(value))
}
