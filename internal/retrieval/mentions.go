package retrieval

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/ardasevinc/mattermost-cli/internal/mattermost"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

const (
	MaxMentionAliases   = 64
	MaxMentionTermBytes = 256
)

var ErrInvalidMentionsRequest = errors.New("invalid mentions request")

type MentionsOptions struct {
	Username  string
	Aliases   []string
	Channel   *string
	ChannelID *string
	Since     *int64
	Limit     int
}

type MentionsResult struct {
	Posts        []mattermost.Post
	Completeness Completeness
}

// Mentions performs one bounded Search per distinct mention term, then applies
// the exact local predicate that the remote search endpoint cannot express.
func Mentions(ctx context.Context, source searchPageSource, teamID string, options MentionsOptions) (MentionsResult, error) {
	if source == nil {
		return MentionsResult{}, ErrInvalidMentionsRequest
	}
	return retrieveMentions(ctx, func(ctx context.Context, teamID, terms string, options SearchOptions) (SearchResult, error) {
		return Search(ctx, source, teamID, terms, options)
	}, teamID, options)
}

type mentionSearchFunc func(context.Context, string, string, SearchOptions) (SearchResult, error)

func retrieveMentions(ctx context.Context, search mentionSearchFunc, teamID string, options MentionsOptions) (MentionsResult, error) {
	terms, channel, err := mentionTerms(options)
	if err != nil || search == nil || strings.TrimSpace(teamID) == "" {
		return MentionsResult{}, ErrInvalidMentionsRequest
	}
	byID := make(map[string]mattermost.Post)
	states := make([]Completeness, 0, len(terms))
	for _, term := range terms {
		if err := ctx.Err(); err != nil {
			return MentionsResult{}, err
		}
		query := searchTermWithScope(term, channel, options.Since)
		result, err := search(ctx, teamID, query, SearchOptions{
			Limit: options.Limit,
			Accept: func(post mattermost.Post) bool {
				return (options.ChannelID == nil || post.ChannelID == *options.ChannelID) && isExactMention(post, term, options.Since)
			},
		})
		if err != nil {
			return MentionsResult{}, err
		}
		states = append(states, result.Completeness)
		for _, post := range result.Posts {
			byID[post.ID] = post
		}
	}
	return MentionsResult{
		Posts:        mostRecent(byID, options.Limit),
		Completeness: mergeMentionCompleteness(states, len(byID), options.Limit),
	}, nil
}

func mentionTerms(options MentionsOptions) ([]string, string, error) {
	if (options.Channel == nil) != (options.ChannelID == nil) {
		return nil, "", ErrInvalidMentionsRequest
	}
	username := strings.TrimSpace(options.Username)
	channel := ""
	if options.Channel != nil {
		channel = strings.TrimPrefix(strings.TrimSpace(*options.Channel), "#")
		if channel == "" {
			return nil, "", ErrInvalidMentionsRequest
		}
	}
	if options.ChannelID != nil {
		channelID := strings.TrimSpace(*options.ChannelID)
		if channelID != *options.ChannelID || !safeSearchAtom(channelID) {
			return nil, "", ErrInvalidMentionsRequest
		}
	}
	if options.Limit <= 0 || int64(options.Limit) > maxSafeInteger || !safeSearchAtom(username) ||
		(options.Since != nil && (*options.Since < 0 || *options.Since > maxDateMilliseconds)) ||
		(channel != "" && !safeSearchAtom(channel)) {
		return nil, "", ErrInvalidMentionsRequest
	}
	terms := make([]string, 0, len(options.Aliases)+1)
	seen := make(map[string]struct{}, len(options.Aliases)+1)
	add := func(term string) bool {
		if _, ok := seen[term]; ok {
			return false
		}
		seen[term] = struct{}{}
		terms = append(terms, term)
		return true
	}
	add("@" + username)
	aliasCount := 0
	for _, raw := range options.Aliases {
		alias := strings.TrimSpace(raw)
		if alias == "" {
			continue
		}
		if len(alias) > MaxMentionTermBytes || !utf8.ValidString(alias) || strings.ContainsAny(alias, "\"\\\r\n\x00") {
			return nil, "", ErrInvalidMentionsRequest
		}
		for _, r := range alias {
			if unicode.IsControl(r) {
				return nil, "", ErrInvalidMentionsRequest
			}
		}
		if add("\"" + alias + "\"") {
			aliasCount++
			if aliasCount > MaxMentionAliases {
				return nil, "", ErrInvalidMentionsRequest
			}
		}
	}
	return terms, channel, nil
}

func safeSearchAtom(value string) bool {
	if value == "" || len(value) > MaxMentionTermBytes {
		return false
	}
	for i := range len(value) {
		c := value[i]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '.' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func searchTermWithScope(term, channel string, since *int64) string {
	parts := []string{term}
	if since != nil {
		date := time.UnixMilli(*since).UTC().Add(-24 * time.Hour).Format("2006-01-02")
		parts = append(parts, "after:"+date)
	}
	if channel != "" {
		parts = append(parts, "in:"+channel)
	}
	return strings.Join(parts, " ")
}

func isExactMention(post mattermost.Post, term string, since *int64) bool {
	if post.DeleteAt != 0 || (since != nil && post.CreateAt < *since) {
		return false
	}
	literal := term
	if !strings.HasPrefix(term, "@") {
		literal = strings.TrimSuffix(strings.TrimPrefix(term, "\""), "\"")
	}
	alias := !strings.HasPrefix(literal, "@")
	return hasLiteralMention(post.Message, literal, alias)
}

func hasLiteralMention(message, literal string, alias bool) bool {
	lower := cases.Lower(language.Und)
	messageUnits := utf16.Encode([]rune(lower.String(message)))
	literalUnits := utf16.Encode([]rune(lower.String(literal)))
	if len(literalUnits) == 0 {
		return false
	}
	for start := 0; start+len(literalUnits) <= len(messageUnits); start++ {
		matched := true
		for index := range literalUnits {
			if messageUnits[start+index] != literalUnits[index] {
				matched = false
				break
			}
		}
		if !matched {
			continue
		}
		end := start + len(literalUnits)
		if !mentionBoundaryUnit(messageUnits, start-1, alias) && !mentionBoundaryUnit(messageUnits, end, alias) {
			return true
		}
	}
	return false
}

func mentionBoundaryUnit(value []uint16, index int, alias bool) bool {
	if index < 0 || index >= len(value) {
		return false
	}
	unit := value[index]
	if alias {
		if unit >= 0xD800 && unit <= 0xDFFF {
			return false
		}
		r := rune(unit)
		return unicode.IsLetter(r) || unicode.IsMark(r) || unicode.IsNumber(r)
	}
	return unit <= unicode.MaxASCII && ((unit >= 'a' && unit <= 'z') || (unit >= '0' && unit <= '9') || strings.ContainsRune("._-", rune(unit)))
}

func mergeMentionCompleteness(states []Completeness, candidates, limit int) Completeness {
	if candidates > limit {
		return CompletenessTruncated
	}
	for _, state := range states {
		if state == CompletenessTruncated {
			return CompletenessTruncated
		}
	}
	for _, state := range states {
		if state != CompletenessComplete {
			return CompletenessUnknown
		}
	}
	return CompletenessComplete
}
