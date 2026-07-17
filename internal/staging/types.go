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
