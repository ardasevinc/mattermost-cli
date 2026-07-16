package staging

import (
	"context"
	"io"

	"github.com/ardasevinc/mattermost-cli/internal/mattermost"
	"github.com/ardasevinc/mattermost-cli/internal/stageinput"
	"github.com/ardasevinc/mattermost-cli/internal/stagestore"
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
