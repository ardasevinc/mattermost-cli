package output

import (
	"fmt"
	"io"
	"net/url"
	"slices"
	"strings"
)

type MarkdownOptions struct {
	Relative bool
}

// FormatMarkdown renders already-presented values without further sanitizing them.
func FormatMarkdown(outputs []MessageOutput, dates DateFormatter, options MarkdownOptions) string {
	sections := make([]string, 0, len(outputs))
	for _, output := range outputs {
		sections = append(sections, formatMarkdownSection(output, dates, options.Relative))
	}
	return strings.Join(sections, "\n\n---\n\n")
}

// WriteMarkdown renders and writes exactly once. It never retries an uncertain write.
func WriteMarkdown(w io.Writer, outputs []MessageOutput, dates DateFormatter, options MarkdownOptions) (int, error) {
	formatted := FormatMarkdown(outputs, dates, options)
	n, err := io.WriteString(w, formatted)
	if err == nil && n != len(formatted) {
		err = io.ErrShortWrite
	}
	return n, err
}

func formatMarkdownSection(output MessageOutput, dates DateFormatter, relative bool) string {
	lines := []string{markdownChannelHeader(output.Channel), ""}
	messages := slices.Clone(output.Messages)
	slices.SortStableFunc(messages, func(a, b Message) int { return a.Timestamp.Compare(b.Timestamp) })
	lastDate := ""
	for _, message := range messages {
		date := dates.FormatDateLong(message.Timestamp)
		if date != lastDate {
			lines = append(lines, "### "+date, "")
			lastDate = date
		}
		lines = append(lines, formatMarkdownMessage(message, dates, relative, 0), "")
	}
	if len(output.Redactions) > 0 {
		lines = append(lines, "", fmt.Sprintf("_%d secret(s) redacted_", len(output.Redactions)))
	}
	state := "completeness unknown"
	if output.Retrieval.Selection.QueryTruncated != nil {
		if *output.Retrieval.Selection.QueryTruncated {
			state = "truncated"
		} else {
			state = "complete"
		}
	}
	lines = append(lines, "", fmt.Sprintf("_Coverage: %d selected, %d visible; query %s_", output.Retrieval.Selection.SelectedCount, output.Retrieval.VisiblePostCount, state))
	if cursor := output.Retrieval.Selection.NextCursor; cursor != nil && *cursor != "" {
		lines = append(lines, "Next cursor: `"+*cursor+"`")
	}
	return strings.Join(lines, "\n")
}

func markdownChannelHeader(channel Channel) string {
	switch channel.Type {
	case "unknown":
		return "## Unknown channel (" + escapeMarkdown(channel.ID) + ")"
	case "dm":
		return "## DMs with " + escapeMarkdown(channel.Name)
	case "group":
		return "## Group DM: " + escapeMarkdown(channel.Name)
	default:
		display := ""
		if channel.DisplayName != "" {
			display = " (" + escapeMarkdown(channel.DisplayName) + ")"
		}
		return "## #" + escapeMarkdown(channel.Name) + display
	}
}

