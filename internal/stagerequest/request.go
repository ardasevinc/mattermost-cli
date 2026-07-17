// Package stagerequest decodes the public stage mutation request contracts and
// converts their syntactic values into staging-domain inputs.
package stagerequest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"reflect"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/ardasevinc/mattermost-cli/internal/schema"
	"github.com/ardasevinc/mattermost-cli/internal/stageinput"
	"github.com/ardasevinc/mattermost-cli/internal/stagestore"
	"github.com/ardasevinc/mattermost-cli/internal/staging"
)

const (
	StageSchema  = "mm/v2/stage-request"
	ReviseSchema = "mm/v2/stage-revise-request"
	CancelSchema = "mm/v2/stage-cancel-request"
	PruneSchema  = "mm/v2/stage-prune-request"
	ApplySchema  = "mm/v2/apply-request"
)

var ErrInvalid = errors.New("invalid stage request")

type Operation string

const (
	CreatePost     Operation = "create_post"
	Reply          Operation = "reply"
	EditPost       Operation = "edit_post"
	DeletePost     Operation = "delete_post"
	React          Operation = "react"
	Unreact        Operation = "unreact"
	ResolveDM      Operation = "resolve_dm"
	ResolveGroupDM Operation = "resolve_group_dm"
)

type Selector struct {
	By    string `json:"by"`
	Value string `json:"value"`
}

type TeamSelector struct {
	By    string `json:"by"`
	Value string `json:"value"`
}

// Target is the tagged union used by the public schema. Fields not belonging
// to its Kind remain empty; Team is nil for an explicit JSON null.
type Target struct {
	Kind             string        `json:"kind"`
	ConversationType string        `json:"conversationType,omitempty"`
	Selector         Selector      `json:"selector,omitempty"`
	Team             *TeamSelector `json:"team,omitempty"`
	PostID           string        `json:"postId,omitempty"`
	Username         string        `json:"username,omitempty"`
	Usernames        []string      `json:"usernames,omitempty"`
}

func (t Target) MarshalJSON() ([]byte, error) {
	switch t.Kind {
	case "conversation":
		return json.Marshal(struct {
			Kind             string        `json:"kind"`
			ConversationType string        `json:"conversationType"`
			Selector         Selector      `json:"selector"`
			Team             *TeamSelector `json:"team"`
		}{t.Kind, t.ConversationType, t.Selector, t.Team})
	case "post":
		return json.Marshal(struct {
			Kind   string `json:"kind"`
			PostID string `json:"postId"`
		}{t.Kind, t.PostID})
	case "user":
		return json.Marshal(struct {
			Kind     string `json:"kind"`
			Username string `json:"username"`
		}{t.Kind, t.Username})
	case "users":
		return json.Marshal(struct {
			Kind      string   `json:"kind"`
			Usernames []string `json:"usernames"`
		}{t.Kind, t.Usernames})
	default:
		return json.Marshal(struct {
			Kind string `json:"kind"`
		}{t.Kind})
	}
}

// Attachment preserves explicit JSON null as nil. Conversion maps nil to the
// staging layer's empty value, meaning derive filename or detect media type.
type Attachment struct {
	Path           string  `json:"path"`
	RemoteFilename *string `json:"remoteFilename"`
	MediaType      *string `json:"mediaType"`
}

type StageRequest struct {
	Schema      string       `json:"schema"`
	Persist     bool         `json:"persist"`
	RequestID   *string      `json:"requestId"`
	Operation   Operation    `json:"operation"`
	Target      Target       `json:"target"`
	Body        *string      `json:"body"`
	Emoji       *string      `json:"emoji"`
	Attachments []Attachment `json:"attachments"`
}

type ReviseRequest struct {
	Schema           string       `json:"schema"`
	RequestID        string       `json:"requestId"`
	StageID          string       `json:"stageId"`
	ExpectedRevision ExactInt64   `json:"expectedRevision"`
	ExpectedDigest   string       `json:"expectedDigest"`
	Revive           bool         `json:"revive"`
	Body             *string      `json:"body"`
	Attachments      []Attachment `json:"attachments"`
}

