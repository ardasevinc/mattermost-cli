package output

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/ardasevinc/mattermost-cli/internal/presentation"
)

type RawUnreadItem struct {
	Channel      RawChannel
	UnreadCount  int64
	MentionCount int64
	LastViewedAt int64
}

type UnreadItem struct {
	Channel      ChannelItem `json:"channel"`
	UnreadCount  int64       `json:"unreadCount"`
	MentionCount int64       `json:"mentionCount"`
	LastViewedAt *MillisTime `json:"lastViewedAt"`
}

type UnreadData struct {
	Unread []UnreadItem     `json:"unread"`
	Peek   []MachineHistory `json:"peek"`
}

type UnreadEnvelope struct {
	Schema string     `json:"schema"`
	Data   UnreadData `json:"data"`
	proof  *UnreadData
}

type UnreadProof struct {
	// PeekLimit is nil when peek was not requested. A non-nil value requires
	// one full history per unread summary channel, including confirmed empties.
	PeekLimit *int
}

func NewUnreadEnvelope(raw []RawUnreadItem, peek []MessageOutput, proof UnreadProof, options presentation.Options) (UnreadEnvelope, error) {
	values := append([]RawUnreadItem(nil), raw...)
	sort.Slice(values, func(i, j int) bool {
		if values[i].MentionCount != values[j].MentionCount {
			return values[i].MentionCount > values[j].MentionCount
		}
		if values[i].UnreadCount != values[j].UnreadCount {
			return values[i].UnreadCount > values[j].UnreadCount
		}
		return values[i].Channel.ID < values[j].Channel.ID
	})
	if proof.PeekLimit == nil && len(peek) != 0 {
		return UnreadEnvelope{}, errors.New("peek histories supplied without a request")
	}
	if proof.PeekLimit != nil && (*proof.PeekLimit < 1 || int64(*proof.PeekLimit) > MaxSafeMachineInteger || len(peek) != len(values)) {
		return UnreadEnvelope{}, errors.New("incomplete unread peek histories")
	}
	options.Credentials = append(append([]string(nil), options.Credentials...), presentation.ActiveCredentials.Values()...)
	items := make([]UnreadItem, len(values))
	seen := make(map[string]struct{}, len(values))
	for i, value := range values {
		if !rawRequired(value.Channel.ID) || value.UnreadCount < 1 || !safeCount(value.UnreadCount) || !safeCount(value.MentionCount) || value.LastViewedAt < 0 {
			return UnreadEnvelope{}, errors.New("invalid raw unread item")
		}
		if _, duplicate := seen[value.Channel.ID]; duplicate {
			return UnreadEnvelope{}, errors.New("duplicate raw unread channel")
		}
		seen[value.Channel.ID] = struct{}{}
		channel, err := NewChannelsEnvelope([]RawChannel{value.Channel}, options)
		if err != nil {
			return UnreadEnvelope{}, err
		}
		viewed := presentTimestamp(value.LastViewedAt)
		if value.LastViewedAt > 0 && viewed == nil {
			return UnreadEnvelope{}, errors.New("invalid unread last viewed timestamp")
		}
		items[i] = UnreadItem{Channel: channel.Channels[0], UnreadCount: value.UnreadCount, MentionCount: value.MentionCount, LastViewedAt: viewed}
	}
	histories := make([]MachineHistory, 0, len(peek))
	for i, value := range peek {
		if err := validateUnreadPeekOutput(value, items[i], *proof.PeekLimit, options); err != nil {
			return UnreadEnvelope{}, err
		}
		complete := MachineComplete
		if *value.Retrieval.Selection.QueryTruncated {
			complete = MachineTruncated
		}
		history, err := MachineHistoryFromOutput(value, complete)
		if err != nil {
			return UnreadEnvelope{}, fmt.Errorf("invalid unread peek history: %w", err)
		}
		histories = append(histories, history)
	}
	document := UnreadEnvelope{Schema: "mm/v2/unread", Data: UnreadData{Unread: items, Peek: histories}}
	if err := preflightUnreadCandidate(document); err != nil {
		return UnreadEnvelope{}, err
	}
	proofData := cloneUnreadData(document.Data)
	document.proof = &proofData
	return document, nil
}

