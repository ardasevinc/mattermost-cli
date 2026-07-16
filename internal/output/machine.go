package output

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"
)

const MaxMachineDocumentBytes = 4 << 20

type MachineCompleteness string

const (
	MachineComplete  MachineCompleteness = "complete"
	MachineTruncated MachineCompleteness = "truncated"
	MachineUnknown   MachineCompleteness = "unknown"
)

// MachineMetadata preserves the distinction between false/empty and unknown.
// Pointer fields are always encoded: nil is the machine contract's explicit null.
type MachineMetadata struct {
	Completeness         MachineCompleteness `json:"completeness"`
	Selection            Selection           `json:"selection"`
	VisibleThreads       VisibleThreads      `json:"visibleThreads"`
	VisiblePostCount     int                 `json:"visiblePostCount"`
	DeletedPostsIncluded bool                `json:"deletedPostsIncluded"`
}

type MachineChannel struct {
	ID             string `json:"id"`
	Type           string `json:"type"`
	Name           string `json:"name"`
	DisplayName    string `json:"displayName"`
	MetadataStatus string `json:"metadataStatus"`
}

type MachineMessage struct {
	ID          string           `json:"id"`
	Permalink   string           `json:"permalink"`
	User        string           `json:"user"`
	UserID      string           `json:"userId"`
	Text        string           `json:"text"`
	Timestamp   MillisTime       `json:"timestamp"`
	UpdatedAt   MillisTime       `json:"updatedAt"`
	EditedAt    *MillisTime      `json:"editedAt"`
	DeletedAt   *MillisTime      `json:"deletedAt"`
	IsDeleted   bool             `json:"isDeleted"`
	PostType    string           `json:"postType"`
	IsSystem    bool             `json:"isSystem"`
	IsPinned    bool             `json:"isPinned"`
	RootID      *string          `json:"rootId"`
	ReplyCount  *int             `json:"replyCount"`
	Files       []string         `json:"files"`
	FileDetails []File           `json:"fileDetails"`
	Attachments []Attachment     `json:"attachments"`
	Reactions   []Reaction       `json:"reactions"`
	Replies     []MachineMessage `json:"replies"`
}

type MillisTime struct{ time.Time }

func (value MillisTime) MarshalJSON() ([]byte, error) {
	_, offset := value.Time.Zone()
	if offset != 0 {
		return nil, fmt.Errorf("machine timestamp must have zero UTC offset")
	}
	value.Time = value.Time.UTC()
	if value.Year() < 1 || value.Year() > 9999 {
		return nil, fmt.Errorf("machine timestamp year must be between 0001 and 9999")
	}
	return []byte(`"` + value.Truncate(time.Millisecond).Format("2006-01-02T15:04:05.000Z") + `"`), nil
}

type MachineHistory struct {
	Channel    MachineChannel   `json:"channel"`
	Messages   []MachineMessage `json:"messages"`
	Redactions []Redaction      `json:"redactions"`
	Metadata   MachineMetadata  `json:"metadata"`
}

type DMSEnvelope struct {
	Schema   string           `json:"schema"`
	Channels []MachineHistory `json:"channels"`
}
type GroupDMSEnvelope struct {
	Schema   string           `json:"schema"`
	Channels []MachineHistory `json:"channels"`
}
type ChannelEnvelope struct {
	Schema string         `json:"schema"`
	Data   MachineHistory `json:"data"`
}
type ThreadData struct {
	Channel    MachineChannel  `json:"channel"`
	Root       MachineMessage  `json:"root"`
	Redactions []Redaction     `json:"redactions"`
	Metadata   MachineMetadata `json:"metadata"`
}
type ThreadEnvelope struct {
	Schema string     `json:"schema"`
	Data   ThreadData `json:"data"`
}
type SearchEnvelope struct {
	Schema  string           `json:"schema"`
	Results []MachineHistory `json:"results"`
}
type MentionsEnvelope struct {
	Schema  string           `json:"schema"`
	Results []MachineHistory `json:"results"`
}
type ErrorEnvelope struct {
	Schema   string `json:"schema"`
	Code     string `json:"code"`
	Message  string `json:"message"`
	ExitCode int    `json:"exitCode"`
	Recovery string `json:"recovery"`
}

type MachineDocument interface{ machineDocument() }

func (DMSEnvelope) machineDocument()      {}
func (GroupDMSEnvelope) machineDocument() {}
func (ChannelEnvelope) machineDocument()  {}
func (ThreadEnvelope) machineDocument()   {}
func (SearchEnvelope) machineDocument()   {}
func (MentionsEnvelope) machineDocument() {}
func (ErrorEnvelope) machineDocument()    {}

