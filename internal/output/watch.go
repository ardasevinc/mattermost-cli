package output

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"time"

	"github.com/ardasevinc/mattermost-cli/v2/internal/mattermost"
	"github.com/ardasevinc/mattermost-cli/v2/internal/presentation"
)

const MaxWatchLineBytes = 1 << 20

var ErrPartialWatchLine = errors.New("partial JSONL write; stream is no longer recoverable")
var ErrInvalidWatchDocument = errors.New("invalid watch document")
var ErrWatchOutput = errors.New("watch output unavailable")

type WatchDocument interface {
	watchDocument()
	valid() bool
}
type WatchSequence struct {
	ConnectionID string `json:"connectionId"`
	Number       int64  `json:"number"`
}
type watchEvent struct {
	Schema      string                   `json:"schema"`
	Type        string                   `json:"type"`
	Sequence    WatchSequence            `json:"sequence"`
	PostID      string                   `json:"postId"`
	ChannelID   string                   `json:"channelId"`
	ChannelName string                   `json:"channelName"`
	SenderID    string                   `json:"senderId"`
	Sender      string                   `json:"sender"`
	Message     string                   `json:"message"`
	Timestamp   MillisTime               `json:"timestamp"`
	RootID      *string                  `json:"rootId"`
	FileIDs     []string                 `json:"fileIds"`
	Redactions  []presentation.Redaction `json:"redactions"`
	seal        bool
}
type watchDiagnostic struct {
	Schema     string                   `json:"schema"`
	Type       string                   `json:"type"`
	Code       string                   `json:"code,omitempty"`
	Recovery   string                   `json:"recovery,omitempty"`
	Timestamp  MillisTime               `json:"timestamp"`
	Message    string                   `json:"message"`
	Backfill   bool                     `json:"backfill"`
	Fatal      bool                     `json:"fatal"`
	Redactions []presentation.Redaction `json:"redactions"`
	Attempt    *int                     `json:"attempt,omitempty"`
	DelayMS    *int64                   `json:"delayMs,omitempty"`
	Expected   *int64                   `json:"expected,omitempty"`
	Received   *int64                   `json:"received,omitempty"`
	PreviousID *string                  `json:"previousConnectionId,omitempty"`
	CurrentID  *string                  `json:"currentConnectionId,omitempty"`
	seal       bool
}

func (watchEvent) watchDocument()      {}
func (watchDiagnostic) watchDocument() {}
func (value watchEvent) valid() bool {
	if !value.seal || value.Schema != "mm/v2/watch-event" || value.Type != "posted" || value.Sequence.ConnectionID == "" || value.Sequence.Number < 0 || value.Sequence.Number > mattermost.MaxSafeSequence || value.PostID == "" || value.ChannelID == "" || value.SenderID == "" || value.Timestamp.Time.IsZero() || value.FileIDs == nil || value.Redactions == nil || value.RootID != nil && *value.RootID == "" {
		return false
	}
	for _, id := range value.FileIDs {
		if id == "" {
			return false
		}
	}
	return true
}
func (value watchDiagnostic) valid() bool {
	if !value.seal || value.Schema != "mm/v2/watch-diagnostic" || value.Message == "" || value.Timestamp.Time.IsZero() || value.Backfill || value.Redactions == nil {
		return false
	}
	switch value.Type {
	case "warning":
		return !value.Fatal && (value.Code == "configuration_warning" || value.Code == "redaction_disabled") && value.Recovery == "none" && value.Attempt == nil && value.DelayMS == nil && value.Expected == nil && value.Received == nil && value.PreviousID == nil && value.CurrentID == nil
	case "reconnect":
		return value.Code == "" && value.Recovery == "" && !value.Fatal && value.Attempt != nil && *value.Attempt > 0 && value.DelayMS != nil && *value.DelayMS >= 0 && value.Expected == nil && value.Received == nil && value.PreviousID == nil && value.CurrentID == nil
	case "sequence_gap":
		return value.Code == "" && value.Recovery == "" && !value.Fatal && value.Expected != nil && *value.Expected >= 0 && *value.Expected <= mattermost.MaxSafeSequence && value.Received != nil && *value.Received >= 0 && *value.Received <= mattermost.MaxSafeSequence && value.Attempt == nil && value.DelayMS == nil && value.PreviousID == nil && value.CurrentID == nil
	case "connection_changed":
		return value.Code == "" && value.Recovery == "" && !value.Fatal && value.Expected != nil && *value.Expected >= 0 && *value.Expected <= mattermost.MaxSafeSequence && value.Received != nil && *value.Received >= 0 && *value.Received <= mattermost.MaxSafeSequence && value.PreviousID != nil && *value.PreviousID != "" && value.CurrentID != nil && *value.CurrentID != "" && value.Attempt == nil && value.DelayMS == nil
	case "malformed", "disconnected":
		return value.Code == "" && value.Recovery == "" && !value.Fatal && value.Attempt == nil && value.DelayMS == nil && value.Expected == nil && value.Received == nil && value.PreviousID == nil && value.CurrentID == nil
	case "terminal":
		validCode := value.Code == "authentication" || value.Code == "reconnect_exhausted" || value.Code == "canceled" || value.Code == "invalid_options" || value.Code == "watch_failed"
		validRecovery := (value.Code == "authentication" && value.Recovery == "check_token") || (value.Code == "reconnect_exhausted" && value.Recovery == "retry_later") || ((value.Code == "canceled" || value.Code == "invalid_options" || value.Code == "watch_failed") && value.Recovery == "none")
		return value.Fatal && validCode && validRecovery && value.Attempt == nil && value.DelayMS == nil && value.Expected == nil && value.Received == nil && value.PreviousID == nil && value.CurrentID == nil
	default:
		return false
	}
}

