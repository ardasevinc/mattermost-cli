package output

import (
	"fmt"
	"io"
	"slices"
	"strings"
	"time"
	"unicode/utf16"
)

const (
	ansiReset = "\x1b[0m"
	ansiBold  = "\x1b[1m"
	ansiDim   = "\x1b[2m"
	ansiCyan  = "\x1b[36m"
)

type PrettyOptions struct {
	Color    bool
	Relative bool
}

// MessageOutput is one independently headed channel section.
type MessageOutput struct {
	Channel    Channel
	Messages   []Message
	Redactions []Redaction
	Retrieval  Retrieval
}

// FormatPretty renders already-presented values. It deliberately performs no
// sanitization or redaction of its own.
func FormatPretty(outputs []MessageOutput, dates DateFormatter, options PrettyOptions) string {
	sections := make([]string, 0, len(outputs))
	for _, output := range outputs {
		if options.Color {
			sections = append(sections, formatColorSection(output, dates, options.Relative))
		} else {
			sections = append(sections, formatPlainSection(output, dates, options.Relative))
		}
	}
	separator := "\n" + strings.Repeat("=", 60) + "\n\n"
	if options.Color {
		separator = "\n" + dim(strings.Repeat("─", 60)) + "\n\n"
	}
	return strings.Join(sections, separator)
}

// WritePretty renders once and writes once. A short write is returned as
// io.ErrShortWrite and is never retried, since the writer's state is unknown.
func WritePretty(w io.Writer, outputs []MessageOutput, dates DateFormatter, options PrettyOptions) (int, error) {
	formatted := FormatPretty(outputs, dates, options)
	n, err := io.WriteString(w, formatted)
	if err == nil && n != len(formatted) {
		err = io.ErrShortWrite
	}
	return n, err
}

func formatColorSection(output MessageOutput, dates DateFormatter, relative bool) string {
	lines := []string{bold(colorHeader(output.Channel)), ""}
	appendMessagesByDate(&lines, output.Messages, dates, func(message Message) {
		lines = append(lines, formatColorMessage(message, dates, relative, "  "))
	}, true)
	if len(output.Redactions) > 0 {
		lines = append(lines, "", dim(fmt.Sprintf("  ⚠ %d secret(s) redacted", len(output.Redactions))))
	}
	appendCoverage(&lines, output)
	return strings.Join(lines, "\n")
}

func formatPlainSection(output MessageOutput, dates DateFormatter, relative bool) string {
	lines := []string{plainHeader(output.Channel), strings.Repeat("─", 40)}
	appendMessagesByDate(&lines, output.Messages, dates, func(message Message) {
		appendPlainMessage(&lines, message, dates, relative, "  ")
	}, false)
	if len(output.Redactions) > 0 {
		lines = append(lines, fmt.Sprintf("  [%d secret(s) redacted]", len(output.Redactions)))
	}
	appendCoverage(&lines, output)
	return strings.Join(lines, "\n")
}

func appendMessagesByDate(lines *[]string, messages []Message, dates DateFormatter, appendMessage func(Message), color bool) {
	sorted := slices.Clone(messages)
	slices.SortStableFunc(sorted, func(a, b Message) int { return a.Timestamp.Compare(b.Timestamp) })
	last := ""
	for _, message := range sorted {
		label := dates.DateGroupLabel(message.Timestamp)
		if label != last {
			if color {
				*lines = append(*lines, dim("  ── "+label+" ──"), "")
			} else {
				*lines = append(*lines, "  -- "+label+" --", "")
			}
			last = label
		}
		appendMessage(message)
	}
}

func formatColorMessage(message Message, dates DateFormatter, relative bool, indent string) string {
	timeText := dates.FormatTime(message.Timestamp)
	if relative {
		timeText = dates.FormatRelativeTime(message.Timestamp)
	}
	markers := messageMarkers(message)
	header := indent + dim(timeText) + " " + bold(userColor(message.User))
	if markers != "" {
		header += " " + dim(markers)
	}
	header += " " + dim(compactPostRef(message))
	textIndent := indent + "  "
	lines := []string{header, indentLines(message.Text, textIndent), dim(textIndent + formatStateTimes(message))}
	if len(message.FileDetails) > 0 {
		lines = append(lines, dim(textIndent+"📎 "+formatFiles(message)))
	}
	appendRichContent(&lines, message, textIndent, dim)
	lines = append(lines, "")
	for _, reply := range message.Replies {
		lines = append(lines, formatColorMessage(reply, dates, relative, indent+"  ↳ "))
	}
	return strings.Join(lines, "\n")
}

