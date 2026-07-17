package retrieval

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/ardasevinc/mattermost-cli/v2/internal/mattermost"
)

func TestMentionTermsTrimQuoteDedupeAndScope(t *testing.T) {
	since := int64(1_768_404_600_000) // 2026-01-14 15:30:00 UTC
	terms, channel, err := mentionTerms(MentionsOptions{
		Username: " arda ", Aliases: []string{" Arda Sevinc ", "", "Arda Sevinc", "arda"},
		Channel: stringPointer(" #general "), ChannelID: stringPointer("channel-id"), Since: &since, Limit: 20,
	})
	if err != nil || channel != "general" || !reflect.DeepEqual(terms, []string{"@arda", `"Arda Sevinc"`, `"arda"`}) {
		t.Fatalf("terms=%q channel=%q err=%v", terms, channel, err)
	}
	got := searchTermWithScope(terms[1], channel, &since)
	if got != `"Arda Sevinc" after:2026-01-13 in:general` {
		t.Fatalf("query=%q", got)
	}
}

func TestExactMentionLiteralUnicodeAndPunctuationBoundaries(t *testing.T) {
	tests := []struct {
		name, message, term string
		want                bool
	}{
		{"alias case", "hello ARDA SEVINC", `"Arda Sevinc"`, true},
		{"alias punctuation", "(Arda).com", `"Arda"`, true},
		{"alias unicode letter before", "İArda", `"Arda"`, false},
		{"alias unicode letter after", "Arda東京", `"Arda"`, false},
		{"alias number", "Arda2", `"Arda"`, false},
		{"alias regex metacharacters literal", "ping (a+b)[x] now", `"(a+b)[x]"`, true},
		{"alias regex metacharacters absent", "ping aaabx now", `"a+b[x]"`, false},
		{"username punctuation", "(@arda)!", "@arda", true},
		{"username dot", "@arda.com", "@arda", false},
		{"username underscore", "@arda_more", "@arda", false},
		{"username hyphen", "@arda-name", "@arda", false},
		{"username prefix", "x@arda", "@arda", false},
		{"username unicode adjacent allowed", "ğ@ardağ", "@arda", true},
		{"quoted username alias uses username boundary", "@ops.com", `"@ops"`, false},
		{"contextual sigma", "ος", `"ΟΣ"`, true},
		{"non-final sigma differs", "οσ", `"ΟΣ"`, false},
		{"full lowercase expansion", "İ", `"i"`, false},
		{"utf16 supplementary boundary parity", "𐐀Arda", `"Arda"`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			post := mattermost.Post{ID: "p", Message: tt.message, CreateAt: 10}
			if got := isExactMention(post, tt.term, nil); got != tt.want {
				t.Fatalf("isExactMention(%q, %q)=%v want %v", tt.message, tt.term, got, tt.want)
			}
		})
	}
}

func TestExactMentionFiltersDeletedAndExactSince(t *testing.T) {
	since := int64(1000)
	for _, post := range []mattermost.Post{
		{Message: "@arda", CreateAt: 999},
		{Message: "@arda", CreateAt: 1000, DeleteAt: 1},
	} {
		if isExactMention(post, "@arda", &since) {
			t.Fatalf("accepted %#v", post)
		}
	}
	if !isExactMention(mattermost.Post{Message: "@arda", CreateAt: 1000}, "@arda", &since) {
		t.Fatal("rejected exact since boundary")
	}
}