type CancelRequest struct {
	Schema           string     `json:"schema"`
	RequestID        string     `json:"requestId"`
	StageID          string     `json:"stageId"`
	ExpectedRevision ExactInt64 `json:"expectedRevision"`
	ExpectedDigest   string     `json:"expectedDigest"`
}

type PruneRequest struct {
	Schema           string     `json:"schema"`
	RequestID        string     `json:"requestId"`
	StageID          string     `json:"stageId"`
	ExpectedRevision ExactInt64 `json:"expectedRevision"`
	ExpectedDigest   string     `json:"expectedDigest"`
	AbandonRecovery  bool       `json:"abandonRecovery"`
}

type ApplyRequest struct {
	Schema         string     `json:"schema"`
	RequestID      string     `json:"requestId"`
	StageID        string     `json:"stageId"`
	Revision       ExactInt64 `json:"revision"`
	ExpectedDigest string     `json:"expectedDigest"`
	RecoveryMode   string     `json:"recoveryMode"`
}

type ExactInt64 int64

func (n *ExactInt64) UnmarshalJSON(data []byte) error {
	var number json.Number
	if err := json.Unmarshal(data, &number); err != nil {
		return err
	}
	rational, ok := new(big.Rat).SetString(number.String())
	if !ok || !rational.IsInt() || !rational.Num().IsInt64() {
		return fmt.Errorf("not an exact int64")
	}
	*n = ExactInt64(rational.Num().Int64())
	return nil
}

func (n ExactInt64) MarshalJSON() ([]byte, error) { return []byte(fmt.Sprint(int64(n))), nil }

type Decoder struct{ registry *schema.Registry }

var conversionRegistry struct {
	sync.Once
	value *schema.Registry
	err   error
}

func NewDecoder() (*Decoder, error) {
	r, err := schema.Load()
	if err != nil {
		return nil, err
	}
	return &Decoder{registry: r}, nil
}

func (d *Decoder) DecodeStage(input io.Reader) (StageRequest, error) {
	return decode[StageRequest](d, StageSchema, input)
}

func (d *Decoder) DecodeRevise(input io.Reader) (ReviseRequest, error) {
	return decode[ReviseRequest](d, ReviseSchema, input)
}

func (d *Decoder) DecodeCancel(input io.Reader) (CancelRequest, error) {
	return decode[CancelRequest](d, CancelSchema, input)
}

func (d *Decoder) DecodePrune(input io.Reader) (PruneRequest, error) {
	return decode[PruneRequest](d, PruneSchema, input)
}

func (d *Decoder) DecodeApply(input io.Reader) (ApplyRequest, error) {
	return decode[ApplyRequest](d, ApplySchema, input)
}

func decode[T any](d *Decoder, id string, input io.Reader) (T, error) {
	var zero T
	if d == nil || d.registry == nil || input == nil {
		return zero, ErrInvalid
	}
	raw, err := d.registry.ReadAndValidate(id, input)
	if err != nil {
		if schema.IsInputReadError(err) {
			return zero, err
		}
		return zero, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if err := rejectDuplicateNames(raw); err != nil {
		return zero, fmt.Errorf("%w: duplicate object member", ErrInvalid)
	}
	var value T
	if err := json.Unmarshal(raw, &value); err != nil {
		return zero, fmt.Errorf("%w: typed decode", ErrInvalid)
	}
	return value, nil
}

func validateConversion(id string, value any) error {
	if !validUTF8Strings(reflect.ValueOf(value)) {
		return ErrInvalid
	}
	conversionRegistry.Do(func() { conversionRegistry.value, conversionRegistry.err = schema.Load() })
	if conversionRegistry.err != nil {
		return conversionRegistry.err
	}
	raw, err := json.Marshal(value)
	if err != nil || conversionRegistry.value.Validate(id, bytes.NewReader(raw)) != nil {
		return ErrInvalid
	}
	return nil
}

func validUTF8Strings(value reflect.Value) bool {
	if !value.IsValid() {
		return true
	}
	if value.Kind() == reflect.Pointer {
		return value.IsNil() || validUTF8Strings(value.Elem())
	}
	switch value.Kind() {
	case reflect.String:
		return utf8.ValidString(value.String())
	case reflect.Struct:
		for i := 0; i < value.NumField(); i++ {
			if !validUTF8Strings(value.Field(i)) {
				return false
			}
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < value.Len(); i++ {
			if !validUTF8Strings(value.Index(i)) {
				return false
			}
		}
	}
	return true
}

func rejectDuplicateNames(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var walk func() error
	walk = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				nameToken, err := decoder.Token()
				if err != nil {
					return err
				}
				name, ok := nameToken.(string)
				if !ok {
					return ErrInvalid
				}
				if _, exists := seen[name]; exists {
					return ErrInvalid
				}
				seen[name] = struct{}{}
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		default:
			return ErrInvalid
		}
	}
	return walk()
}