func NewWatchEvent(post mattermost.WatchPost, sequence mattermost.Sequence, disableHeuristics bool) (WatchDocument, error) {
	if post.ID == "" || post.ChannelID == "" || post.UserID == "" || sequence.ConnectionID == "" || sequence.Number < 0 || sequence.Number > mattermost.MaxSafeSequence || post.CreateAt < 0 || post.FileIDs == nil || rawWatchSize(post, sequence) > MaxWatchLineBytes {
		return nil, ErrInvalidWatchDocument
	}
	credentials := presentation.ActiveCredentials.Values()
	clean := func(value string, label bool, field string) (string, []presentation.Redaction) {
		if label {
			value = presentation.SanitizeLabel(value)
		}
		result := presentation.PreprocessWithOptions(value, presentation.Options{Credentials: credentials, DisableHeuristics: disableHeuristics})
		text := result.Text
		for i := range result.Redactions {
			result.Redactions[i].Field = field
		}
		return text, result.Redactions
	}
	postID, r := clean(post.ID, true, "watch.postId")
	redactions := append([]presentation.Redaction{}, r...)
	channelID, r := clean(post.ChannelID, true, "watch.channelId")
	redactions = append(redactions, r...)
	channelName, r := clean(post.ChannelName, true, "watch.channelName")
	redactions = append(redactions, r...)
	senderID, r := clean(post.UserID, true, "watch.senderId")
	redactions = append(redactions, r...)
	sender, r := clean(post.SenderName, true, "watch.sender")
	redactions = append(redactions, r...)
	message, r := clean(post.Message, false, "watch.message")
	redactions = append(redactions, r...)
	connectionID, r := clean(sequence.ConnectionID, true, "watch.sequence.connectionId")
	redactions = append(redactions, r...)
	files := make([]string, len(post.FileIDs))
	for i, id := range post.FileIDs {
		if id == "" {
			return nil, ErrInvalidWatchDocument
		}
		files[i], r = clean(id, true, "watch.fileId")
		redactions = append(redactions, r...)
	}
	var root *string
	if post.RootID != "" {
		value, rr := clean(post.RootID, true, "watch.rootId")
		root = &value
		redactions = append(redactions, rr...)
	}
	document := watchEvent{Schema: "mm/v2/watch-event", Type: "posted", Sequence: WatchSequence{connectionID, sequence.Number}, PostID: postID, ChannelID: channelID, ChannelName: channelName, SenderID: senderID, Sender: sender, Message: message, Timestamp: MillisTime{Time: time.UnixMilli(post.CreateAt).UTC()}, RootID: root, FileIDs: files, Redactions: redactions, seal: true}
	if !document.valid() {
		return nil, ErrInvalidWatchDocument
	}
	return document, nil
}