type wireMessage struct {
	ID          string           `json:"id"`
	Permalink   string           `json:"permalink"`
	User        string           `json:"user"`
	UserID      string           `json:"userId"`
	Text        string           `json:"text"`
	Timestamp   MillisTime       `json:"timestamp"`
	UpdatedAt   MillisTime       `json:"updatedAt"`
	EditedAt    *MillisTime      `json:"editedAt"`
	DeletedAt   *MillisTime      `json:"deletedAt"`
	IsDeleted   bool             `json:"isDeleted"`
	PostType    string           `json:"postType"`
	IsSystem    bool             `json:"isSystem"`
	IsPinned    bool             `json:"isPinned"`
	RootID      *string          `json:"rootId"`
	ReplyCount  *int             `json:"replyCount"`
	Files       []string         `json:"files"`
	FileDetails []File           `json:"fileDetails"`
	Attachments []wireAttachment `json:"attachments"`
	Reactions   []wireReaction   `json:"reactions"`
	Replies     []wireMessage    `json:"replies"`
}

type wireAttachment struct {
	Fallback   string            `json:"fallback,omitempty"`
	Pretext    string            `json:"pretext,omitempty"`
	Title      string            `json:"title,omitempty"`
	TitleLink  string            `json:"titleLink,omitempty"`
	Text       string            `json:"text,omitempty"`
	Fields     []AttachmentField `json:"fields"`
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

type wireReaction struct {
	Emoji  string          `json:"emoji"`
	Count  int             `json:"count"`
	Actors []ReactionActor `json:"actors"`
}

type wireMetadata struct {
	Completeness         MachineCompleteness `json:"completeness"`
	Selection            Selection           `json:"selection"`
	VisibleThreads       wireVisibleThreads  `json:"visibleThreads"`
	VisiblePostCount     int                 `json:"visiblePostCount"`
	DeletedPostsIncluded bool                `json:"deletedPostsIncluded"`
}

type wireVisibleThreads struct {
	Status            string   `json:"status"`
	HydratedRootCount int      `json:"hydratedRootCount"`
	FailedRootIDs     []string `json:"failedRootIds"`
}

type wireHistory struct {
	Channel    MachineChannel `json:"channel"`
	Messages   []wireMessage  `json:"messages"`
	Redactions []Redaction    `json:"redactions"`
	Metadata   wireMetadata   `json:"metadata"`
}

func canonicalMachineDocument(document MachineDocument) (any, error) {
	switch value := document.(type) {
	case DMSEnvelope:
		return struct {
			Schema   string        `json:"schema"`
			Channels []wireHistory `json:"channels"`
		}{value.Schema, canonicalHistories(value.Channels)}, nil
	case GroupDMSEnvelope:
		return struct {
			Schema   string        `json:"schema"`
			Channels []wireHistory `json:"channels"`
		}{value.Schema, canonicalHistories(value.Channels)}, nil
	case ChannelEnvelope:
		return struct {
			Schema string      `json:"schema"`
			Data   wireHistory `json:"data"`
		}{value.Schema, canonicalHistory(value.Data)}, nil
	case ThreadEnvelope:
		return struct {
			Schema string `json:"schema"`
			Data   struct {
				Channel    MachineChannel `json:"channel"`
				Root       wireMessage    `json:"root"`
				Redactions []Redaction    `json:"redactions"`
				Metadata   wireMetadata   `json:"metadata"`
			} `json:"data"`
		}{value.Schema, struct {
			Channel    MachineChannel `json:"channel"`
			Root       wireMessage    `json:"root"`
			Redactions []Redaction    `json:"redactions"`
			Metadata   wireMetadata   `json:"metadata"`
		}{value.Data.Channel, canonicalMessage(value.Data.Root), cloneSlice(value.Data.Redactions), canonicalMetadata(value.Data.Metadata)}}, nil
	case SearchEnvelope:
		return struct {
			Schema  string        `json:"schema"`
			Results []wireHistory `json:"results"`
		}{value.Schema, canonicalHistories(value.Results)}, nil
	case MentionsEnvelope:
		return struct {
			Schema  string        `json:"schema"`
			Results []wireHistory `json:"results"`
		}{value.Schema, canonicalHistories(value.Results)}, nil
	case ErrorEnvelope:
		return value, nil
	default:
		return nil, fmt.Errorf("unsupported machine document type %T", document)
	}
}

func canonicalHistories(values []MachineHistory) []wireHistory {
	result := make([]wireHistory, len(values))
	for index := range values {
		result[index] = canonicalHistory(values[index])
	}
	return result
}

func canonicalHistory(value MachineHistory) wireHistory {
	messages := make([]wireMessage, len(value.Messages))
	for index := range value.Messages {
		messages[index] = canonicalMessage(value.Messages[index])
	}
	return wireHistory{value.Channel, messages, cloneSlice(value.Redactions), canonicalMetadata(value.Metadata)}
}

func canonicalMessage(value MachineMessage) wireMessage {
	attachments := make([]wireAttachment, len(value.Attachments))
	for index, attachment := range value.Attachments {
		attachments[index] = wireAttachment{attachment.Fallback, attachment.Pretext, attachment.Title, attachment.TitleLink, attachment.Text, cloneSlice(attachment.Fields), attachment.Footer, attachment.FooterIcon, attachment.AuthorName, attachment.AuthorLink, attachment.AuthorIcon, attachment.Color, attachment.ImageURL, attachment.ThumbURL, attachment.Timestamp}
	}
	reactions := make([]wireReaction, len(value.Reactions))
	for index, reaction := range value.Reactions {
		reactions[index] = wireReaction{reaction.Emoji, reaction.Count, cloneSlice(reaction.Actors)}
	}
	replies := make([]wireMessage, len(value.Replies))
	for index := range value.Replies {
		replies[index] = canonicalMessage(value.Replies[index])
	}
	return wireMessage{value.ID, value.Permalink, value.User, value.UserID, value.Text, value.Timestamp, value.UpdatedAt, value.EditedAt, value.DeletedAt, value.IsDeleted, value.PostType, value.IsSystem, value.IsPinned, value.RootID, value.ReplyCount, cloneSlice(value.Files), cloneSlice(value.FileDetails), attachments, reactions, replies}
}

func canonicalMetadata(value MachineMetadata) wireMetadata {
	return wireMetadata{value.Completeness, value.Selection, wireVisibleThreads{value.VisibleThreads.Status, value.VisibleThreads.HydratedRootCount, cloneSlice(value.VisibleThreads.FailedRootIDs)}, value.VisiblePostCount, value.DeletedPostsIncluded}
}

func cloneSlice[T any](values []T) []T {
	result := make([]T, len(values))
	copy(result, values)
	return result
}

// WriteMachineJSON encodes one bounded value, appends exactly one newline, and
// performs one write. A short or failed write is returned and never retried.
func WriteMachineJSON(w io.Writer, value MachineDocument) (int, error) {
	if err := preflightMachineDocument(value); err != nil {
		return 0, err
	}
	wireValue, err := canonicalMachineDocument(value)
	if err != nil {
		return 0, err
	}
	buffer := boundedBuffer{limit: MaxMachineDocumentBytes}
	wire := separatorWriter{destination: &buffer}
	encoder := json.NewEncoder(&wire)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(wireValue); err != nil {
		return 0, err
	}
	if err := wire.flush(); err != nil {
		return 0, err
	}
	data := buffer.Bytes()
	n, err := w.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	return n, err
}

var errMachineDocumentTooLarge = errors.New("machine JSON document exceeds size limit")

type boundedBuffer struct {
	bytes.Buffer
	limit int
}

type separatorWriter struct {
	destination io.Writer
	pending     [5]byte
	pendingLen  int
	slashRun    int
}

func (writer *separatorWriter) Write(data []byte) (int, error) {
	for index, current := range data {
		if err := writer.feed(current); err != nil {
			return index, err
		}
	}
	return len(data), nil
}

func (writer *separatorWriter) feed(current byte) error {
	if writer.pendingLen < len(writer.pending) {
		writer.pending[writer.pendingLen] = current
		writer.pendingLen++
		return nil
	}
	if writer.pending[0] == '\\' && writer.slashRun%2 == 0 &&
		bytes.Equal(writer.pending[1:], []byte("u202")) && (current == '8' || current == '9') {
		separator := []byte("\u2028")
		if current == '9' {
			separator = []byte("\u2029")
		}
		if _, err := writer.destination.Write(separator); err != nil {
			return err
		}
		writer.pendingLen = 0
		writer.slashRun = 0
		return nil
	}
	if err := writer.emit(writer.pending[0]); err != nil {
		return err
	}
	copy(writer.pending[:], writer.pending[1:])
	writer.pending[len(writer.pending)-1] = current
	return nil
}

func (writer *separatorWriter) flush() error {
	for index := 0; index < writer.pendingLen; index++ {
		if err := writer.emit(writer.pending[index]); err != nil {
			return err
		}
	}
	writer.pendingLen = 0
	return nil
}

func (writer *separatorWriter) emit(current byte) error {
	if _, err := writer.destination.Write([]byte{current}); err != nil {
		return err
	}
	if current == '\\' {
		writer.slashRun++
	} else {
		writer.slashRun = 0
	}
	return nil
}

const (
	machinePreflightMaxValues = 32768
	machinePreflightMaxDepth  = 256
)

func preflightMachineDocument(document MachineDocument) error {
	switch document.(type) {
	case DMSEnvelope, GroupDMSEnvelope, ChannelEnvelope, ThreadEnvelope, SearchEnvelope, MentionsEnvelope, ErrorEnvelope:
	default:
		return fmt.Errorf("unsupported machine document type %T", document)
	}
	contentBudget := int64(MaxMachineDocumentBytes)
	valueBudget := machinePreflightMaxValues
	stack := make(map[preflightVisit]bool)
	if err := consumeMachineBudget(reflect.ValueOf(document), &contentBudget, &valueBudget, stack, 0); err != nil {
		return err
	}
	return nil
}

type preflightVisit struct {
	typeName reflect.Type
	pointer  uintptr
}

func consumeMachineBudget(value reflect.Value, contentBudget *int64, valueBudget *int, stack map[preflightVisit]bool, depth int) error {
	if depth > machinePreflightMaxDepth {
		return fmt.Errorf("machine JSON document exceeds nesting limit")
	}
	if !value.IsValid() {
		return nil
	}
	*valueBudget--
	if *valueBudget < 0 {
		return fmt.Errorf("machine JSON document exceeds complexity limit of %d values", machinePreflightMaxValues)
	}
	if value.Type() == reflect.TypeFor[time.Time]() || value.Type() == reflect.TypeFor[MillisTime]() {
		return nil
	}
	switch value.Kind() {
	case reflect.String:
		*contentBudget -= int64(machineJSONStringBytes(value.String()))
	case reflect.Pointer, reflect.Interface:
		if !value.IsNil() {
			return consumeMachineBudget(value.Elem(), contentBudget, valueBudget, stack, depth+1)
		}
	case reflect.Struct:
		for index := 0; index < value.NumField(); index++ {
			if err := consumeMachineBudget(value.Field(index), contentBudget, valueBudget, stack, depth+1); err != nil {
				return err
			}
		}
	case reflect.Slice:
		if value.IsNil() {
			break
		}
		visit := preflightVisit{typeName: value.Type(), pointer: value.Pointer()}
		if stack[visit] {
			return fmt.Errorf("machine JSON document contains a collection cycle")
		}
		stack[visit] = true
		defer delete(stack, visit)
		for index := 0; index < value.Len(); index++ {
			if err := consumeMachineBudget(value.Index(index), contentBudget, valueBudget, stack, depth+1); err != nil {
				return err
			}
		}
	}
	if *contentBudget < 0 {
		return fmt.Errorf("%w: %d bytes", errMachineDocumentTooLarge, MaxMachineDocumentBytes)
	}
	return nil
}

func machineJSONStringBytes(value string) int {
	length := 2
	for index := 0; index < len(value); {
		current := value[index]
		if current < utf8.RuneSelf {
			switch current {
			case '\\', '"', '\b', '\f', '\n', '\r', '\t':
				length += 2
			default:
				if current < 0x20 {
					length += 6
				} else {
					length++
				}
			}
			index++
			continue
		}
		decoded, size := utf8.DecodeRuneInString(value[index:])
		if decoded == utf8.RuneError && size == 1 {
			length += 6
			index++
			continue
		}
		length += size
		index += size
	}
	return length
}

func (buffer *boundedBuffer) Write(data []byte) (int, error) {
	remaining := buffer.limit - buffer.Len()
	if len(data) > remaining {
		if remaining > 0 {
			_, _ = buffer.Buffer.Write(data[:remaining])
		}
		return remaining, fmt.Errorf("%w: %d bytes", errMachineDocumentTooLarge, buffer.limit)
	}
	return buffer.Buffer.Write(data)
}

func ValidMachineSchema(command, schema string) bool {
	return schema == "mm/v2/"+strings.TrimSpace(command)
}
