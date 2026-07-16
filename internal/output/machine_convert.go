package output

import (
	"fmt"
	"time"
)

// MachineMessageFromMessage losslessly copies a presentation message into its
// machine representation. Timestamps retain their instant and are normalized
// to UTC; collection and pointer fields do not alias the input.
func MachineMessageFromMessage(message Message) MachineMessage {
	replies := make([]MachineMessage, len(message.Replies))
	for index := range message.Replies {
		replies[index] = MachineMessageFromMessage(message.Replies[index])
	}

	return MachineMessage{
		ID:          message.ID,
		Permalink:   message.Permalink,
		User:        message.User,
		UserID:      message.UserID,
		Text:        message.Text,
		Timestamp:   machineMillis(message.Timestamp),
		UpdatedAt:   machineMillis(message.UpdatedAt),
		EditedAt:    machineMillisPointer(message.EditedAt),
		DeletedAt:   machineMillisPointer(message.DeletedAt),
		IsDeleted:   message.IsDeleted,
		PostType:    message.PostType,
		IsSystem:    message.IsSystem,
		IsPinned:    message.IsPinned,
		RootID:      nullableString(message.RootID),
		ReplyCount:  clonePointer(message.ReplyCount),
		Files:       cloneSlice(message.Files),
		FileDetails: cloneFiles(message.FileDetails),
		Attachments: cloneAttachments(message.Attachments),
		Reactions:   cloneReactions(message.Reactions),
		Replies:     replies,
	}
}

// MachineHistoryFromOutput converts one independently headed output section.
func MachineHistoryFromOutput(value MessageOutput, completeness MachineCompleteness) (MachineHistory, error) {
	if !validMachineCompleteness(completeness) {
		return MachineHistory{}, fmt.Errorf("invalid machine completeness %q", completeness)
	}
	channel, err := machineChannel(value.Channel)
	if err != nil {
		return MachineHistory{}, err
	}
	if err := validateRetrieval(value.Retrieval); err != nil {
		return MachineHistory{}, err
	}
	if err := validateQueryCompleteness(value.Retrieval.Selection.QueryTruncated, completeness); err != nil {
		return MachineHistory{}, err
	}
	for index := range value.Messages {
		if err := validateMessage(value.Messages[index], 0); err != nil {
			return MachineHistory{}, fmt.Errorf("message %d: %w", index, err)
		}
	}
	for index, redaction := range value.Redactions {
		if redaction.Position < 0 {
			return MachineHistory{}, fmt.Errorf("redaction %d has negative position", index)
		}
	}
	messages := make([]MachineMessage, len(value.Messages))
	for index := range value.Messages {
		messages[index] = MachineMessageFromMessage(value.Messages[index])
	}
	return MachineHistory{
		Channel:    channel,
		Messages:   messages,
		Redactions: cloneSlice(value.Redactions),
		Metadata: MachineMetadata{
			Completeness:         completeness,
			Selection:            cloneSelection(value.Retrieval.Selection),
			VisibleThreads:       cloneVisibleThreads(value.Retrieval.VisibleThreads),
			VisiblePostCount:     value.Retrieval.VisiblePostCount,
			DeletedPostsIncluded: value.Retrieval.DeletedPostsIncluded,
		},
	}, nil
}

func validateQueryCompleteness(queryTruncated *bool, completeness MachineCompleteness) error {
	switch completeness {
	case MachineComplete:
		if queryTruncated == nil || *queryTruncated {
			return fmt.Errorf("complete retrieval requires queryTruncated false")
		}
	case MachineTruncated:
		if queryTruncated == nil || !*queryTruncated {
			return fmt.Errorf("truncated retrieval requires queryTruncated true")
		}
	case MachineUnknown:
		if queryTruncated != nil {
			return fmt.Errorf("unknown retrieval requires queryTruncated null")
		}
	default:
		return fmt.Errorf("invalid machine completeness %q", completeness)
	}
	return nil
}

func NewChannelEnvelope(value MessageOutput, completeness MachineCompleteness) (ChannelEnvelope, error) {
	if err := validateSource(value.Retrieval.Selection.Source, "recent"); err != nil {
		return ChannelEnvelope{}, err
	}
	history, err := MachineHistoryFromOutput(value, completeness)
	return ChannelEnvelope{Schema: "mm/v2/channel", Data: history}, err
}

func NewDMSEnvelope(values []MessageOutput, completeness MachineCompleteness) (DMSEnvelope, error) {
	if len(values) == 0 && completeness != MachineComplete {
		return DMSEnvelope{}, fmt.Errorf("empty direct-message output requires confirmed completeness")
	}
	histories, err := machineHistories(values, completeness, "recent", "dm", "unknown")
	return DMSEnvelope{Schema: "mm/v2/dms", Channels: histories}, err
}