func appendPlainMessage(lines *[]string, message Message, dates DateFormatter, relative bool, indent string) {
	timeText := dates.FormatTime(message.Timestamp)
	if relative {
		timeText = dates.FormatRelativeTime(message.Timestamp)
	}
	markers := messageMarkers(message)
	header := fmt.Sprintf("%s[%s] %s", indent, timeText, message.User)
	if markers != "" {
		header += " " + markers
	}
	header += " " + compactPostRef(message)
	textIndent := indent + "  "
	*lines = append(*lines, header, indentLines(message.Text, textIndent), textIndent+formatStateTimes(message))
	if len(message.FileDetails) > 0 {
		*lines = append(*lines, textIndent+"Files: "+formatFiles(message))
	}
	appendRichContent(lines, message, textIndent, func(value string) string { return value })
	*lines = append(*lines, "")
	for _, reply := range message.Replies {
		appendPlainMessage(lines, reply, dates, relative, indent+"  > ")
	}
}

func colorHeader(channel Channel) string {
	switch channel.Type {
	case "unknown":
		return "⚠ Unknown channel (" + cyan(channel.ID) + ")"
	case "dm":
		return "💬 DMs with " + cyan(channel.Name)
	case "group":
		return "💬 Group DM: " + cyan(channel.Name)
	default:
		display := ""
		if channel.DisplayName != "" {
			display = " (" + channel.DisplayName + ")"
		}
		return "📢 " + cyan("#"+channel.Name) + display
	}
}

func plainHeader(channel Channel) string {
	switch channel.Type {
	case "unknown":
		return "Unknown channel (" + channel.ID + ")"
	case "dm":
		return "DMs with " + channel.Name
	case "group":
		return "Group DM: " + channel.Name
	default:
		display := ""
		if channel.DisplayName != "" {
			display = " (" + channel.DisplayName + ")"
		}
		return "#" + channel.Name + display
	}
}

func compactPostRef(message Message) string {
	id := message.ID
	if len(id) > 8 {
		id = id[:8]
	}
	return id + " " + message.Permalink
}

func messageMarkers(message Message) string {
	markers := make([]string, 0, 3)
	if message.IsDeleted {
		markers = append(markers, "[deleted]")
	} else if message.EditedAt != nil {
		markers = append(markers, "[edited]")
	}
	if message.IsSystem {
		if message.PostType != "" {
			markers = append(markers, "[system:"+message.PostType+"]")
		} else {
			markers = append(markers, "[system]")
		}
	}
	if message.IsPinned {
		markers = append(markers, "[pinned]")
	}
	return strings.Join(markers, " ")
}

func formatFiles(message Message) string {
	files := make([]string, 0, len(message.FileDetails))
	for _, file := range message.FileDetails {
		label := file.ID
		if file.Name != "" {
			label = file.Name + " (" + file.ID + ")"
		}
		details := make([]string, 0, 3)
		if file.MIME != "" {
			details = append(details, file.MIME)
		}
		if file.Extension != "" {
			details = append(details, file.Extension)
		}
		if file.Size != nil {
			details = append(details, fmt.Sprintf("%d B", *file.Size))
		}
		if len(details) > 0 {
			label += ", " + strings.Join(details, ", ")
		}
		files = append(files, label)
	}
	return strings.Join(files, ", ")
}

func formatStateTimes(message Message) string {
	values := []string{"Updated " + isoTime(message.UpdatedAt)}
	if message.EditedAt != nil {
		values = append(values, "edited "+isoTime(*message.EditedAt))
	}
	if message.DeletedAt != nil {
		values = append(values, "deleted "+isoTime(*message.DeletedAt))
	}
	return strings.Join(values, "; ")
}

