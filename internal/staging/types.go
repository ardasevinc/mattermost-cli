package staging

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/ardasevinc/mattermost-cli/v2/internal/mattermost"
	"github.com/ardasevinc/mattermost-cli/v2/internal/stageinput"
	"github.com/ardasevinc/mattermost-cli/v2/internal/stagestore"
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

type ReplyInput struct {
	RequestID   string
	PostID      string
	Body        io.Reader
	Attachments []Attachment
}

type EditPostInput struct {
	RequestID string
	PostID    string
	Body      io.Reader
}

type DeletePostInput struct{ RequestID, PostID string }
type ReactionInput struct{ RequestID, PostID, Emoji string }
type PostDryRunInput struct{ PostID string }
type ReactionDryRunInput struct{ PostID, Emoji string }

type DryRunInput struct{ Target Target }

type ResolveDMInput struct {
	RequestID string
	Target    Target
}

type ResolveGroupInput struct {
	RequestID string
	Usernames []string
}

type Destination struct {
	Kind            string     `json:"kind"`
	ChannelID       string     `json:"channelId"`
	ChannelType     string     `json:"channelType"`
	TeamID          *string    `json:"teamId"`
	PostID          *string    `json:"postId"`
	RootPostID      *string    `json:"rootPostId"`
	ParticipantIDs  []string   `json:"participantIds"`
	Emoji           *string    `json:"emoji"`
	PostState       *PostState `json:"postState"`
	ReactionPresent *bool      `json:"reactionPresent"`
}

// MarshalJSON preserves the public nullable channel identity while keeping the
// internal resolved-channel API ergonomic. Empty channel fields mean an exact
// participant set whose conversation still has to be resolved at apply time.
func (d Destination) MarshalJSON() ([]byte, error) {
	var channelID, channelType any
	if d.ChannelID != "" {
		channelID = d.ChannelID
	}
	if d.ChannelType != "" {
		channelType = d.ChannelType
	}
	return json.Marshal(struct {
		Kind            string     `json:"kind"`
		ChannelID       any        `json:"channelId"`
		ChannelType     any        `json:"channelType"`
		TeamID          *string    `json:"teamId"`
		PostID          *string    `json:"postId"`
		RootPostID      *string    `json:"rootPostId"`
		ParticipantIDs  []string   `json:"participantIds"`
		Emoji           *string    `json:"emoji"`
		PostState       *PostState `json:"postState"`
		ReactionPresent *bool      `json:"reactionPresent"`
	}{d.Kind, channelID, channelType, d.TeamID, d.PostID, d.RootPostID, d.ParticipantIDs, d.Emoji, d.PostState, d.ReactionPresent})
}

func (d *Destination) UnmarshalJSON(data []byte) error {
	if d == nil {
		return errors.New("nil destination")
	}
	var raw struct {
		Kind            json.RawMessage `json:"kind"`
		ChannelID       json.RawMessage `json:"channelId"`
		ChannelType     json.RawMessage `json:"channelType"`
		TeamID          json.RawMessage `json:"teamId"`
		PostID          json.RawMessage `json:"postId"`
		RootPostID      json.RawMessage `json:"rootPostId"`
		ParticipantIDs  json.RawMessage `json:"participantIds"`
		Emoji           json.RawMessage `json:"emoji"`
		PostState       json.RawMessage `json:"postState"`
		ReactionPresent json.RawMessage `json:"reactionPresent"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&raw) != nil || decoder.Decode(new(any)) != io.EOF {
		return errors.New("invalid destination")
	}
	required := []json.RawMessage{raw.Kind, raw.ChannelID, raw.ChannelType, raw.TeamID, raw.PostID, raw.RootPostID, raw.ParticipantIDs, raw.Emoji, raw.PostState, raw.ReactionPresent}
	for _, value := range required {
		if len(value) == 0 {
			return errors.New("incomplete destination")
		}
	}
	var out Destination
	if json.Unmarshal(raw.Kind, &out.Kind) != nil || decodeNullableString(raw.ChannelID, &out.ChannelID) != nil || decodeNullableString(raw.ChannelType, &out.ChannelType) != nil ||
		json.Unmarshal(raw.TeamID, &out.TeamID) != nil || json.Unmarshal(raw.PostID, &out.PostID) != nil || json.Unmarshal(raw.RootPostID, &out.RootPostID) != nil ||
		json.Unmarshal(raw.ParticipantIDs, &out.ParticipantIDs) != nil || out.ParticipantIDs == nil || json.Unmarshal(raw.Emoji, &out.Emoji) != nil ||
		json.Unmarshal(raw.PostState, &out.PostState) != nil || json.Unmarshal(raw.ReactionPresent, &out.ReactionPresent) != nil {
		return errors.New("invalid destination")
	}
	*d = out
	return nil
}

func decodeNullableString(raw json.RawMessage, out *string) error {
	if bytes.Equal(raw, []byte("null")) {
		*out = ""
		return nil
	}
	return json.Unmarshal(raw, out)
}

type PostState struct {
	AuthorUserID  string `json:"authorUserId"`
	UpdateAt      int64  `json:"updateAt"`
	ContentDigest string `json:"contentDigest"`
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

type Posts interface {
	ByID(context.Context, string) (mattermost.Post, error)
	ReactionState(context.Context, string, string, string, string) (bool, error)
}

type Store interface {
	FindCreate(context.Context, string, string, string) (stagestore.CreateRecord, bool, error)
	Create(context.Context, stagestore.CreateInput) (stagestore.CreateRecord, error)
}

type AttachmentBinder func(context.Context, []stageinput.Attachment, [][]byte) ([]stagestore.Attachment, error)

// RevisionStore is the narrow offline lifecycle surface used by Reviser.
type RevisionStore interface {
	Show(context.Context, string) (stagestore.StageDetail, error)
	FindRevise(context.Context, string, string, string, [32]byte) (stagestore.MutationResult, bool, error)
	Revise(context.Context, stagestore.ReviseInput) (stagestore.MutationResult, error)
	Cancel(context.Context, stagestore.CancelInput) (stagestore.MutationResult, error)
}

type ReviseInput struct {
	StageID, RequestID string
	ExpectedRevision   int64
	ExpectedDigest     [32]byte
	Revive             bool
	Body               io.Reader
	Attachments        []Attachment
}

type CancelInput struct {
	StageID, RequestID string
	ExpectedRevision   int64
	ExpectedDigest     [32]byte
}

type RevisionResult struct {
	Stored      stagestore.MutationResult
	Destination []byte
}
