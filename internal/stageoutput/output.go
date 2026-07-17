// Package stageoutput converts trusted staging domain values into strict public
// stage documents. Every constructor validates the finished document against
// its embedded schema and rejects active credentials anywhere in emitted text.
package stageoutput

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ardasevinc/mattermost-cli/internal/schema"
	"github.com/ardasevinc/mattermost-cli/internal/stagecursor"
	"github.com/ardasevinc/mattermost-cli/internal/stagestore"
	"github.com/ardasevinc/mattermost-cli/internal/staging"
)

var ErrInvalid = errors.New("stage output: invalid or unsafe state")

type Binding struct {
	ServerURL string  `json:"serverUrl"`
	ServerID  *string `json:"serverId"`
	UserID    string  `json:"userId"`
}
type Summary struct {
	StageID        string              `json:"stageId"`
	StageRef       string              `json:"stageRef"`
	Revision       int64               `json:"revision"`
	Operation      string              `json:"operation"`
	SemanticDigest string              `json:"semanticDigest"`
	Lifecycle      string              `json:"lifecycle"`
	Recovery       string              `json:"recovery"`
	CreatedAt      string              `json:"createdAt"`
	UpdatedAt      string              `json:"updatedAt"`
	Binding        Binding             `json:"binding"`
	Destination    staging.Destination `json:"destination"`
}
type Preview struct {
	Schema           string              `json:"schema"`
	Persist          bool                `json:"persist"`
	Operation        string              `json:"operation"`
	Binding          Binding             `json:"binding"`
	Destination      staging.Destination `json:"destination"`
	Plan             staging.Plan        `json:"plan"`
	ContentValidated bool                `json:"contentValidated"`
}
type Receipt struct {
	Schema     string  `json:"schema"`
	Action     string  `json:"action"`
	Revived    bool    `json:"revived"`
	Replayed   bool    `json:"replayed"`
	RecordedAt string  `json:"recordedAt"`
	Stage      Summary `json:"stage"`
}
type Content struct {
	State string  `json:"state"`
	Body  *string `json:"body"`
}
type Attachment struct {
	Path           string `json:"path"`
	CanonicalPath  string `json:"canonicalPath"`
	RemoteFilename string `json:"remoteFilename"`
	ByteLength     int64  `json:"byteLength"`
	MediaType      string `json:"mediaType"`
	ContentDigest  string `json:"contentDigest"`
}
type Stage struct {
	Schema          string       `json:"schema"`
	Stage           Summary      `json:"stage"`
	RevisionState   string       `json:"revisionState"`
	Content         Content      `json:"content"`
	AttachmentState string       `json:"attachmentState"`
	Attachments     []Attachment `json:"attachments"`
	Plan            staging.Plan `json:"plan"`
}
type Stages struct {
	Schema     string    `json:"schema"`
	Stages     []Summary `json:"stages"`
	NextCursor *string   `json:"nextCursor"`
}
type PruneResult struct {
	Schema      string `json:"schema"`
	Action      string `json:"action"`
	Cutoff      string `json:"cutoff"`
	PrunedCount int64  `json:"prunedCount"`
	RecordedAt  string `json:"recordedAt"`
}

func NewPruneResult(in stagestore.BulkPruneResult, credentials []string) (PruneResult, error) {
	if in.Schema != "mm/v2/stage-prune-result" || in.Action != "pruned" || in.PrunedCount < 0 || in.PrunedCount > 9_007_199_254_740_991 || in.Cutoff.IsZero() || in.RecordedAt.IsZero() || !in.Cutoff.Before(in.RecordedAt) {
		return PruneResult{}, ErrInvalid
	}
	out := PruneResult{in.Schema, in.Action, stamp(in.Cutoff), in.PrunedCount, stamp(in.RecordedAt)}
	return out, validate("mm/v2/stage-prune-result", out, credentials)
}