func isoTime(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000Z")
}

func appendRichContent(lines *[]string, message Message, indent string, decorate func(string) string) {
	for _, attachment := range message.Attachments {
		if attachment.Pretext != "" {
			*lines = append(*lines, decorate(indent+attachment.Pretext))
		}
		if attachment.Title != "" {
			*lines = append(*lines, decorate(indent+"Attachment: "+attachment.Title))
		}
		if attachment.TitleLink != "" {
			*lines = append(*lines, decorate(indent+"  Link: "+attachment.TitleLink))
		}
		if attachment.AuthorName != "" {
			*lines = append(*lines, decorate(indent+"  By: "+attachment.AuthorName))
		}
		if attachment.Text != "" {
			*lines = append(*lines, decorate(indent+"  "+attachment.Text))
		}
		for _, field := range attachment.Fields {
			prefix := ""
			if field.Title != "" {
				prefix = field.Title + ": "
			}
			*lines = append(*lines, decorate(indent+"  "+prefix+field.Value))
		}
		if attachment.Footer != "" {
			*lines = append(*lines, decorate(indent+"  "+attachment.Footer))
		}
		if attachment.Fallback != "" {
			*lines = append(*lines, decorate(indent+"  Fallback: "+attachment.Fallback))
		}
		if attachment.Color != "" {
			*lines = append(*lines, decorate(indent+"  Color: "+attachment.Color))
		}
		if attachment.Timestamp != "" {
			*lines = append(*lines, decorate(indent+"  Timestamp: "+attachment.Timestamp))
		}
		for _, value := range []string{attachment.AuthorLink, attachment.AuthorIcon, attachment.FooterIcon, attachment.ImageURL, attachment.ThumbURL} {
			if value != "" {
				*lines = append(*lines, decorate(indent+"  "+value))
			}
		}
	}
	if len(message.Reactions) > 0 {
		reactions := make([]string, 0, len(message.Reactions))
		for _, reaction := range message.Reactions {
			names := make([]string, 0, len(reaction.Actors))
			for _, actor := range reaction.Actors {
				if actor.Username != "" {
					names = append(names, actor.Username)
				} else {
					names = append(names, actor.ID)
				}
			}
			value := fmt.Sprintf(":%s: %d", reaction.Emoji, reaction.Count)
			if len(names) > 0 {
				value += " (" + strings.Join(names, ", ") + ")"
			}
			reactions = append(reactions, value)
		}
		*lines = append(*lines, decorate(indent+"Reactions: "+strings.Join(reactions, "  ")))
	}
}

func appendCoverage(lines *[]string, output MessageOutput) {
	state := "completeness unknown"
	if output.Retrieval.Selection.QueryTruncated != nil {
		if *output.Retrieval.Selection.QueryTruncated {
			state = "truncated"
		} else {
			state = "complete"
		}
	}
	*lines = append(*lines, fmt.Sprintf("  Coverage: %d selected, %d visible; query %s", output.Retrieval.Selection.SelectedCount, output.Retrieval.VisiblePostCount, state))
	if output.Retrieval.Selection.NextCursor != nil && *output.Retrieval.Selection.NextCursor != "" {
		*lines = append(*lines, "  Next cursor: "+*output.Retrieval.Selection.NextCursor)
	}
}

func indentLines(value, indent string) string {
	return indent + strings.ReplaceAll(value, "\n", "\n"+indent)
}

func bold(value string) string { return ansiBold + value + ansiReset }
func dim(value string) string  { return ansiDim + value + ansiReset }
func cyan(value string) string { return ansiCyan + value + ansiReset }

func userColor(value string) string {
	colors := [...]string{ansiCyan, "\x1b[33m", "\x1b[32m", "\x1b[35m", "\x1b[34m"}
	var hash int32
	for _, r := range value {
		unit := uint16(r)
		if r > 0xFFFF {
			high, _ := utf16.EncodeRune(r)
			unit = uint16(high)
		}
		hash = hash*31 + int32(unit)
	}
	index := int64(hash)
	if index < 0 {
		index = -index
	}
	return colors[index%int64(len(colors))] + value + ansiReset
}