// MessageOutput is the established post normalization and hydration boundary.
// This constructor validates and snapshots it; it deliberately does not create
// a second raw-message presentation pipeline.
func validateUnreadPeekOutput(value MessageOutput, item UnreadItem, limit int, options presentation.Options) error {
	selection := value.Retrieval.Selection
	displayName := ""
	if item.Channel.DisplayName != nil {
		displayName = *item.Channel.DisplayName
	}
	if value.Channel.ID != item.Channel.ID || value.Channel.Type != item.Channel.Type || value.Channel.Name != item.Channel.Name ||
		value.Channel.DisplayName != displayName || value.Channel.MetadataStatus != "resolved" || selection.Source != "unread" ||
		selection.RequestedLimit == nil || *selection.RequestedLimit != limit || int64(*selection.RequestedLimit) > MaxSafeMachineInteger || selection.QueryTruncated == nil || selection.SelectedCount < 0 ||
		selection.SelectedCount > limit || selection.InputCursor != nil || selection.NextCursor != nil || value.Retrieval.VisibleThreads.Status != "not_requested" ||
		value.Retrieval.VisibleThreads.HydratedRootCount != 0 || len(value.Retrieval.VisibleThreads.FailedRootIDs) != 0 || value.Retrieval.DeletedPostsIncluded {
		return errors.New("unbound unread peek history")
	}
	wantSince := nullableMillisString(item.LastViewedAt)
	visible, validGraph := validateUnreadMessageGraph(value.Messages)
	if !reflect.DeepEqual(selection.Since, wantSince) || !validGraph || visible != selection.SelectedCount || visible != value.Retrieval.VisiblePostCount ||
		!safePresentedOutput(value, options) {
		return errors.New("invalid unread peek presentation")
	}
	return nil
}

func nullableMillisString(value *MillisTime) *string {
	if value == nil {
		return nil
	}
	text := value.UTC().Format("2006-01-02T15:04:05.000Z")
	return &text
}

func validateUnreadMessageGraph(messages []Message) (int, bool) {
	presented := make(map[string]string)
	canonical := make(map[string]string)
	count := 0
	for _, message := range messages {
		if message.ID == "" || message.CanonicalID == "" || (message.RootID == "") != (message.CanonicalRootID == "") || (len(message.Replies) > 0 && (message.RootID != "" || message.CanonicalRootID != "")) {
			return 0, false
		}
		if prior, duplicate := presented[message.ID]; duplicate || (prior != "" && prior != message.CanonicalID) {
			return 0, false
		}
		if prior, duplicate := canonical[message.CanonicalID]; duplicate || (prior != "" && prior != message.ID) {
			return 0, false
		}
		presented[message.ID], canonical[message.CanonicalID] = message.CanonicalID, message.ID
		count++
		for _, reply := range message.Replies {
			if len(reply.Replies) != 0 || reply.ID == "" || reply.CanonicalID == "" || reply.RootID != message.ID || reply.CanonicalRootID != message.CanonicalID {
				return 0, false
			}
			if _, duplicate := presented[reply.ID]; duplicate {
				return 0, false
			}
			if _, duplicate := canonical[reply.CanonicalID]; duplicate {
				return 0, false
			}
			presented[reply.ID], canonical[reply.CanonicalID] = reply.CanonicalID, reply.ID
			count++
		}
	}
	return count, true
}