func (t Target) StagingTarget() (staging.Target, error) {
	if t.Kind != "conversation" {
		return staging.Target{}, ErrInvalid
	}
	conversation := map[string]staging.ConversationType{"dm": staging.Direct, "group": staging.Group, "channel": staging.Channel}[t.ConversationType]
	selector := map[string]staging.SelectorType{"username": staging.ByUsername, "id": staging.ByID, "name": staging.ByName}[t.Selector.By]
	if conversation == 0 || selector == 0 || t.Selector.Value == "" {
		return staging.Target{}, ErrInvalid
	}
	out := staging.Target{Conversation: conversation, Selector: selector, Value: t.Selector.Value}
	if t.Team != nil {
		by := map[string]staging.SelectorType{"id": staging.ByID, "name": staging.ByName}[t.Team.By]
		if by == 0 || t.Team.Value == "" {
			return staging.Target{}, ErrInvalid
		}
		out.Team = &staging.TeamSelector{By: by, Value: t.Team.Value}
	}
	if conversation == staging.Direct && (selector != staging.ByUsername || out.Team != nil) ||
		conversation == staging.Group && (selector != staging.ByID || out.Team != nil) ||
		conversation == staging.Channel && selector == staging.ByID && out.Team != nil ||
		conversation == staging.Channel && selector == staging.ByName && out.Team == nil {
		return staging.Target{}, ErrInvalid
	}
	return out, nil
}

func (a Attachment) StagingAttachment() stageinput.Attachment {
	out := stageinput.Attachment{Path: a.Path}
	if a.RemoteFilename != nil {
		out.RemoteFilename = *a.RemoteFilename
	}
	if a.MediaType != nil {
		out.MediaType = *a.MediaType
	}
	return out
}

func StagingAttachments(values []Attachment) []stageinput.Attachment {
	if values == nil {
		return nil
	}
	out := make([]stageinput.Attachment, len(values))
	for i := range values {
		out[i] = values[i].StagingAttachment()
	}
	return out
}

func (r StageRequest) CreatePostInput() (staging.CreatePostInput, error) {
	if validateConversion(StageSchema, r) != nil || !r.Persist || r.Operation != CreatePost || r.RequestID == nil || r.Body == nil || r.Emoji != nil {
		return staging.CreatePostInput{}, ErrInvalid
	}
	target, err := r.Target.StagingTarget()
	if err != nil {
		return staging.CreatePostInput{}, err
	}
	return staging.CreatePostInput{RequestID: *r.RequestID, Target: target, Body: strings.NewReader(*r.Body), Attachments: StagingAttachments(r.Attachments)}, nil
}

func (r StageRequest) DryRunCreatePostInput() (staging.DryRunInput, error) {
	if validateConversion(StageSchema, r) != nil || r.Persist || r.Operation != CreatePost || r.RequestID != nil || r.Body != nil || r.Emoji != nil || len(r.Attachments) != 0 {
		return staging.DryRunInput{}, ErrInvalid
	}
	target, err := r.Target.StagingTarget()
	return staging.DryRunInput{Target: target}, err
}

