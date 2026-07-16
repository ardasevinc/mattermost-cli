package mattermost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const MaxPostsPage = 200

const (
	maxPostIDLength     = 128
	maxDateMilliseconds = int64(8_640_000_000_000_000)
	maxPostTypeLength   = 26
	maxPostFileIDs      = 100
	maxPostReactions    = 10_000
	maxEmojiNameLength  = 64
	maxRemoteIDLength   = 256
)

var (
	ErrInvalidPostResponse      = errors.New("Mattermost returned an invalid post response")
	ErrInvalidPostsResponse     = errors.New("Mattermost returned an invalid posts response")
	ErrInvalidSearchResponse    = errors.New("Mattermost returned an invalid search response")
	ErrInvalidPostsRequest      = errors.New("invalid Mattermost posts request")
	ErrInvalidReactionsResponse = errors.New("Mattermost returned an invalid reactions response")
)

type postTransport interface {
	Get(context.Context, string, any) error
	PostRead(context.Context, string, any, any) error
}

type Post struct {
	ID               string
	ChannelID        string
	UserID           string
	Message          string
	CreateAt         int64
	UpdateAt         int64
	EditAt           int64
	DeleteAt         int64
	RootID           string
	ReplyCount       int
	ThreadShapeKnown bool
	Type             string
	IsPinned         bool
	OverrideUsername string
	FileIDs          []string
	Files            []PostFile
	Attachments      []PostAttachment
	Reactions        []PostReaction
}

type PostFile struct {
	ID        string
	Name      string
	MIMEType  string
	Size      *int64
	Extension string
}

type PostAttachmentField struct {
	Title string
	Value string
	Short *bool
}

type PostAttachment struct {
	Fallback   string
	Pretext    string
	Title      string
	TitleLink  string
	Text       string
	Fields     []PostAttachmentField
	Footer     string
	FooterIcon string
	AuthorName string
	AuthorLink string
	AuthorIcon string
	Color      string
	ImageURL   string
	ThumbURL   string
	Timestamp  string
}

type PostReaction struct {
	UserID    string
	PostID    string
	EmojiName string
	CreateAt  int64
}