func TestRetrieveMentionsMergesGloballyAndCompleteness(t *testing.T) {
	var queries []string
	result, err := retrieveMentions(context.Background(), func(_ context.Context, _, query string, options SearchOptions) (SearchResult, error) {
		queries = append(queries, query)
		candidates := []mattermost.Post{
			{ID: "shared", Message: "@arda Arda", CreateAt: 2},
			{ID: query, Message: query, CreateAt: 1},
		}
		accepted := make([]mattermost.Post, 0, len(candidates))
		for _, post := range candidates {
			if options.Accept(post) {
				accepted = append(accepted, post)
			}
		}
		state := CompletenessComplete
		if query == `"Arda"` {
			state = CompletenessUnknown
		}
		return SearchResult{Posts: accepted, Completeness: state}, nil
	}, "team", MentionsOptions{Username: "arda", Aliases: []string{"Arda"}, Limit: 2})
	if err != nil || fmt.Sprint(queries) != `[@arda "Arda"]` || fmt.Sprint(ids(result.Posts)) != `[shared "Arda"]` || result.Completeness != CompletenessTruncated {
		t.Fatalf("queries=%q result=%#v err=%v", queries, result, err)
	}

	if got := mergeMentionCompleteness([]Completeness{CompletenessComplete, CompletenessUnknown}, 1, 2); got != CompletenessUnknown {
		t.Fatalf("unknown merge=%v", got)
	}
	if got := mergeMentionCompleteness([]Completeness{CompletenessUnknown, CompletenessTruncated}, 1, 2); got != CompletenessTruncated {
		t.Fatalf("truncated merge=%v", got)
	}
}

func TestRetrieveMentionsEnforcesExactResolvedChannelID(t *testing.T) {
	result, err := retrieveMentions(context.Background(), func(_ context.Context, _, _ string, options SearchOptions) (SearchResult, error) {
		posts := []mattermost.Post{
			{ID: "expected", ChannelID: "channel-1", Message: "@arda", CreateAt: 1},
			{ID: "foreign", ChannelID: "channel-2", Message: "@arda", CreateAt: 2},
		}
		accepted := make([]mattermost.Post, 0, len(posts))
		for _, post := range posts {
			if options.Accept(post) {
				accepted = append(accepted, post)
			}
		}
		return SearchResult{Posts: accepted, Completeness: CompletenessComplete}, nil
	}, "team", MentionsOptions{
		Username: "arda", Channel: stringPointer("town-square"), ChannelID: stringPointer("channel-1"), Limit: 10,
	})
	if err != nil || fmt.Sprint(ids(result.Posts)) != "[expected]" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestRetrieveMentionsPropagatesErrorsAndCancellation(t *testing.T) {
	want := errors.New("search failed")
	_, err := retrieveMentions(context.Background(), func(context.Context, string, string, SearchOptions) (SearchResult, error) {
		return SearchResult{}, want
	}, "team", MentionsOptions{Username: "arda", Limit: 1})
	if !errors.Is(err, want) {
		t.Fatalf("error=%v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = retrieveMentions(ctx, func(context.Context, string, string, SearchOptions) (SearchResult, error) {
		t.Fatal("search called after cancellation")
		return SearchResult{}, nil
	}, "team", MentionsOptions{Username: "arda", Limit: 1})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error=%v", err)
	}
}

func TestMentionValidationBoundsWithoutReflectingInput(t *testing.T) {
	duplicates := make([]string, MaxMentionAliases+1)
	for index := range duplicates {
		duplicates[index] = "Arda Sevinc"
	}
	terms, _, err := mentionTerms(MentionsOptions{Username: "arda", Aliases: duplicates, Limit: 1})
	if err != nil || !reflect.DeepEqual(terms, []string{"@arda", `"Arda Sevinc"`}) {
		t.Fatalf("duplicate aliases were not bounded after dedupe: terms=%q err=%v", terms, err)
	}
	distinct := make([]string, MaxMentionAliases+1)
	for index := range distinct {
		distinct[index] = fmt.Sprintf("alias-%d", index)
	}
	tests := []MentionsOptions{
		{Username: "bad user", Limit: 1},
		{Username: "arda", Channel: stringPointer("general after:2000-01-01"), Limit: 1},
		{Username: "arda", Channel: stringPointer("#"), Limit: 1},
		{Username: "arda", Aliases: []string{`hostile" after:2000-01-01`}, Limit: 1},
		{Username: "arda", Aliases: []string{string(make([]byte, MaxMentionTermBytes+1))}, Limit: 1},
		{Username: "arda", Aliases: distinct, Limit: 1},
	}
	for _, options := range tests {
		_, _, err := mentionTerms(options)
		if !errors.Is(err, ErrInvalidMentionsRequest) || err.Error() != "invalid mentions request" {
			t.Fatalf("error=%v", err)
		}
	}
}

func stringPointer(value string) *string { return &value }