func safePresentedOutput(value MessageOutput, options presentation.Options) bool {
	label := func(text string) bool { return safePresentedString(text, false, options) }
	multiline := func(text string) bool { return safePresentedString(text, true, options) }
	for _, text := range []string{value.Channel.ID, value.Channel.Type, value.Channel.Name, value.Channel.DisplayName, value.Channel.MetadataStatus} {
		if !label(text) {
			return false
		}
	}
	selection := value.Retrieval.Selection
	for _, pointer := range []*string{selection.Since, selection.InputCursor, selection.NextCursor} {
		if pointer != nil && !label(*pointer) {
			return false
		}
	}
	for _, id := range value.Retrieval.VisibleThreads.FailedRootIDs {
		if !label(id) {
			return false
		}
	}
	for _, redaction := range value.Redactions {
		if !label(redaction.Type) || !label(redaction.Masked) || !label(redaction.Field) {
			return false
		}
	}
	var messageSafe func(Message) bool
	messageSafe = func(message Message) bool {
		for _, text := range []string{message.ID, message.Permalink, message.User, message.UserID, message.PostType, message.RootID} {
			if !label(text) {
				return false
			}
		}
		if !multiline(message.Text) {
			return false
		}
		for _, id := range message.Files {
			if !label(id) {
				return false
			}
		}
		for _, file := range message.FileDetails {
			for _, text := range []string{file.ID, file.Name, file.MIME, file.Extension} {
				if !label(text) {
					return false
				}
			}
		}
		for _, attachment := range message.Attachments {
			for _, text := range []string{attachment.TitleLink, attachment.FooterIcon, attachment.AuthorLink, attachment.AuthorIcon, attachment.Color, attachment.ImageURL, attachment.ThumbURL, attachment.Timestamp} {
				if !label(text) {
					return false
				}
			}
			for _, text := range []string{attachment.Fallback, attachment.Pretext, attachment.Title, attachment.Text, attachment.Footer, attachment.AuthorName} {
				if !multiline(text) {
					return false
				}
			}
			for _, field := range attachment.Fields {
				if !multiline(field.Title) || !multiline(field.Value) {
					return false
				}
			}
		}
		for _, reaction := range message.Reactions {
			if !label(reaction.Emoji) {
				return false
			}
			for _, actor := range reaction.Actors {
				if !label(actor.ID) || !label(actor.Username) {
					return false
				}
			}
		}
		for _, reply := range message.Replies {
			if !messageSafe(reply) {
				return false
			}
		}
		return true
	}
	for _, message := range value.Messages {
		if !messageSafe(message) {
			return false
		}
	}
	return true
}

func safePresentedString(value string, allowMultiline bool, options presentation.Options) bool {
	if value == "" {
		return true
	}
	if allowMultiline {
		if presentation.SanitizeControls(value) != value {
			return false
		}
	} else if presentation.SanitizeLabel(value) != value {
		return false
	}
	exact := presentation.PreprocessWithOptions(value, presentation.Options{Credentials: options.Credentials, DisableHeuristics: true})
	return exact.Text == value
}

func validateUnreadEnvelope(document UnreadEnvelope) error {
	if document.Schema != "mm/v2/unread" || document.Data.Unread == nil || document.Data.Peek == nil || document.proof == nil {
		return errors.New("invalid unread document")
	}
	if !reflect.DeepEqual(document.Data, *document.proof) {
		return errors.New("mutated unread document")
	}
	return nil
}

func preflightUnreadCandidate(document UnreadEnvelope) error {
	contentBudget := int64(MaxMachineDocumentBytes)
	valueBudget := machinePreflightMaxValues
	return consumeMachineBudget(reflect.ValueOf(document), &contentBudget, &valueBudget, make(map[preflightVisit]bool), 0)
}

func cloneUnreadData(value UnreadData) UnreadData {
	unread := cloneSlice(value.Unread)
	for i := range unread {
		unread[i].Channel.DisplayName = clonePointer(unread[i].Channel.DisplayName)
		unread[i].Channel.Team = cloneChannelTeam(unread[i].Channel.Team)
		unread[i].Channel.LastPost = clonePointer(unread[i].Channel.LastPost)
		unread[i].LastViewedAt = clonePointer(unread[i].LastViewedAt)
	}
	peek := make([]MachineHistory, len(value.Peek))
	for i := range value.Peek {
		peek[i] = cloneMachineHistory(value.Peek[i])
	}
	return UnreadData{Unread: unread, Peek: peek}
}

func cloneChannelTeam(value *ChannelTeam) *ChannelTeam {
	if value == nil {
		return nil
	}
	copy := *value
	copy.DisplayName = clonePointer(value.DisplayName)
	return &copy
}