func (p *Post) UnmarshalJSON(data []byte) error {
	var raw struct {
		ID         json.RawMessage `json:"id"`
		ChannelID  json.RawMessage `json:"channel_id"`
		UserID     json.RawMessage `json:"user_id"`
		Message    json.RawMessage `json:"message"`
		CreateAt   json.RawMessage `json:"create_at"`
		UpdateAt   json.RawMessage `json:"update_at"`
		EditAt     json.RawMessage `json:"edit_at"`
		DeleteAt   json.RawMessage `json:"delete_at"`
		RootID     json.RawMessage `json:"root_id"`
		ReplyCount json.RawMessage `json:"reply_count"`
		Type       json.RawMessage `json:"type"`
		IsPinned   json.RawMessage `json:"is_pinned"`
		Override   json.RawMessage `json:"override_username"`
		FileIDs    json.RawMessage `json:"file_ids"`
		Props      json.RawMessage `json:"props"`
		Metadata   json.RawMessage `json:"metadata"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return ErrInvalidPostResponse
	}
	id, idOK := safePostID(raw.ID)
	channelID, channelOK := requiredString(raw.ChannelID)
	createAt, createOK := nonnegativeInteger(raw.CreateAt)
	deleteAt, deleteOK := nonnegativeInteger(raw.DeleteAt)
	rootID, rootOK, rootKnown := optionalPostID(raw.RootID)
	replyCount, replyOK, replyKnown := optionalNonnegativeInteger(raw.ReplyCount)
	message, messageOK := strictString(raw.Message)
	if !idOK || !channelOK || (!messageOK && deleteAt == 0) || !createOK || createAt == 0 || createAt > maxDateMilliseconds || !deleteOK || deleteAt > maxDateMilliseconds || !rootOK || !replyOK {
		return ErrInvalidPostResponse
	}
	post := Post{
		ID: id, ChannelID: channelID, UserID: postOptionalString(raw.UserID),
		CreateAt: createAt, UpdateAt: optionalTimestamp(raw.UpdateAt, createAt), EditAt: optionalTimestamp(raw.EditAt, 0), DeleteAt: deleteAt,
		RootID: rootID, ReplyCount: replyCount, ThreadShapeKnown: rootKnown && replyKnown,
	}
	if deleteAt == 0 {
		post.Message = message
		post.Type = postOptionalString(raw.Type)
		post.IsPinned = optionalBool(raw.IsPinned)
		directOverride, directOverrideKnown := optionalStringValue(raw.Override)
		if directOverrideKnown {
			post.OverrideUsername = directOverride
		}
		post.FileIDs = stringArray(raw.FileIDs)
		post.Files, post.Reactions = parsePostMetadata(raw.Metadata)
		post.Attachments, post.OverrideUsername = parsePostProps(raw.Props, post.OverrideUsername, directOverrideKnown)
	}
	*p = post
	return nil
}

func postOptionalString(raw json.RawMessage) string {
	value, ok := optionalStringValue(raw)
	if !ok {
		return ""
	}
	return value
}

func optionalStringValue(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || isJSONNull(raw) {
		return "", false
	}
	return strictString(raw)
}

func optionalBool(raw json.RawMessage) bool {
	var value bool
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
		return false
	}
	return value
}

func optionalTimestamp(raw json.RawMessage, fallback int64) int64 {
	value, ok := nonnegativeInteger(raw)
	if !ok || value > maxDateMilliseconds {
		return fallback
	}
	return value
}

func stringArray(raw json.RawMessage) []string {
	var values []json.RawMessage
	if json.Unmarshal(raw, &values) != nil {
		return nil
	}
	result := make([]string, 0, len(values))
	for _, candidate := range values {
		if value := postOptionalString(candidate); value != "" {
			result = append(result, value)
		}
	}
	return result
}

func parsePostMetadata(raw json.RawMessage) ([]PostFile, []PostReaction) {
	var metadata map[string]json.RawMessage
	if json.Unmarshal(raw, &metadata) != nil {
		return nil, nil
	}
	var rawFiles []json.RawMessage
	_ = json.Unmarshal(metadata["files"], &rawFiles)
	files := make([]PostFile, 0, len(rawFiles))
	for _, candidate := range rawFiles {
		var file struct {
			ID        json.RawMessage `json:"id"`
			Name      json.RawMessage `json:"name"`
			MIMEType  json.RawMessage `json:"mime_type"`
			Size      json.RawMessage `json:"size"`
			Extension json.RawMessage `json:"extension"`
		}
		if json.Unmarshal(candidate, &file) != nil || postOptionalString(file.ID) == "" {
			continue
		}
		value := PostFile{ID: postOptionalString(file.ID), Name: postOptionalString(file.Name), MIMEType: postOptionalString(file.MIMEType), Extension: postOptionalString(file.Extension)}
		if size, ok := nonnegativeInteger(file.Size); ok {
			value.Size = &size
		}
		files = append(files, value)
	}
	var rawReactions []json.RawMessage
	_ = json.Unmarshal(metadata["reactions"], &rawReactions)
	reactions := make([]PostReaction, 0, len(rawReactions))
	for _, candidate := range rawReactions {
		var reaction struct {
			UserID    json.RawMessage `json:"user_id"`
			PostID    json.RawMessage `json:"post_id"`
			EmojiName json.RawMessage `json:"emoji_name"`
			CreateAt  json.RawMessage `json:"create_at"`
		}
		if json.Unmarshal(candidate, &reaction) != nil {
			continue
		}
		value := PostReaction{UserID: postOptionalString(reaction.UserID), PostID: postOptionalString(reaction.PostID), EmojiName: postOptionalString(reaction.EmojiName), CreateAt: optionalTimestamp(reaction.CreateAt, 0)}
		if value.UserID != "" && value.EmojiName != "" {
			reactions = append(reactions, value)
		}
	}
	return files, reactions
}

func parsePostProps(raw json.RawMessage, override string, overrideKnown bool) ([]PostAttachment, string) {
	var props map[string]json.RawMessage
	if json.Unmarshal(raw, &props) != nil {
		return nil, override
	}
	if !overrideKnown {
		if propsOverride, ok := optionalStringValue(props["override_username"]); ok {
			override = propsOverride
		}
	}
	var rawAttachments []json.RawMessage
	_ = json.Unmarshal(props["attachments"], &rawAttachments)
	attachments := make([]PostAttachment, 0, len(rawAttachments))
	for _, candidate := range rawAttachments {
		var source map[string]json.RawMessage
		if json.Unmarshal(candidate, &source) != nil || source == nil {
			continue
		}
		attachment := PostAttachment{
			Fallback: postOptionalString(source["fallback"]), Pretext: postOptionalString(source["pretext"]), Title: postOptionalString(source["title"]),
			TitleLink: postOptionalString(source["title_link"]), Text: postOptionalString(source["text"]), Footer: postOptionalString(source["footer"]),
			FooterIcon: postOptionalString(source["footer_icon"]), AuthorName: postOptionalString(source["author_name"]), AuthorLink: postOptionalString(source["author_link"]),
			AuthorIcon: postOptionalString(source["author_icon"]), Color: postOptionalString(source["color"]), ImageURL: postOptionalString(source["image_url"]),
			ThumbURL: postOptionalString(source["thumb_url"]), Timestamp: stringOrNumber(source["ts"]),
		}
		var fields []json.RawMessage
		if json.Unmarshal(source["fields"], &fields) == nil {
			for _, rawField := range fields {
				var fieldSource map[string]json.RawMessage
				if json.Unmarshal(rawField, &fieldSource) != nil || fieldSource == nil {
					continue
				}
				field := PostAttachmentField{Title: stringOrNumber(fieldSource["title"]), Value: stringOrNumber(fieldSource["value"])}
				var short bool
				if rawShort := fieldSource["short"]; len(rawShort) > 0 && !isJSONNull(rawShort) && json.Unmarshal(rawShort, &short) == nil {
					field.Short = &short
				}
				if field.Title != "" || field.Value != "" || field.Short != nil {
					attachment.Fields = append(attachment.Fields, field)
				}
			}
		}
		if postAttachmentHasContent(attachment) {
			attachments = append(attachments, attachment)
		}
	}
	return attachments, override
}

func postAttachmentHasContent(value PostAttachment) bool {
	return value.Fallback != "" || value.Pretext != "" || value.Title != "" || value.TitleLink != "" || value.Text != "" ||
		len(value.Fields) > 0 || value.Footer != "" || value.FooterIcon != "" || value.AuthorName != "" || value.AuthorLink != "" ||
		value.AuthorIcon != "" || value.Color != "" || value.ImageURL != "" || value.ThumbURL != "" || value.Timestamp != ""
}

func stringOrNumber(raw json.RawMessage) string {
	if value := postOptionalString(raw); value != "" {
		return value
	}
	var number float64
	if len(raw) > 0 && !isJSONNull(raw) && json.Unmarshal(raw, &number) == nil {
		return ecmaScriptNumberString(number)
	}
	return ""
}

func ecmaScriptNumberString(value float64) string {
	if value == 0 {
		return "0"
	}
	abs := value
	if abs < 0 {
		abs = -abs
	}
	if abs >= 1e-6 && abs < 1e21 {
		return strconv.FormatFloat(value, 'f', -1, 64)
	}
	formatted := strconv.FormatFloat(value, 'e', -1, 64)
	parts := strings.SplitN(formatted, "e", 2)
	exponent, _ := strconv.Atoi(parts[1])
	if exponent >= 0 {
		return parts[0] + "e+" + strconv.Itoa(exponent)
	}
	return parts[0] + "e" + strconv.Itoa(exponent)
}

func optionalPostID(raw json.RawMessage) (string, bool, bool) {
	if len(raw) == 0 {
		return "", true, false
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return "", false, true
	}
	if value == "" {
		return "", true, true
	}
	value, ok := safePostID(raw)
	return value, ok, true
}

func optionalNonnegativeInteger(raw json.RawMessage) (int, bool, bool) {
	if len(raw) == 0 {
		return 0, true, false
	}
	value, ok := nonnegativeInteger(raw)
	if !ok || value > int64(^uint(0)>>1) {
		return 0, false, true
	}
	return int(value), true, true
}

func safePostID(raw json.RawMessage) (string, bool) {
	value, ok := requiredString(raw)
	if !ok || !isSafePostID(value) {
		return "", false
	}
	return value, true
}

func isSafePostID(value string) bool {
	if len(value) == 0 || len(value) > maxPostIDLength {
		return false
	}
	for i := range len(value) {
		c := value[i]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func nonnegativeInteger(raw json.RawMessage) (int64, bool) {
	var value int64
	if len(raw) == 0 || isJSONNull(raw) || json.Unmarshal(raw, &value) != nil || value < 0 {
		return 0, false
	}
	return value, true
}

func isJSONNull(raw json.RawMessage) bool {
	return string(bytes.TrimSpace(raw)) == "null"
}

// OrderedPostsPage preserves the server's order and raw fullness separately
// from its validated, visible posts.
type OrderedPostsPage struct {
	Posts                     []Post
	RawCount                  int
	HasNext                   *bool
	FirstInaccessiblePostTime *int64
	Incomplete                bool
	Continuation              *ThreadCursor
	ThreadRootID              string
	ThreadChannelID           string
	ContainsRequestedPost     bool
	bindings                  []postBinding
}

type ThreadCursor struct {
	PostID   string
	CreateAt int64
}

type postBinding struct {
	ID               string
	ChannelID        string
	RootID           string
	ThreadShapeKnown bool
}

func (p *OrderedPostsPage) UnmarshalJSON(data []byte) error {
	var raw struct {
		Order                     json.RawMessage            `json:"order"`
		Posts                     map[string]json.RawMessage `json:"posts"`
		HasNext                   json.RawMessage            `json:"has_next"`
		FirstInaccessiblePostTime json.RawMessage            `json:"first_inaccessible_post_time"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return ErrInvalidPostsResponse
	}
	var order []json.RawMessage
	if len(raw.Order) == 0 || json.Unmarshal(raw.Order, &order) != nil || order == nil {
		return ErrInvalidPostsResponse
	}

	result := OrderedPostsPage{RawCount: len(order)}
	if raw.Posts == nil && len(order) > 0 {
		result.Incomplete = true
	}
	for _, rawID := range order {
		var id string
		if json.Unmarshal(rawID, &id) != nil || strings.TrimSpace(id) == "" {
			result.Incomplete = true
			continue
		}
		candidate, ok := raw.Posts[id]
		if !ok {
			result.Incomplete = true
			continue
		}
		var post Post
		if json.Unmarshal(candidate, &post) != nil || post.ID != id {
			result.Incomplete = true
			continue
		}
		result.bindings = append(result.bindings, postBinding{ID: post.ID, ChannelID: post.ChannelID, RootID: post.RootID, ThreadShapeKnown: post.ThreadShapeKnown})
		result.Continuation = &ThreadCursor{PostID: post.ID, CreateAt: post.CreateAt}
		if post.DeleteAt == 0 {
			result.Posts = append(result.Posts, post)
		}
	}
	if len(raw.HasNext) > 0 {
		var hasNext bool
		if string(raw.HasNext) == "null" || json.Unmarshal(raw.HasNext, &hasNext) != nil {
			result.Incomplete = true
		} else {
			result.HasNext = &hasNext
		}
	}
	if len(raw.FirstInaccessiblePostTime) > 0 && string(raw.FirstInaccessiblePostTime) != "null" {
		value, ok := nonnegativeInteger(raw.FirstInaccessiblePostTime)
		if !ok {
			result.Incomplete = true
		} else if value > 0 {
			result.FirstInaccessiblePostTime = &value
		}
	}
	*p = result
	return nil
}