func NewGroupDMSEnvelope(values []MessageOutput, completeness MachineCompleteness) (GroupDMSEnvelope, error) {
	if len(values) == 0 && completeness != MachineComplete {
		return GroupDMSEnvelope{}, fmt.Errorf("empty group direct-message output requires confirmed completeness")
	}
	histories, err := machineHistories(values, completeness, "recent", "group", "unknown")
	return GroupDMSEnvelope{Schema: "mm/v2/group-dms", Channels: histories}, err
}

func NewSearchEnvelope(values []MessageOutput, completeness MachineCompleteness) (SearchEnvelope, error) {
	if len(values) == 0 && completeness != MachineComplete {
		return SearchEnvelope{}, fmt.Errorf("empty search output requires confirmed completeness")
	}
	histories, err := machineHistories(values, completeness, "search")
	return SearchEnvelope{Schema: "mm/v2/search", Results: histories}, err
}

func NewMentionsEnvelope(values []MessageOutput, completeness MachineCompleteness) (MentionsEnvelope, error) {
	if len(values) == 0 && completeness != MachineComplete {
		return MentionsEnvelope{}, fmt.Errorf("empty mentions output requires confirmed completeness")
	}
	histories, err := machineHistories(values, completeness, "mentions")
	return MentionsEnvelope{Schema: "mm/v2/mentions", Results: histories}, err
}

// NewThreadEnvelope preserves a proven root when available and carries
// rootless partial posts separately. It never promotes unknown shape to root.
func NewThreadEnvelope(value MessageOutput, completeness MachineCompleteness) (ThreadEnvelope, error) {
	if err := validateSource(value.Retrieval.Selection.Source, "thread"); err != nil {
		return ThreadEnvelope{}, err
	}
	if err := validateThreadRetrieval(value.Retrieval, completeness); err != nil {
		return ThreadEnvelope{}, err
	}
	history, err := MachineHistoryFromOutput(value, completeness)
	if err != nil {
		return ThreadEnvelope{}, err
	}
	rootIndex := -1
	unboundIndexes := make([]int, 0)
	for index, message := range value.Messages {
		if threadShapeKnown(message) && message.RootID == "" && message.CanonicalRootID == "" {
			if rootIndex >= 0 {
				return ThreadEnvelope{}, fmt.Errorf("thread output contains multiple proven roots")
			}
			rootIndex = index
		} else {
			unboundIndexes = append(unboundIndexes, index)
		}
	}
	if completeness == MachineComplete && (rootIndex < 0 || len(unboundIndexes) != 0) {
		return ThreadEnvelope{}, fmt.Errorf("complete thread output requires one root and no unbound posts")
	}
	if rootIndex < 0 && value.Retrieval.VisibleThreads.Status != "partial" {
		return ThreadEnvelope{}, fmt.Errorf("rootless thread output must report partial hydration")
	}
	var machineRoot *MachineMessage
	rootIdentity := ""
	seenTopLevel := make(map[string]bool)
	if rootIndex >= 0 {
		root := value.Messages[rootIndex]
		rootIdentity = messageIdentity(root)
		if rootIdentity == "" || root.ID == "" {
			return ThreadEnvelope{}, fmt.Errorf("thread root must have presented and canonical identity")
		}
		seen := map[string]bool{rootIdentity: true, root.ID: true}
		seenTopLevel[rootIdentity], seenTopLevel[root.ID] = true, true
		for index, reply := range root.Replies {
			if len(reply.Replies) != 0 {
				return ThreadEnvelope{}, fmt.Errorf("thread reply %d contains nested replies", index)
			}
			replyRootIdentity := reply.CanonicalRootID
			if replyRootIdentity == "" {
				replyRootIdentity = reply.RootID
			}
			if reply.RootID != root.ID || replyRootIdentity != rootIdentity {
				return ThreadEnvelope{}, fmt.Errorf("thread reply %d does not reference root", index)
			}
			replyIdentity := messageIdentity(reply)
			if replyIdentity == "" || reply.ID == "" || seen[replyIdentity] || seen[reply.ID] {
				return ThreadEnvelope{}, fmt.Errorf("thread reply %d has missing or duplicate identity", index)
			}
			seen[replyIdentity], seen[reply.ID] = true, true
			seenTopLevel[replyIdentity], seenTopLevel[reply.ID] = true, true
		}
		converted := history.Messages[rootIndex]
		machineRoot = &converted
	}
	unbound := make([]MachineMessage, len(unboundIndexes))
	for outputIndex, messageIndex := range unboundIndexes {
		post := value.Messages[messageIndex]
		if len(post.Replies) != 0 {
			return ThreadEnvelope{}, fmt.Errorf("unbound post %d contains nested replies", outputIndex)
		}
		identity := messageIdentity(post)
		if identity == "" || post.ID == "" || seenTopLevel[identity] || seenTopLevel[post.ID] {
			return ThreadEnvelope{}, fmt.Errorf("unbound post %d has missing or duplicate identity", outputIndex)
		}
		if rootIdentity != "" {
			postRoot := post.CanonicalRootID
			if postRoot == "" {
				postRoot = post.RootID
			}
			if postRoot == rootIdentity {
				return ThreadEnvelope{}, fmt.Errorf("unbound post %d should be grouped under the proven root", outputIndex)
			}
		}
		seenTopLevel[identity], seenTopLevel[post.ID] = true, true
		unbound[outputIndex] = history.Messages[messageIndex]
	}
	return ThreadEnvelope{Schema: "mm/v2/thread", Data: ThreadData{
		Channel: history.Channel, Root: machineRoot, UnboundPosts: unbound,
		Redactions: history.Redactions, Metadata: history.Metadata,
	}}, nil
}