func (r StageRequest) ReplyInput() (staging.ReplyInput, error) {
	if validateConversion(StageSchema, r) != nil || !r.Persist || r.Operation != Reply || r.RequestID == nil || r.Body == nil || r.Emoji != nil || r.Target.Kind != "post" || r.Target.PostID == "" {
		return staging.ReplyInput{}, ErrInvalid
	}
	return staging.ReplyInput{RequestID: *r.RequestID, PostID: r.Target.PostID, Body: strings.NewReader(*r.Body), Attachments: StagingAttachments(r.Attachments)}, nil
}

func (r StageRequest) EditPostInput() (staging.EditPostInput, error) {
	if validateConversion(StageSchema, r) != nil || !r.Persist || r.Operation != EditPost || r.RequestID == nil || r.Body == nil || r.Emoji != nil || r.Target.Kind != "post" || r.Target.PostID == "" || len(r.Attachments) != 0 {
		return staging.EditPostInput{}, ErrInvalid
	}
	return staging.EditPostInput{RequestID: *r.RequestID, PostID: r.Target.PostID, Body: strings.NewReader(*r.Body)}, nil
}

func (r StageRequest) DeletePostInput() (staging.DeletePostInput, error) {
	if validateConversion(StageSchema, r) != nil || !r.Persist || r.Operation != DeletePost || r.RequestID == nil || r.Emoji != nil || !r.contentlessPost() {
		return staging.DeletePostInput{}, ErrInvalid
	}
	return staging.DeletePostInput{RequestID: *r.RequestID, PostID: r.Target.PostID}, nil
}

func (r StageRequest) ReactionInput() (staging.ReactionInput, error) {
	if validateConversion(StageSchema, r) != nil || !r.Persist || (r.Operation != React && r.Operation != Unreact) || r.RequestID == nil || !r.contentlessPost() || r.Emoji == nil {
		return staging.ReactionInput{}, ErrInvalid
	}
	return staging.ReactionInput{RequestID: *r.RequestID, PostID: r.Target.PostID, Emoji: *r.Emoji}, nil
}

func (r StageRequest) PostDryRunInput() (staging.PostDryRunInput, error) {
	if validateConversion(StageSchema, r) != nil || r.Persist || (r.Operation != Reply && r.Operation != EditPost && r.Operation != DeletePost) || r.RequestID != nil || r.Emoji != nil || !r.contentlessPost() {
		return staging.PostDryRunInput{}, ErrInvalid
	}
	return staging.PostDryRunInput{PostID: r.Target.PostID}, nil
}

func (r StageRequest) ReactionDryRunInput() (staging.ReactionDryRunInput, error) {
	if validateConversion(StageSchema, r) != nil || r.Persist || (r.Operation != React && r.Operation != Unreact) || r.RequestID != nil || !r.contentlessPost() || r.Emoji == nil {
		return staging.ReactionDryRunInput{}, ErrInvalid
	}
	return staging.ReactionDryRunInput{PostID: r.Target.PostID, Emoji: *r.Emoji}, nil
}

func (r StageRequest) contentlessPost() bool {
	return r.Target.Kind == "post" && r.Target.PostID != "" && r.Body == nil && len(r.Attachments) == 0
}

func (r StageRequest) ResolveDMTarget() (staging.Target, error) {
	if validateConversion(StageSchema, r) != nil || r.Operation != ResolveDM || r.Target.Kind != "user" || r.Target.Username == "" || r.Body != nil || r.Emoji != nil || len(r.Attachments) != 0 || r.Persist != (r.RequestID != nil) {
		return staging.Target{}, ErrInvalid
	}
	return staging.Target{Conversation: staging.Direct, Selector: staging.ByUsername, Value: r.Target.Username}, nil
}

func (r StageRequest) ResolveGroupUsernames() ([]string, error) {
	if validateConversion(StageSchema, r) != nil || r.Operation != ResolveGroupDM || r.Target.Kind != "users" || len(r.Target.Usernames) < 2 || r.Body != nil || r.Emoji != nil || len(r.Attachments) != 0 || r.Persist != (r.RequestID != nil) {
		return nil, ErrInvalid
	}
	return append([]string(nil), r.Target.Usernames...), nil
}

