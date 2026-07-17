package output

import (
	"time"

	"github.com/ardasevinc/mattermost-cli/v2/internal/presentation"
)

// Redaction is presentation-owned because its Position is a UTF-16 code-unit
// offset into the final sanitized text. Output models must not recompute it.
type Redaction = presentation.Redaction

type Message struct {
	ID          string       `json:"id"`
	Permalink   string       `json:"permalink"`
	User        string       `json:"user"`
	UserID      string       `json:"userId"`
	Text        string       `json:"text"`
	Timestamp   time.Time    `json:"timestamp"`
	UpdatedAt   time.Time    `json:"updatedAt"`
	EditedAt    *time.Time   `json:"editedAt,omitempty"`
	DeletedAt   *time.Time   `json:"deletedAt,omitempty"`
	IsDeleted   bool         `json:"isDeleted"`
	PostType    string       `json:"postType"`
	IsSystem    bool         `json:"isSystem"`
	IsPinned    bool         `json:"isPinned"`
	Files       []string     `json:"files"`
	FileDetails []File       `json:"fileDetails"`
	Attachments []Attachment `json:"attachments"`
	Reactions   []Reaction   `json:"reactions"`
	RootID      string       `json:"rootId,omitempty"`
	ReplyCount  *int         `json:"replyCount,omitempty"`
	Replies     []Message    `json:"replies,omitempty"`

	// Canonical identities are unsanitized internal values. They drive
	// ordering and grouping but are never serialized.
	CanonicalID               string `json:"-"`
	CanonicalRootID           string `json:"-"`
	CanonicalThreadShapeKnown *bool  `json:"-"`
}

type File struct {
	ID        string `json:"id"`
	Name      string `json:"name,omitempty"`
	MIME      string `json:"mime,omitempty"`
	Size      *int64 `json:"size,omitempty"`
	Extension string `json:"extension,omitempty"`
}

type AttachmentField struct {
	Title string `json:"title,omitempty"`
	Value string `json:"value,omitempty"`
	Short *bool  `json:"short,omitempty"`
}

type Attachment struct {
	Fallback   string            `json:"fallback,omitempty"`
	Pretext    string            `json:"pretext,omitempty"`
	Title      string            `json:"title,omitempty"`
	TitleLink  string            `json:"titleLink,omitempty"`
	Text       string            `json:"text,omitempty"`
	Fields     []AttachmentField `json:"fields,omitempty"`
	Footer     string            `json:"footer,omitempty"`
	FooterIcon string            `json:"footerIcon,omitempty"`
	AuthorName string            `json:"authorName,omitempty"`
	AuthorLink string            `json:"authorLink,omitempty"`
	AuthorIcon string            `json:"authorIcon,omitempty"`
	Color      string            `json:"color,omitempty"`
	ImageURL   string            `json:"imageUrl,omitempty"`
	ThumbURL   string            `json:"thumbUrl,omitempty"`
	Timestamp  string            `json:"timestamp,omitempty"`
}

type ReactionActor struct {
	ID       string `json:"id"`
	Username string `json:"username,omitempty"`
}

type Reaction struct {
	Emoji  string          `json:"emoji"`
	Count  int             `json:"count"`
	Actors []ReactionActor `json:"actors"`
}

type Channel struct {
	ID             string `json:"id"`
	Type           string `json:"type"`
	Name           string `json:"name"`
	DisplayName    string `json:"displayName,omitempty"`
	MetadataStatus string `json:"metadataStatus"`
}

type Retrieval struct {
	Selection            Selection      `json:"selection"`
	VisibleThreads       VisibleThreads `json:"visibleThreads"`
	VisiblePostCount     int            `json:"visiblePostCount"`
	DeletedPostsIncluded bool           `json:"deletedPostsIncluded"`
}

type Selection struct {
	Source         string  `json:"source"`
	SelectedCount  int     `json:"selectedCount"`
	RequestedLimit *int    `json:"requestedLimit"`
	Since          *string `json:"since"`
	QueryTruncated *bool   `json:"queryTruncated"`
	InputCursor    *string `json:"inputCursor"`
	NextCursor     *string `json:"nextCursor"`
}

type VisibleThreads struct {
	Status            string   `json:"status"`
	HydratedRootCount int      `json:"hydratedRootCount"`
	FailedRootIDs     []string `json:"failedRootIds"`
}