func cloneMachineHistory(value MachineHistory) MachineHistory {
	messages := make([]MachineMessage, len(value.Messages))
	for i := range value.Messages {
		messages[i] = cloneMachineMessage(value.Messages[i])
	}
	return MachineHistory{Channel: value.Channel, Messages: messages, Redactions: cloneSlice(value.Redactions), Metadata: MachineMetadata{
		Completeness: value.Metadata.Completeness, Selection: cloneSelection(value.Metadata.Selection), VisibleThreads: cloneVisibleThreads(value.Metadata.VisibleThreads),
		VisiblePostCount: value.Metadata.VisiblePostCount, DeletedPostsIncluded: value.Metadata.DeletedPostsIncluded,
	}}
}

func cloneMachineMessage(value MachineMessage) MachineMessage {
	copy := value
	copy.EditedAt, copy.DeletedAt, copy.RootID, copy.ReplyCount = clonePointer(value.EditedAt), clonePointer(value.DeletedAt), clonePointer(value.RootID), clonePointer(value.ReplyCount)
	copy.Files, copy.FileDetails = cloneSlice(value.Files), cloneSlice(value.FileDetails)
	for i := range copy.FileDetails {
		copy.FileDetails[i].Size = clonePointer(value.FileDetails[i].Size)
	}
	copy.Attachments = cloneSlice(value.Attachments)
	for i := range copy.Attachments {
		copy.Attachments[i].Fields = cloneSlice(value.Attachments[i].Fields)
		for j := range copy.Attachments[i].Fields {
			copy.Attachments[i].Fields[j].Short = clonePointer(value.Attachments[i].Fields[j].Short)
		}
	}
	copy.Reactions = cloneSlice(value.Reactions)
	for i := range copy.Reactions {
		copy.Reactions[i].Actors = cloneSlice(value.Reactions[i].Actors)
	}
	copy.Replies = make([]MachineMessage, len(value.Replies))
	for i := range value.Replies {
		copy.Replies[i] = cloneMachineMessage(value.Replies[i])
	}
	return copy
}

func FormatUnreadPretty(document UnreadEnvelope, peek []MessageOutput, dates DateFormatter, options PrettyOptions) (string, error) {
	return formatUnreadHuman(document, peek, FormatPretty(peek, dates, options))
}

func FormatUnreadMarkdown(document UnreadEnvelope, peek []MessageOutput, dates DateFormatter, options MarkdownOptions) (string, error) {
	return formatUnreadHuman(document, peek, FormatMarkdown(peek, dates, options))
}

func formatUnreadHuman(document UnreadEnvelope, peek []MessageOutput, renderedPeek string) (string, error) {
	if err := validateUnreadEnvelope(document); err != nil {
		return "", err
	}
	if len(document.Data.Peek) == 0 {
		if len(peek) != 0 {
			return "", errors.New("peek does not match unread document")
		}
	} else {
		if len(peek) != len(document.Data.Peek) {
			return "", errors.New("peek does not match unread document")
		}
		for i := range peek {
			complete := document.Data.Peek[i].Metadata.Completeness
			converted, err := MachineHistoryFromOutput(peek[i], complete)
			if err != nil || !reflect.DeepEqual(converted, document.Data.Peek[i]) {
				return "", errors.New("peek does not match unread document")
			}
		}
	}
	if len(document.Data.Unread) == 0 {
		return "All caught up!", nil
	}
	lines := []string{"Unread Channels:", ""}
	for _, item := range document.Data.Unread {
		label := item.Channel.Name
		if item.Channel.Type == "public" || item.Channel.Type == "private" {
			label = "#" + label
		}
		summary := fmt.Sprintf("%d unread", item.UnreadCount)
		if item.MentionCount > 0 {
			summary += fmt.Sprintf(", %d mentions", item.MentionCount)
		}
		lines = append(lines, fmt.Sprintf("  %-32s %s", label, summary))
	}
	lines = append(lines, "", fmt.Sprintf("Total: %d channels with unread messages", len(document.Data.Unread)))
	result := strings.Join(lines, "\n")
	if renderedPeek != "" {
		result += "\n\n" + renderedPeek
	}
	return result, nil
}
