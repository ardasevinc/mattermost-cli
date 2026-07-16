// Package normalization converts validated Mattermost data into safe output models.
package normalization

import (
	"sort"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/ardasevinc/mattermost-cli/internal/mattermost"
	"github.com/ardasevinc/mattermost-cli/internal/output"
	"github.com/ardasevinc/mattermost-cli/internal/presentation"
	"github.com/ardasevinc/mattermost-cli/internal/serverurl"
)

const deletedPostText = "[deleted post]"

// PostUserIDs returns unique author and reaction actor IDs in encounter order.
func PostUserIDs(posts []mattermost.Post) []string {
	seen := make(map[string]struct{})
	ids := make([]string, 0)
	add := func(id string) {
		if id == "" {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	for _, post := range posts {
		add(post.UserID)
		for _, reaction := range post.Reactions {
			add(reaction.UserID)
		}
	}
	return ids
}

// NormalizePosts safely prepares validated posts for presentation.
func NormalizePosts(posts []mattermost.Post, users map[string]mattermost.User, myUserID, serverURL string, options presentation.Options) ([]output.Message, []output.Redaction, error) {
	redactions := make([]output.Redaction, 0)
	clean := func(value, field string, label bool) string {
		result := presentation.PreprocessWithOptions(value, options)
		if label {
			remapLabelRedactionPositions(result.Text, result.Redactions)
			result.Text = presentation.SanitizeLabel(result.Text)
		}
		for _, item := range result.Redactions {
			item.Field = field
			redactions = append(redactions, item)
		}
		return result.Text
	}
	messages := make([]output.Message, 0, len(posts))
	for _, post := range posts {
		permalink, err := serverurl.BuildPostPermalink(serverURL, post.ID)
		if err != nil {
			return nil, nil, err
		}
		deleted := post.DeleteAt > 0
		username := post.UserID
		if user, ok := users[post.UserID]; ok {
			username = user.Username
		}
		displayUser := ""
		switch {
		case post.OverrideUsername != "":
			displayUser = clean(post.OverrideUsername, "user", true)
		case post.UserID == "" || strings.HasPrefix(post.Type, "system_"):
			displayUser = "system"
		case post.UserID == myUserID:
			displayUser = "you"
		default:
			displayUser = clean(username, "user", true)
		}

		files, details := normalizeFiles(post, deleted, clean)
		attachments := normalizeAttachments(post, deleted, clean)
		reactions := normalizeReactions(post, users, deleted, clean)
		messageText := deletedPostText
		if !deleted {
			messageText = clean(post.Message, "post.message", false)
		}
		message := output.Message{
			ID: clean(post.ID, "post.id", true), Permalink: clean(permalink, "post.permalink", true),
			User: displayUser, UserID: clean(post.UserID, "post.userId", true), Text: messageText,
			Timestamp: time.UnixMilli(post.CreateAt).UTC(), UpdatedAt: time.UnixMilli(post.UpdateAt).UTC(),
			IsDeleted: deleted, PostType: clean(post.Type, "post.type", true),
			IsSystem: post.UserID == "" || strings.HasPrefix(post.Type, "system_"), IsPinned: post.IsPinned,
			Files: files, FileDetails: details, Attachments: attachments, Reactions: reactions,
			CanonicalID: post.ID, CanonicalRootID: post.RootID,
		}
		shapeKnown := post.ThreadShapeKnown
		message.CanonicalThreadShapeKnown = &shapeKnown
		if post.EditAt > 0 {
			value := time.UnixMilli(post.EditAt).UTC()
			message.EditedAt = &value
		}
		if post.DeleteAt > 0 {
			value := time.UnixMilli(post.DeleteAt).UTC()
			message.DeletedAt = &value
		}
		if post.RootID != "" {
			message.RootID = clean(post.RootID, "post.rootId", true)
		}
		if post.ReplyCount > 0 {
			value := post.ReplyCount
			message.ReplyCount = &value
		}
		messages = append(messages, message)
	}
	return messages, redactions, nil
}

type cleaner func(string, string, bool) string

func normalizeFiles(post mattermost.Post, deleted bool, clean cleaner) ([]string, []output.File) {
	if deleted {
		return []string{}, []output.File{}
	}
	metadata := make(map[string]mattermost.PostFile, len(post.Files))
	for _, file := range post.Files {
		if file.ID != "" {
			metadata[file.ID] = file
		}
	}
	ids := make([]string, 0, len(post.FileIDs)+len(post.Files))
	seen := make(map[string]struct{})
	add := func(id string) {
		if id != "" {
			if _, ok := seen[id]; !ok {
				seen[id] = struct{}{}
				ids = append(ids, id)
			}
		}
	}
	for _, id := range post.FileIDs {
		add(id)
	}
	for _, file := range post.Files {
		add(file.ID)
	}
	visible := make([]string, 0, len(ids))
	details := make([]output.File, 0, len(ids))
	for _, id := range ids {
		visible = append(visible, clean(id, "file.id", true))
		source := metadata[id]
		item := output.File{ID: clean(id, "file.id", true)}
		if source.Name != "" {
			item.Name = clean(source.Name, "file.name", true)
		}
		if source.MIMEType != "" {
			item.MIME = clean(source.MIMEType, "file.mime", true)
		}
		if source.Size != nil {
			value := *source.Size
			item.Size = &value
		}
		if source.Extension != "" {
			item.Extension = clean(source.Extension, "file.extension", true)
		}
		details = append(details, item)
	}
	return visible, details
}

func normalizeAttachments(post mattermost.Post, deleted bool, clean cleaner) []output.Attachment {
	result := make([]output.Attachment, 0)
	if deleted {
		return result
	}
	for i, source := range post.Attachments {
		field := func(name string) string { return "attachment." + itoa(i) + "." + name }
		item := output.Attachment{
			Fallback: cleanOptional(source.Fallback, field("fallback"), false, clean), Pretext: cleanOptional(source.Pretext, field("pretext"), false, clean),
			Title: cleanOptional(source.Title, field("title"), false, clean), TitleLink: cleanOptional(source.TitleLink, field("title_link"), true, clean),
			Text: cleanOptional(source.Text, field("text"), false, clean), Footer: cleanOptional(source.Footer, field("footer"), false, clean),
			FooterIcon: cleanOptional(source.FooterIcon, field("footer_icon"), true, clean), AuthorName: cleanOptional(source.AuthorName, field("author_name"), false, clean),
			AuthorLink: cleanOptional(source.AuthorLink, field("author_link"), true, clean), AuthorIcon: cleanOptional(source.AuthorIcon, field("author_icon"), true, clean),
			Color: cleanOptional(source.Color, field("color"), true, clean), ImageURL: cleanOptional(source.ImageURL, field("image_url"), true, clean),
			ThumbURL: cleanOptional(source.ThumbURL, field("thumb_url"), true, clean), Timestamp: cleanOptional(source.Timestamp, field("ts"), true, clean),
		}
		for j, sourceField := range source.Fields {
			prefix := field("fields." + itoa(j))
			f := output.AttachmentField{Title: cleanOptional(sourceField.Title, prefix+".title", false, clean), Value: cleanOptional(sourceField.Value, prefix+".value", false, clean)}
			if sourceField.Short != nil {
				value := *sourceField.Short
				f.Short = &value
			}
			if f.Title != "" || f.Value != "" || f.Short != nil {
				item.Fields = append(item.Fields, f)
			}
		}
		result = append(result, item)
	}
	return result
}

func normalizeReactions(post mattermost.Post, users map[string]mattermost.User, deleted bool, clean cleaner) []output.Reaction {
	result := make([]output.Reaction, 0)
	if deleted {
		return result
	}
	groups := make(map[string][]mattermost.PostReaction)
	for _, reaction := range post.Reactions {
		if reaction.EmojiName != "" && reaction.UserID != "" {
			groups[reaction.EmojiName] = append(groups[reaction.EmojiName], reaction)
		}
	}
	emojis := make([]string, 0, len(groups))
	for emoji := range groups {
		emojis = append(emojis, emoji)
	}
	sort.Strings(emojis)
	for _, emoji := range emojis {
		entries := groups[emoji]
		sort.SliceStable(entries, func(i, j int) bool { return entries[i].UserID < entries[j].UserID })
		actors := make([]output.ReactionActor, 0, len(entries))
		for _, entry := range entries {
			actor := output.ReactionActor{ID: clean(entry.UserID, "reaction.actor.id", true)}
			if user, ok := users[entry.UserID]; ok && user.Username != "" {
				actor.Username = clean(user.Username, "reaction.actor.username", true)
			}
			actors = append(actors, actor)
		}
		result = append(result, output.Reaction{Emoji: clean(emoji, "reaction.emoji", true), Count: len(entries), Actors: actors})
	}
	return result
}

func cleanOptional(value, field string, label bool, clean cleaner) string {
	if value == "" {
		return ""
	}
	return clean(value, field, label)
}

func remapLabelRedactionPositions(text string, redactions []presentation.Redaction) {
	next := 0
	originalPosition := 0
	expansion := 0
	applyThrough := func(position int) {
		for next < len(redactions) && redactions[next].Position <= position {
			redactions[next].Position += expansion
			next++
		}
	}
	applyThrough(0)
	for _, character := range text {
		width := 1
		if character > 0xffff {
			width = 2
		}
		originalPosition += width
		if character == '\n' || character == '\t' {
			expansion++
		}
		applyThrough(originalPosition)
	}
	for next < len(redactions) {
		redactions[next].Position += expansion
		next++
	}
}

func utf16Length(value string) int { return len(utf16.Encode([]rune(value))) }

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	var digits [20]byte
	cursor := len(digits)
	for value > 0 {
		cursor--
		digits[cursor] = byte('0' + value%10)
		value /= 10
	}
	return string(digits[cursor:])
}