func validateThreadRetrieval(value Retrieval, completeness MachineCompleteness) error {
	queryTruncated := value.Selection.QueryTruncated
	visible := value.VisibleThreads
	switch completeness {
	case MachineComplete:
		if queryTruncated == nil || *queryTruncated || visible.Status != "complete" || visible.HydratedRootCount != 1 || len(visible.FailedRootIDs) != 0 {
			return fmt.Errorf("complete thread retrieval requires confirmed complete query and hydration metadata")
		}
	case MachineTruncated:
		if queryTruncated == nil || !*queryTruncated || visible.Status != "partial" || visible.HydratedRootCount != 0 || len(visible.FailedRootIDs) == 0 {
			return fmt.Errorf("truncated thread retrieval requires confirmed truncation and partial hydration metadata")
		}
	case MachineUnknown:
		if queryTruncated != nil || visible.Status != "partial" || visible.HydratedRootCount != 0 || len(visible.FailedRootIDs) == 0 {
			return fmt.Errorf("unknown thread retrieval requires unknown query and partial hydration metadata")
		}
	default:
		return fmt.Errorf("invalid machine completeness %q", completeness)
	}
	return nil
}

func messageIdentity(message Message) string {
	if message.CanonicalID != "" {
		return message.CanonicalID
	}
	return message.ID
}

func machineHistories(values []MessageOutput, completeness MachineCompleteness, source string, channelTypes ...string) ([]MachineHistory, error) {
	if !validMachineCompleteness(completeness) {
		return nil, fmt.Errorf("invalid machine completeness %q", completeness)
	}
	result := make([]MachineHistory, len(values))
	for index := range values {
		if err := validateSource(values[index].Retrieval.Selection.Source, source); err != nil {
			return nil, fmt.Errorf("convert output %d: %w", index, err)
		}
		if len(channelTypes) != 0 && !containsString(channelTypes, values[index].Channel.Type) {
			return nil, fmt.Errorf("convert output %d: channel type %q is not valid for this envelope", index, values[index].Channel.Type)
		}
		converted, err := MachineHistoryFromOutput(values[index], completeness)
		if err != nil {
			return nil, fmt.Errorf("convert output %d: %w", index, err)
		}
		result[index] = converted
	}
	return result, nil
}

func machineChannel(channel Channel) (MachineChannel, error) {
	switch channel.Type {
	case "dm", "public", "private", "group", "unknown":
	default:
		return MachineChannel{}, fmt.Errorf("invalid machine channel type %q", channel.Type)
	}
	switch channel.MetadataStatus {
	case "resolved", "unavailable":
	default:
		return MachineChannel{}, fmt.Errorf("invalid machine channel metadata status %q", channel.MetadataStatus)
	}
	if (channel.Type == "unknown") != (channel.MetadataStatus == "unavailable") {
		return MachineChannel{}, fmt.Errorf("machine channel type %q and metadata status %q are inconsistent", channel.Type, channel.MetadataStatus)
	}
	return MachineChannel{ID: channel.ID, Type: channel.Type, Name: channel.Name, DisplayName: channel.DisplayName, MetadataStatus: channel.MetadataStatus}, nil
}

func validMachineCompleteness(value MachineCompleteness) bool {
	return value == MachineComplete || value == MachineTruncated || value == MachineUnknown
}