func NewPreview(operation stagestore.Operation, in staging.Preview, credentials []string) (Preview, error) {
	out := Preview{"mm/v2/stage-preview", false, string(operation), binding(in.ServerURL, in.ServerID, in.UserID), cloneDestination(in.Destination), clonePlan(in.Plan), false}
	if !validPlan(operation, out.Plan, -1) {
		return Preview{}, ErrInvalid
	}
	return out, validate("mm/v2/stage-preview", out, credentials)
}

func NewCreateReceipt(in staging.CreatePostResult, credentials []string) (Receipt, error) {
	d, err := decodeDestination(in.Stored.Stage.Operation, mustJSON(in.Preview.Destination))
	if err != nil {
		return Receipt{}, ErrInvalid
	}
	return newReceipt(in.Stored, d, credentials)
}

func NewReceipt(in stagestore.MutationResult, destination json.RawMessage, credentials []string) (Receipt, error) {
	d, err := decodeDestination(in.Stage.Operation, destination)
	if err != nil {
		return Receipt{}, ErrInvalid
	}
	return newReceipt(in, d, credentials)
}

func newReceipt(in stagestore.MutationResult, d staging.Destination, credentials []string) (Receipt, error) {
	actions := map[string]string{"create": "created", "revise": "revised", "cancel": "canceled", "prune": "pruned"}
	if !validSummaryTimes(in.Stage) || in.RecordedAt.IsZero() || emittedTime(in.RecordedAt).Before(emittedTime(in.Stage.UpdatedAt)) {
		return Receipt{}, ErrInvalid
	}
	out := Receipt{"mm/v2/stage-receipt", actions[in.Action], in.Revived, in.Replay, stamp(in.RecordedAt), summary(in.Stage, d)}
	return out, validate("mm/v2/stage-receipt", out, credentials)
}

func NewStage(in stagestore.StageDetail, credentials []string) (Stage, error) {
	if !stagestore.VerifyDetail(in) {
		return Stage{}, ErrInvalid
	}
	d, err := decodeDestination(in.Operation, in.Destination)
	if err != nil {
		return Stage{}, ErrInvalid
	}
	var plan staging.Plan
	if err := strictDecode(in.Plan, &plan); err != nil {
		return Stage{}, ErrInvalid
	}
	plan = clonePlan(plan)
	contentful := in.Operation == stagestore.CreatePost || in.Operation == stagestore.Reply || in.Operation == stagestore.EditPost
	pruned := in.Lifecycle == stagestore.LifecycleCompleted || in.Lifecycle == stagestore.LifecyclePruned
	attachmentCount := len(in.Attachments)
	if pruned {
		attachmentCount = -1
	}
	if !validSummaryTimes(in.StageSummary) || in.RevisionCreatedAt.IsZero() ||
		emittedTime(in.RevisionCreatedAt).Before(emittedTime(in.CreatedAt)) || emittedTime(in.RevisionCreatedAt).After(emittedTime(in.UpdatedAt)) ||
		!validPlan(in.Operation, plan, attachmentCount) {
		return Stage{}, ErrInvalid
	}
	out := Stage{Schema: "mm/v2/stage", Stage: summary(in.StageSummary, d), RevisionState: "current", Attachments: make([]Attachment, 0), Plan: plan}
	if (!contentful && (in.Body != nil || len(in.Attachments) != 0)) ||
		(in.Operation == stagestore.EditPost && len(in.Attachments) != 0) ||
		(contentful && !pruned && in.Body == nil) ||
		(pruned && (in.Body != nil || len(in.Attachments) != 0)) {
		return Stage{}, ErrInvalid
	}
	if !contentful {
		out.Content = Content{"none", nil}
	} else if pruned || in.Body == nil {
		out.Content = Content{"pruned", nil}
	} else {
		body := string(bytes.Clone(in.Body))
		out.Content = Content{"present", &body}
	}
	if !contentful || in.Operation == stagestore.EditPost {
		out.AttachmentState = "none"
	} else if pruned {
		out.AttachmentState = "none"
		for _, step := range plan.Steps {
			if step.Type == "upload_attachment" {
				out.AttachmentState = "pruned"
				break
			}
		}
	} else if len(in.Attachments) == 0 {
		out.AttachmentState = "none"
	} else {
		out.AttachmentState = "retained"
	}
	if out.AttachmentState == "retained" {
		for _, a := range in.Attachments {
			out.Attachments = append(out.Attachments, Attachment{a.SuppliedPath, a.CanonicalPath, a.RemoteFilename, a.ByteLength, a.MediaType, hex.EncodeToString(a.ContentDigest[:])})
		}
	}
	return out, validate("mm/v2/stage", out, credentials)
}