func NewWatchDiagnostic(value mattermost.WatchDiagnostic) (WatchDocument, error) {
	if len(value.Message)+len(value.PreviousID)+len(value.CurrentID) > MaxWatchLineBytes {
		return nil, ErrInvalidWatchDocument
	}
	credentials := presentation.ActiveCredentials.Values()
	clean := func(text, field string) (string, []presentation.Redaction) {
		text = presentation.SanitizeLabel(text)
		result := presentation.PreprocessWithOptions(text, presentation.Options{Credentials: credentials})
		for i := range result.Redactions {
			result.Redactions[i].Field = field
		}
		return result.Text, result.Redactions
	}
	message, redactions := clean(value.Message, "watch.diagnostic.message")
	document := watchDiagnostic{Schema: "mm/v2/watch-diagnostic", Type: value.Type, Code: value.Code, Recovery: value.Recovery, Timestamp: MillisTime{Time: value.Timestamp.UTC()}, Message: message, Backfill: false, Fatal: value.Fatal, Redactions: redactions, Attempt: value.Attempt, Expected: value.Expected, Received: value.Received, seal: true}
	if value.Delay != nil {
		ms := value.Delay.Milliseconds()
		document.DelayMS = &ms
	}
	if value.PreviousID != "" {
		cleaned, reds := clean(value.PreviousID, "watch.diagnostic.previousConnectionId")
		document.PreviousID = &cleaned
		document.Redactions = append(document.Redactions, reds...)
	}
	if value.CurrentID != "" {
		cleaned, reds := clean(value.CurrentID, "watch.diagnostic.currentConnectionId")
		document.CurrentID = &cleaned
		document.Redactions = append(document.Redactions, reds...)
	}
	if !document.valid() {
		return nil, ErrInvalidWatchDocument
	}
	return document, nil
}

type limitedBuffer struct {
	bytes.Buffer
	limit int
}

func (writer *limitedBuffer) Write(data []byte) (int, error) {
	if writer.Len()+len(data) > writer.limit {
		return 0, errors.New("JSONL document exceeds limit")
	}
	return writer.Buffer.Write(data)
}
func WriteWatchDocument(writer io.Writer, document WatchDocument) error {
	if nilableWriter(writer) {
		return ErrWatchOutput
	}
	if document == nil || !document.valid() {
		return ErrInvalidWatchDocument
	}
	buffer := limitedBuffer{limit: MaxWatchLineBytes}
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(document); err != nil {
		return err
	}
	written, err := writer.Write(buffer.Bytes())
	if written != buffer.Len() {
		return ErrPartialWatchLine
	}
	return err
}

func nilableWriter(writer io.Writer) bool {
	if writer == nil {
		return true
	}
	value := reflect.ValueOf(writer)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice, reflect.UnsafePointer:
		return value.IsNil()
	default:
		return false
	}
}

func rawWatchSize(post mattermost.WatchPost, sequence mattermost.Sequence) int {
	total := len(post.ID) + len(post.ChannelID) + len(post.UserID) + len(post.Message) + len(post.RootID) + len(post.ChannelName) + len(post.SenderName) + len(sequence.ConnectionID)
	for _, id := range post.FileIDs {
		total += len(id)
	}
	return total
}

type JSONLWatchSink struct {
	Events, Diagnostics io.Writer
	DisableHeuristics   bool
}

func FormatWatchHumanLine(timestamp time.Time, sender, message string, color bool) string {
	message = strings.Join(strings.Fields(strings.ReplaceAll(message, "\ufeff", " ")), " ")
	if message == "" {
		message = "[empty message]"
	}
	stamp := "[" + timestamp.Format("15:04") + "]"
	if color {
		return dim(stamp) + " " + userColor(sender) + ": " + message
	}
	return stamp + " " + sender + ": " + message
}

func (sink JSONLWatchSink) Post(post mattermost.WatchPost, sequence mattermost.Sequence) error {
	document, err := NewWatchEvent(post, sequence, sink.DisableHeuristics)
	if err != nil {
		return err
	}
	return WriteWatchDocument(sink.Events, document)
}
func (sink JSONLWatchSink) Diagnostic(value mattermost.WatchDiagnostic) error {
	document, err := NewWatchDiagnostic(value)
	if err != nil {
		return err
	}
	return WriteWatchDocument(sink.Diagnostics, document)
}