func decodeDigest(value string) ([32]byte, error) {
	var out [32]byte
	if len(value) != 64 {
		return out, ErrInvalid
	}
	for _, c := range value {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return out, ErrInvalid
		}
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != len(out) {
		return out, ErrInvalid
	}
	copy(out[:], decoded)
	return out, nil
}

func (r ReviseRequest) ReviseInput() (staging.ReviseInput, error) {
	digest, err := decodeDigest(r.ExpectedDigest)
	if err != nil || validateConversion(ReviseSchema, r) != nil {
		return staging.ReviseInput{}, ErrInvalid
	}
	var body io.Reader
	if r.Body != nil {
		body = strings.NewReader(*r.Body)
	}
	return staging.ReviseInput{StageID: r.StageID, RequestID: r.RequestID, ExpectedRevision: int64(r.ExpectedRevision), ExpectedDigest: digest, Revive: r.Revive, Body: body, Attachments: StagingAttachments(r.Attachments)}, nil
}

func (r CancelRequest) CancelInput() (staging.CancelInput, error) {
	digest, err := decodeDigest(r.ExpectedDigest)
	if err != nil || validateConversion(CancelSchema, r) != nil {
		return staging.CancelInput{}, ErrInvalid
	}
	return staging.CancelInput{StageID: r.StageID, RequestID: r.RequestID, ExpectedRevision: int64(r.ExpectedRevision), ExpectedDigest: digest}, nil
}

func (r PruneRequest) PruneInput() (stagestore.PruneInput, error) {
	digest, err := decodeDigest(r.ExpectedDigest)
	if err != nil || digest == ([32]byte{}) || validateConversion(PruneSchema, r) != nil {
		return stagestore.PruneInput{}, ErrInvalid
	}
	return stagestore.PruneInput{StageID: r.StageID, RequestID: r.RequestID, ExpectedRevision: int64(r.ExpectedRevision), ExpectedDigest: digest, AbandonRecovery: r.AbandonRecovery}, nil
}

// ApplyClaimInput converts the public request and derives caller-intent replay
// identity. The request ID is deliberately excluded from the digest.
func (r ApplyRequest) ApplyClaimInput() (stagestore.ApplyClaimInput, error) {
	digest, err := decodeDigest(r.ExpectedDigest)
	mode := map[string]stagestore.RecoveryMode{
		string(stagestore.RecoveryModeOrdinary): stagestore.RecoveryModeOrdinary,
		string(stagestore.RecoveryModePartial):  stagestore.RecoveryModePartial,
		string(stagestore.RecoveryModeUnknown):  stagestore.RecoveryModeUnknown,
	}[r.RecoveryMode]
	if err != nil || mode == "" || validateConversion(ApplySchema, r) != nil {
		return stagestore.ApplyClaimInput{}, ErrInvalid
	}
	return NewApplyClaimInput(r.StageID, r.RequestID, int64(r.Revision), digest, mode), nil
}

// NewApplyClaimInput builds the same replay identity for human and structured
// callers. Human callers may omit requestID and therefore opt out of replay.
func NewApplyClaimInput(stageID, requestID string, revision int64, expectedDigest [32]byte, mode stagestore.RecoveryMode) stagestore.ApplyClaimInput {
	intent := struct {
		Domain         string                  `json:"domain"`
		StageID        string                  `json:"stageId"`
		Revision       int64                   `json:"revision"`
		ExpectedDigest string                  `json:"expectedDigest"`
		RecoveryMode   stagestore.RecoveryMode `json:"recoveryMode"`
	}{"mm/v2/apply-request/caller-intent/v1", stageID, revision, hex.EncodeToString(expectedDigest[:]), mode}
	raw, _ := json.Marshal(intent)
	requestDigest := [32]byte{}
	if requestID != "" {
		requestDigest = sha256.Sum256(raw)
	}
	return stagestore.ApplyClaimInput{StageID: stageID, RequestID: requestID, Revision: revision, ExpectedDigest: expectedDigest, RequestDigest: requestDigest, RecoveryMode: mode}
}