func NewStages(page stagestore.ListPage, credentials []string) (Stages, error) {
	in := page.Records
	if len(in) > 100 {
		return Stages{}, ErrInvalid
	}
	out := Stages{Schema: "mm/v2/stages", Stages: make([]Summary, 0, len(in))}
	if page.NextCursor != nil {
		if len(in) == 0 {
			return Stages{}, ErrInvalid
		}
		boundary, err := stagecursor.Decode(*page.NextCursor)
		last := in[len(in)-1]
		if err != nil || !boundary.UpdatedAt.Equal(last.UpdatedAt) || boundary.StageID != last.ID {
			return Stages{}, ErrInvalid
		}
		v := *page.NextCursor
		out.NextCursor = &v
	}
	seen := make(map[string]struct{}, len(in))
	for i, record := range in {
		updated := emittedTime(record.UpdatedAt)
		if _, exists := seen[record.ID]; exists || !validSummaryTimes(record.StageSummary) ||
			i > 0 && (updated.After(emittedTime(in[i-1].UpdatedAt)) || (updated.Equal(emittedTime(in[i-1].UpdatedAt)) && record.ID < in[i-1].ID)) {
			return Stages{}, ErrInvalid
		}
		seen[record.ID] = struct{}{}
		d, err := decodeDestination(record.Operation, record.Destination)
		if err != nil {
			return Stages{}, ErrInvalid
		}
		out.Stages = append(out.Stages, summary(record.StageSummary, d))
	}
	return out, validate("mm/v2/stages", out, credentials)
}

func summary(s stagestore.StageSummary, d staging.Destination) Summary {
	return Summary{s.ID, s.ID + "@" + strconv.FormatInt(s.Revision, 10), s.Revision, string(s.Operation), hex.EncodeToString(s.SemanticDigest[:]), string(s.Lifecycle), string(s.Recovery), stamp(s.CreatedAt), stamp(s.UpdatedAt), binding(s.ServerURL, s.ServerID, s.UserID), cloneDestination(d)}
}
func binding(url, id, user string) Binding {
	var p *string
	if id != "" {
		v := id
		p = &v
	}
	return Binding{url, p, user}
}
func stamp(t time.Time) string          { return t.UTC().Format("2006-01-02T15:04:05.000Z") }
func emittedTime(t time.Time) time.Time { return t.UTC().Truncate(time.Millisecond) }
func validSummaryTimes(s stagestore.StageSummary) bool {
	return s.SemanticDigest != ([32]byte{}) && !s.CreatedAt.IsZero() && !s.UpdatedAt.IsZero() && !emittedTime(s.UpdatedAt).Before(emittedTime(s.CreatedAt))
}

