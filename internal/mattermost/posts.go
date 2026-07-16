package mattermost

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
)

const MaxPostsPage = 200

const (
	maxPostIDLength     = 128
	maxDateMilliseconds = int64(8_640_000_000_000_000)
)

var (
	ErrInvalidPostResponse   = errors.New("Mattermost returned an invalid post response")
	ErrInvalidPostsResponse  = errors.New("Mattermost returned an invalid posts response")
	ErrInvalidSearchResponse = errors.New("Mattermost returned an invalid search response")
	ErrInvalidPostsRequest   = errors.New("invalid Mattermost posts request")
)

type postTransport interface {
	Get(context.Context, string, any) error
	PostRead(context.Context, string, any, any) error
}

// Post retains only fields needed for history selection. Presentation-specific
// fields belong in a later, wider model rather than leaking unchecked payloads.
type Post struct {
	ID               string
	ChannelID        string
	Message          string
	CreateAt         int64
	DeleteAt         int64
	RootID           string
	ReplyCount       int
	ThreadShapeKnown bool
}

func (p *Post) UnmarshalJSON(data []byte) error {
	var raw struct {
		ID         json.RawMessage `json:"id"`
		ChannelID  json.RawMessage `json:"channel_id"`
		Message    json.RawMessage `json:"message"`
		CreateAt   json.RawMessage `json:"create_at"`
		DeleteAt   json.RawMessage `json:"delete_at"`
		RootID     json.RawMessage `json:"root_id"`
		ReplyCount json.RawMessage `json:"reply_count"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return ErrInvalidPostResponse
	}
	id, idOK := safePostID(raw.ID)
	channelID, channelOK := requiredString(raw.ChannelID)
	message, messageOK := strictString(raw.Message)
	createAt, createOK := nonnegativeInteger(raw.CreateAt)
	deleteAt, deleteOK := nonnegativeInteger(raw.DeleteAt)
	rootID, rootOK, rootKnown := optionalPostID(raw.RootID)
	replyCount, replyOK, replyKnown := optionalNonnegativeInteger(raw.ReplyCount)
	if !idOK || !channelOK || !messageOK || !createOK || createAt == 0 || createAt > maxDateMilliseconds || !deleteOK || deleteAt > maxDateMilliseconds || !rootOK || !replyOK {
		return ErrInvalidPostResponse
	}
	*p = Post{ID: id, ChannelID: channelID, Message: message, CreateAt: createAt, DeleteAt: deleteAt, RootID: rootID, ReplyCount: replyCount, ThreadShapeKnown: rootKnown && replyKnown}
	return nil
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
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil || value < 0 {
		return 0, false
	}
	return value, true
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