type ChannelPostsOptions struct {
	PerPage int
	Page    int
	Before  string
}

type ThreadPageOptions struct {
	PerPage      int
	FromPost     string
	FromCreateAt *int64
}

const MaxSearchPage = 100

type SearchPageOptions struct {
	Terms   string
	Page    int
	PerPage int
}

type SearchPage struct {
	Posts                     []Post
	OrderedIDs                []string
	Matches                   map[string][]string
	RawCount                  int
	HasNext                   *bool
	FirstInaccessiblePostTime *int64
	Incomplete                bool
}

func (p *SearchPage) UnmarshalJSON(data []byte) error {
	var raw struct {
		Order                     json.RawMessage            `json:"order"`
		Posts                     map[string]json.RawMessage `json:"posts"`
		Matches                   map[string]json.RawMessage `json:"matches"`
		HasNext                   json.RawMessage            `json:"has_next"`
		FirstInaccessiblePostTime json.RawMessage            `json:"first_inaccessible_post_time"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return ErrInvalidSearchResponse
	}
	var order []json.RawMessage
	if len(raw.Order) == 0 || json.Unmarshal(raw.Order, &order) != nil || order == nil {
		return ErrInvalidSearchResponse
	}
	result := SearchPage{RawCount: len(order), Matches: make(map[string][]string)}
	seen := make(map[string]struct{})
	if raw.Posts == nil && len(order) > 0 {
		result.Incomplete = true
	}
	for _, rawID := range order {
		var id string
		if json.Unmarshal(rawID, &id) != nil || !isSafePostID(id) {
			result.Incomplete = true
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		result.OrderedIDs = append(result.OrderedIDs, id)
		candidate, ok := raw.Posts[id]
		if !ok {
			result.Incomplete = true
			continue
		}
		var post Post
		if json.Unmarshal(candidate, &post) != nil || post.ID != id {
			result.Incomplete = true
			continue
		}
		if post.DeleteAt != 0 {
			continue
		}
		result.Posts = append(result.Posts, post)
		if value, exists := raw.Matches[id]; exists {
			var rawMatches []json.RawMessage
			if json.Unmarshal(value, &rawMatches) == nil && rawMatches != nil {
				matches := make([]string, 0, len(rawMatches))
				for _, rawMatch := range rawMatches {
					var match string
					if json.Unmarshal(rawMatch, &match) == nil {
						matches = append(matches, match)
					}
				}
				result.Matches[id] = matches
			}
		}
	}
	if len(raw.HasNext) > 0 {
		var hasNext bool
		if string(raw.HasNext) == "null" || json.Unmarshal(raw.HasNext, &hasNext) != nil {
			result.Incomplete = true
		} else {
			result.HasNext = &hasNext
		}
	}
	if len(raw.FirstInaccessiblePostTime) > 0 && string(raw.FirstInaccessiblePostTime) != "null" {
		value, ok := nonnegativeInteger(raw.FirstInaccessiblePostTime)
		if !ok {
			result.Incomplete = true
		} else if value > 0 {
			result.FirstInaccessiblePostTime = &value
		}
	}
	*p = result
	return nil
}

type Posts struct{ client postTransport }

func NewPosts(client postTransport) *Posts { return &Posts{client: client} }

type canonicalSinglePost struct{ Post Post }

func (p *canonicalSinglePost) UnmarshalJSON(data []byte) error {
	raw, ok := uniqueJSONObject(data)
	if !ok {
		return ErrInvalidPostResponse
	}
	id, idOK := safePostID(raw["id"])
	channelID, channelOK := safePostID(raw["channel_id"])
	userID, userOK := safePostID(raw["user_id"])
	message, messageOK := strictString(raw["message"])
	createAt, createOK := nonnegativeInteger(raw["create_at"])
	updateAt, updateOK := nonnegativeInteger(raw["update_at"])
	deleteAt, deleteOK := nonnegativeInteger(raw["delete_at"])
	rootID, rootOK := strictString(raw["root_id"])
	rootShapeOK := rootOK && (rootID == "" || isSafePostID(rootID))
	postType, typeOK := strictString(raw["type"])
	fileIDs, fileIDsOK := canonicalPostIDs(raw["file_ids"], maxPostFileIDs)
	if !idOK || !channelOK || !userOK || !messageOK || !createOK || createAt == 0 || createAt > maxDateMilliseconds ||
		!updateOK || updateAt == 0 || updateAt > maxDateMilliseconds || !deleteOK || deleteAt != 0 || !rootShapeOK ||
		!typeOK || !safePresentationString(postType, maxPostTypeLength) || !fileIDsOK {
		return ErrInvalidPostResponse
	}
	p.Post = Post{
		ID: id, ChannelID: channelID, UserID: userID, Message: message,
		CreateAt: createAt, UpdateAt: updateAt, DeleteAt: deleteAt,
		RootID: rootID, Type: postType, FileIDs: fileIDs,
	}
	return nil
}

func uniqueJSONObject(data []byte) (map[string]json.RawMessage, bool) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, false
	}
	fields := make(map[string]json.RawMessage)
	for decoder.More() {
		token, err = decoder.Token()
		name, ok := token.(string)
		if err != nil || !ok {
			return nil, false
		}
		if _, duplicate := fields[name]; duplicate {
			return nil, false
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return nil, false
		}
		fields[name] = value
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return nil, false
	}
	var trailing any
	if err = decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, false
	}
	return fields, true
}

func canonicalPostIDs(raw json.RawMessage, maximum int) ([]string, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	if isJSONNull(raw) {
		return []string{}, true
	}
	var values []json.RawMessage
	if json.Unmarshal(raw, &values) != nil || len(values) > maximum {
		return nil, false
	}
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, candidate := range values {
		value, ok := safePostID(candidate)
		if !ok {
			return nil, false
		}
		if _, duplicate := seen[value]; duplicate {
			return nil, false
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result, true
}

func safePresentationString(value string, maximum int) bool {
	if len(value) > maximum || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || r == '\u061c' || r == '\u200e' || r == '\u200f' ||
			r >= '\u202a' && r <= '\u202e' || r >= '\u2066' && r <= '\u2069' {
			return false
		}
	}
	return true
}

func validEmojiName(value string) bool {
	if len(value) == 0 || len(value) > maxEmojiNameLength {
		return false
	}
	for i := range len(value) {
		c := value[i]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-' || c == '+') {
			return false
		}
	}
	return true
}

// ByID returns one exact, live post. The response must carry the canonical
// identity fields needed to bind later operations to the requested post.
func (s *Posts) ByID(ctx context.Context, postID string) (Post, error) {
	if !isSafePostID(postID) {
		return Post{}, ErrInvalidPostsRequest
	}
	var decoded canonicalSinglePost
	if err := s.client.Get(ctx, "/posts/"+url.PathEscape(postID), &decoded); err != nil {
		return Post{}, err
	}
	post := decoded.Post
	if post.ID != postID {
		return Post{}, ErrInvalidPostResponse
	}
	return post, nil
}

// ReactionState authoritatively reads the complete reaction set for one post.
func (s *Posts) ReactionState(ctx context.Context, postID, channelID, userID, emoji string) (bool, error) {
	if !isSafePostID(postID) || !isSafePostID(channelID) || !isSafePostID(userID) || !validEmojiName(emoji) {
		return false, ErrInvalidPostsRequest
	}
	var decoded canonicalReactions
	decoded.postID = postID
	decoded.channelID = channelID
	if err := s.client.Get(ctx, "/posts/"+url.PathEscape(postID)+"/reactions", &decoded); err != nil {
		return false, err
	}
	for _, reaction := range decoded.values {
		if reaction.UserID == userID && reaction.EmojiName == emoji {
			return true, nil
		}
	}
	return false, nil
}

type canonicalReactions struct {
	postID    string
	channelID string
	values    []PostReaction
}

func (r *canonicalReactions) UnmarshalJSON(data []byte) error {
	var raw []json.RawMessage
	if !isJSONNull(data) && json.Unmarshal(data, &raw) != nil || len(raw) > maxPostReactions {
		return ErrInvalidReactionsResponse
	}
	values := make([]PostReaction, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, candidate := range raw {
		fields, ok := uniqueJSONObject(candidate)
		if !ok {
			return ErrInvalidReactionsResponse
		}
		userID, userOK := safePostID(fields["user_id"])
		postID, postOK := safePostID(fields["post_id"])
		channelID, channelOK := safePostID(fields["channel_id"])
		emoji, emojiOK := strictString(fields["emoji_name"])
		createAt, createOK := nonnegativeInteger(fields["create_at"])
		updateAt, updateOK := nonnegativeInteger(fields["update_at"])
		deleteAt, deleteOK := nonnegativeInteger(fields["delete_at"])
		remoteID, remoteOK := optionalStringValue(fields["remote_id"])
		remoteShapeOK := (remoteOK || isJSONNull(fields["remote_id"])) && safePresentationString(remoteID, maxRemoteIDLength)
		if !userOK || !postOK || postID != r.postID || !channelOK || channelID != r.channelID ||
			!emojiOK || !validEmojiName(emoji) || !createOK || createAt == 0 || createAt > maxDateMilliseconds ||
			!updateOK || updateAt == 0 || updateAt > maxDateMilliseconds || !deleteOK || deleteAt != 0 || !remoteShapeOK {
			return ErrInvalidReactionsResponse
		}
		key := userID + "\x00" + emoji
		if _, duplicate := seen[key]; duplicate {
			return ErrInvalidReactionsResponse
		}
		seen[key] = struct{}{}
		values = append(values, PostReaction{UserID: userID, PostID: postID, EmojiName: emoji, CreateAt: createAt})
	}
	r.values = values
	return nil
}

func (s *Posts) SearchPage(ctx context.Context, teamID string, options SearchPageOptions) (SearchPage, error) {
	if strings.TrimSpace(teamID) == "" || strings.TrimSpace(options.Terms) == "" || options.Page < 0 || options.PerPage <= 0 || options.PerPage > MaxSearchPage {
		return SearchPage{}, ErrInvalidPostsRequest
	}
	body := struct {
		Terms      string `json:"terms"`
		IsOrSearch bool   `json:"is_or_search"`
		Page       int    `json:"page"`
		PerPage    int    `json:"per_page"`
	}{options.Terms, false, options.Page, options.PerPage}
	var page SearchPage
	path := "/teams/" + url.PathEscape(teamID) + "/posts/search"
	if err := s.client.PostRead(ctx, path, body, &page); err != nil {
		return SearchPage{}, err
	}
	return page, nil
}

func (s *Posts) ChannelPage(ctx context.Context, channelID string, options ChannelPostsOptions) (OrderedPostsPage, error) {
	if strings.TrimSpace(channelID) == "" || options.PerPage <= 0 || options.PerPage > MaxPostsPage || options.Page < 0 {
		return OrderedPostsPage{}, ErrInvalidPostsRequest
	}
	params := url.Values{}
	params.Set("per_page", strconv.Itoa(options.PerPage))
	params.Set("page", strconv.Itoa(options.Page))
	params.Set("skipFetchThreads", "true")
	if options.Before != "" {
		params.Set("before", options.Before)
	}
	var page OrderedPostsPage
	path := "/channels/" + url.PathEscape(channelID) + "/posts?" + params.Encode()
	if err := s.client.Get(ctx, path, &page); err != nil {
		return OrderedPostsPage{}, err
	}
	for _, binding := range page.bindings {
		if binding.ChannelID != channelID {
			return OrderedPostsPage{}, ErrInvalidPostsResponse
		}
	}
	return page, nil
}

func (s *Posts) ThreadPage(ctx context.Context, postID string, options ThreadPageOptions) (OrderedPostsPage, error) {
	if !isSafePostID(postID) || options.PerPage <= 0 || options.PerPage > MaxPostsPage ||
		(options.FromPost != "" && !isSafePostID(options.FromPost)) ||
		(options.FromCreateAt != nil && (*options.FromCreateAt <= 0 || *options.FromCreateAt > maxDateMilliseconds)) {
		return OrderedPostsPage{}, ErrInvalidPostsRequest
	}
	params := []string{"perPage=" + strconv.Itoa(options.PerPage), "direction=down"}
	if options.FromPost != "" {
		params = append(params, "fromPost="+url.QueryEscape(options.FromPost))
	}
	if options.FromCreateAt != nil {
		params = append(params, "fromCreateAt="+strconv.FormatInt(*options.FromCreateAt, 10))
	}
	var page OrderedPostsPage
	path := "/posts/" + url.PathEscape(postID) + "/thread?" + strings.Join(params, "&")
	if err := s.client.Get(ctx, path, &page); err != nil {
		return OrderedPostsPage{}, err
	}
	for _, binding := range page.bindings {
		if page.ThreadChannelID == "" {
			page.ThreadChannelID = binding.ChannelID
		} else if page.ThreadChannelID != binding.ChannelID {
			return OrderedPostsPage{}, ErrInvalidPostsResponse
		}
		if binding.ID == postID {
			page.ContainsRequestedPost = true
		}
		if !binding.ThreadShapeKnown {
			page.Incomplete = true
			continue
		}
		candidateRoot := binding.RootID
		if candidateRoot == "" {
			candidateRoot = binding.ID
		}
		if page.ThreadRootID == "" {
			page.ThreadRootID = candidateRoot
		} else if page.ThreadRootID != candidateRoot {
			return OrderedPostsPage{}, ErrInvalidPostsResponse
		}
	}
	if page.ThreadRootID == postID {
		page.ContainsRequestedPost = true
	}
	return page, nil
}