func validPlan(operation stagestore.Operation, plan staging.Plan, attachmentCount int) bool {
	if len(plan.Steps) == 0 {
		return false
	}
	for i, step := range plan.Steps {
		if step.Ordinal != i+1 {
			return false
		}
	}
	terminalType, terminalCondition := "", "always"
	switch operation {
	case stagestore.CreatePost, stagestore.Reply:
		terminalType = "create_post"
		start := 0
		if first := plan.Steps[0]; first.Type == "resolve_conversation" && first.Condition == "if_missing" {
			start = 1
		}
		uploads := len(plan.Steps) - start - 1
		if uploads < 0 {
			return false
		}
		if attachmentCount >= 0 && uploads != attachmentCount {
			return false
		}
		for _, step := range plan.Steps[start : start+uploads] {
			if step.Type != "upload_attachment" || step.Condition != "always" {
				return false
			}
		}
	case stagestore.EditPost:
		terminalType = "edit_post"
	case stagestore.DeletePost:
		terminalType = "delete_post"
	case stagestore.React:
		terminalType, terminalCondition = "add_reaction", "if_missing"
	case stagestore.Unreact:
		terminalType, terminalCondition = "remove_reaction", "if_missing"
	case stagestore.ResolveDM, stagestore.ResolveGroupDM:
		terminalType, terminalCondition = "resolve_conversation", "if_missing"
	default:
		return false
	}
	last := plan.Steps[len(plan.Steps)-1]
	return last.Type == terminalType && last.Condition == terminalCondition &&
		(operation == stagestore.CreatePost || operation == stagestore.Reply || len(plan.Steps) == 1)
}
func cloneDestination(d staging.Destination) staging.Destination {
	out := d
	out.ParticipantIDs = append([]string{}, d.ParticipantIDs...)
	if out.ParticipantIDs == nil {
		out.ParticipantIDs = []string{}
	}
	if d.TeamID != nil {
		v := *d.TeamID
		out.TeamID = &v
	}
	if d.PostID != nil {
		v := *d.PostID
		out.PostID = &v
	}
	if d.RootPostID != nil {
		v := *d.RootPostID
		out.RootPostID = &v
	}
	if d.Emoji != nil {
		v := *d.Emoji
		out.Emoji = &v
	}
	if d.PostState != nil {
		v := *d.PostState
		out.PostState = &v
	}
	if d.ReactionPresent != nil {
		v := *d.ReactionPresent
		out.ReactionPresent = &v
	}
	return out
}
func clonePlan(p staging.Plan) staging.Plan {
	return staging.Plan{Steps: append([]staging.PlanStep{}, p.Steps...)}
}
func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }
func decodeDestination(_ stagestore.Operation, raw []byte) (staging.Destination, error) {
	var d staging.Destination
	if err := strictDecode(raw, &d); err != nil {
		return d, err
	}
	return cloneDestination(d), nil
}
func strictDecode(raw []byte, out any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return errors.New("invalid JSON trailer")
	}
	return nil
}

var registryOnce sync.Once
var registry *schema.Registry
var registryErr error

func validate(id string, value any, credentials []string) error {
	b, err := json.Marshal(value)
	if err != nil {
		return ErrInvalid
	}
	for _, credential := range credentials {
		if credential != "" && (strings.Contains(string(b), credential) || containsString(reflect.ValueOf(value), credential)) {
			return ErrInvalid
		}
	}
	registryOnce.Do(func() { registry, registryErr = schema.Load() })
	if registryErr != nil {
		return fmt.Errorf("%w: schemas unavailable", ErrInvalid)
	}
	if err = registry.Validate(id, bytes.NewReader(b)); err != nil {
		return ErrInvalid
	}
	return nil
}

func containsString(v reflect.Value, needle string) bool {
	if !v.IsValid() {
		return false
	}
	if v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return false
		}
		return containsString(v.Elem(), needle)
	}
	switch v.Kind() {
	case reflect.String:
		return strings.Contains(v.String(), needle)
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if containsString(v.Field(i), needle) {
				return true
			}
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			if containsString(v.Index(i), needle) {
				return true
			}
		}
	case reflect.Map:
		for _, key := range v.MapKeys() {
			if containsString(key, needle) || containsString(v.MapIndex(key), needle) {
				return true
			}
		}
	}
	return false
}