func formatMarkdownMessage(message Message, dates DateFormatter, relative bool, depth int) string {
	timeText := dates.FormatTime(message.Timestamp)
	if relative {
		timeText = dates.FormatRelativeTime(message.Timestamp)
	}
	prefix := strings.Repeat("> ", depth)
	postID := escapeMarkdown(message.ID)
	postRef := postID
	if permalink, ok := safeHTTPURL(message.Permalink); ok {
		postRef = "[" + postID + "](<" + permalink + ">)"
	}
	markers := markdownMessageMarkers(message)
	header := prefix + "**" + escapeMarkdown(message.User) + "**"
	if markers != "" {
		header += " " + markers
	}
	header += " (" + timeText + ", " + postRef + "):"
	quotePrefix := prefix + "> "
	lines := []string{header, markdownQuoteLines(escapeMarkdown(message.Text), quotePrefix)}
	lines = append(lines, quotePrefix+"_"+formatStateTimes(message)+"_")
	if len(message.FileDetails) > 0 {
		lines = append(lines, quotePrefix+"_Files: "+escapeMarkdown(formatFiles(message))+"_")
	}
	for _, attachment := range message.Attachments {
		title := attachment.Title
		if title == "" {
			title = "Attachment"
		}
		if attachment.Pretext != "" {
			lines = append(lines, markdownQuoteLines(escapeMarkdown(attachment.Pretext), quotePrefix))
		}
		var titleLine string
		if titleLink, ok := safeHTTPURL(attachment.TitleLink); ok {
			titleLine = "**[" + escapeMarkdown(title) + "](<" + titleLink + ">)**"
		} else {
			titleLine = "**" + escapeMarkdown(title) + "**"
		}
		lines = append(lines, markdownQuoteLines(titleLine, quotePrefix))
		if attachment.AuthorName != "" {
			lines = append(lines, markdownQuoteLines("By: "+escapeMarkdown(attachment.AuthorName), quotePrefix))
		}
		if attachment.Text != "" {
			lines = append(lines, markdownQuoteLines(escapeMarkdown(attachment.Text), quotePrefix))
		}
		for _, field := range attachment.Fields {
			value := escapeMarkdown(field.Value)
			if field.Title != "" {
				value = "**" + escapeMarkdown(field.Title) + ":** " + value
			}
			lines = append(lines, markdownQuoteLines(value, quotePrefix))
		}
		if attachment.Fallback != "" {
			lines = append(lines, markdownQuoteLines("Fallback: "+escapeMarkdown(attachment.Fallback), quotePrefix))
		}
		if attachment.Footer != "" {
			lines = append(lines, markdownQuoteLines("_"+escapeMarkdown(attachment.Footer)+"_", quotePrefix))
		}
		if attachment.Color != "" {
			lines = append(lines, quotePrefix+"Color: "+escapeMarkdown(attachment.Color))
		}
		if attachment.Timestamp != "" {
			lines = append(lines, quotePrefix+"Timestamp: "+escapeMarkdown(attachment.Timestamp))
		}
		for _, candidate := range []string{attachment.AuthorLink, attachment.AuthorIcon, attachment.FooterIcon, attachment.ImageURL, attachment.ThumbURL} {
			if safe, ok := safeHTTPURL(candidate); ok {
				lines = append(lines, quotePrefix+"<"+safe+">")
			}
		}
	}
	if len(message.Reactions) > 0 {
		reactions := make([]string, 0, len(message.Reactions))
		for _, reaction := range message.Reactions {
			actors := make([]string, 0, len(reaction.Actors))
			for _, actor := range reaction.Actors {
				name := actor.Username
				if name == "" {
					name = actor.ID
				}
				actors = append(actors, escapeMarkdown(name))
			}
			value := fmt.Sprintf(":%s: %d", escapeMarkdown(reaction.Emoji), reaction.Count)
			if len(actors) > 0 {
				value += " (" + strings.Join(actors, ", ") + ")"
			}
			reactions = append(reactions, value)
		}
		lines = append(lines, quotePrefix+"_Reactions: "+strings.Join(reactions, " · ")+"_")
	}
	if len(message.Replies) > 0 {
		lines = append(lines, "")
		for _, reply := range message.Replies {
			lines = append(lines, formatMarkdownMessage(reply, dates, relative, depth+1), "")
		}
	}
	return strings.Join(lines, "\n")
}

func markdownMessageMarkers(message Message) string {
	markers := make([]string, 0, 3)
	if message.IsDeleted {
		markers = append(markers, "[deleted]")
	} else if message.EditedAt != nil {
		markers = append(markers, "[edited]")
	}
	if message.IsSystem {
		if message.PostType != "" {
			markers = append(markers, "[system:"+escapeMarkdown(message.PostType)+"]")
		} else {
			markers = append(markers, "[system]")
		}
	}
	if message.IsPinned {
		markers = append(markers, "[pinned]")
	}
	return strings.Join(markers, " ")
}

var markdownEscaper = strings.NewReplacer(
	"\\", "\\\\", "`", "\\`", "*", "\\*", "_", "\\_", "[", "\\[", "]", "\\]",
	"{", "\\{", "}", "\\}", "(", "\\(", ")", "\\)", "<", "\\<", ">", "\\>",
	"#", "\\#", "+", "\\+", "-", "\\-", ".", "\\.", "!", "\\!", "|", "\\|", "~", "\\~",
)

func escapeMarkdown(value string) string { return markdownEscaper.Replace(value) }

func markdownQuoteLines(value, prefix string) string {
	return prefix + strings.ReplaceAll(value, "\n", "\n"+prefix)
}

func safeHTTPURL(value string) (string, bool) {
	if value == "" {
		return "", false
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", false
	}
	replacer := strings.NewReplacer("<", "%3C", ">", "%3E", "\\", "%5C")
	return replacer.Replace(value), true
}