func validateSource(source string, allowed ...string) error {
	for _, value := range allowed {
		if source == value {
			return nil
		}
	}
	return fmt.Errorf("selection source %q is not valid for this envelope", source)
}

func validateRetrieval(value Retrieval) error {
	switch value.Selection.Source {
	case "recent", "search", "mentions", "unread", "thread":
	default:
		return fmt.Errorf("invalid selection source %q", value.Selection.Source)
	}
	if value.Selection.SelectedCount < 0 {
		return fmt.Errorf("selected count must be nonnegative")
	}
	if value.Selection.RequestedLimit != nil && *value.Selection.RequestedLimit < 1 {
		return fmt.Errorf("requested limit must be positive")
	}
	if value.VisiblePostCount < 0 || value.VisibleThreads.HydratedRootCount < 0 {
		return fmt.Errorf("visible post and hydrated root counts must be nonnegative")
	}
	switch value.VisibleThreads.Status {
	case "not_requested":
		if value.VisibleThreads.HydratedRootCount != 0 || len(value.VisibleThreads.FailedRootIDs) != 0 {
			return fmt.Errorf("threads not requested cannot have hydration results")
		}
	case "complete":
		if len(value.VisibleThreads.FailedRootIDs) != 0 {
			return fmt.Errorf("complete thread hydration cannot have failed roots")
		}
	case "partial":
	default:
		return fmt.Errorf("invalid visible thread status %q", value.VisibleThreads.Status)
	}
	if value.DeletedPostsIncluded {
		return fmt.Errorf("machine schema does not permit deleted posts to be included")
	}
	return nil
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func validateMessage(message Message, depth int) error {
	if depth > machinePreflightMaxDepth {
		return fmt.Errorf("reply nesting exceeds machine limit")
	}
	for name, value := range map[string]time.Time{"timestamp": message.Timestamp, "updatedAt": message.UpdatedAt} {
		if err := validateMachineTime(value); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	for name, value := range map[string]*time.Time{"editedAt": message.EditedAt, "deletedAt": message.DeletedAt} {
		if value != nil {
			if err := validateMachineTime(*value); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
		}
	}
	if message.ReplyCount != nil && *message.ReplyCount < 0 {
		return fmt.Errorf("reply count must be nonnegative")
	}
	for index, file := range message.FileDetails {
		if file.Size != nil && *file.Size < 0 {
			return fmt.Errorf("file %d size must be nonnegative", index)
		}
	}
	for index, reaction := range message.Reactions {
		if reaction.Count < 0 {
			return fmt.Errorf("reaction %d count must be nonnegative", index)
		}
	}
	for index := range message.Replies {
		if err := validateMessage(message.Replies[index], depth+1); err != nil {
			return fmt.Errorf("reply %d: %w", index, err)
		}
	}
	return nil
}

func validateMachineTime(value time.Time) error {
	utc := value.UTC()
	if utc.Year() < 1 || utc.Year() > 9999 {
		return fmt.Errorf("timestamp year must be between 0001 and 9999 after UTC normalization")
	}
	return nil
}

func machineMillis(value time.Time) MillisTime { return MillisTime{Time: value.UTC()} }

func machineMillisPointer(value *time.Time) *MillisTime {
	if value == nil {
		return nil
	}
	converted := machineMillis(*value)
	return &converted
}

func nullableString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func clonePointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneFiles(values []File) []File {
	result := cloneSlice(values)
	for index := range result {
		result[index].Size = clonePointer(result[index].Size)
	}
	return result
}

func cloneAttachments(values []Attachment) []Attachment {
	result := cloneSlice(values)
	for index := range result {
		result[index].Fields = cloneSlice(result[index].Fields)
		for fieldIndex := range result[index].Fields {
			result[index].Fields[fieldIndex].Short = clonePointer(result[index].Fields[fieldIndex].Short)
		}
	}
	return result
}

func cloneReactions(values []Reaction) []Reaction {
	result := cloneSlice(values)
	for index := range result {
		result[index].Actors = cloneSlice(result[index].Actors)
	}
	return result
}

func cloneSelection(value Selection) Selection {
	value.RequestedLimit = clonePointer(value.RequestedLimit)
	value.Since = clonePointer(value.Since)
	value.QueryTruncated = clonePointer(value.QueryTruncated)
	value.InputCursor = clonePointer(value.InputCursor)
	value.NextCursor = clonePointer(value.NextCursor)
	return value
}

func cloneVisibleThreads(value VisibleThreads) VisibleThreads {
	value.FailedRootIDs = cloneSlice(value.FailedRootIDs)
	return value
}
