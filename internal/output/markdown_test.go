package output

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func markdownDates() DateFormatter {
	return NewDateFormatter(func() time.Time { return time.Date(2026, 2, 21, 12, 0, 0, 0, time.UTC) }, time.UTC)
}

func TestFormatMarkdownHostileGolden(t *testing.T) {
	complete := false
	next := "opaque_123"
	size := int64(42)
	edited := time.Date(2026, 2, 20, 10, 1, 0, 0, time.UTC)
	deleted := time.Date(2026, 2, 20, 10, 2, 0, 0, time.UTC)
	output := MessageOutput{
		Channel: Channel{Type: "private", Name: "secret-stuff", DisplayName: "[General]"},
		Messages: []Message{
			{ID: "later", Permalink: "javascript:alert(1)", User: "mallory", Text: "last", Timestamp: time.Date(2026, 2, 21, 8, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 2, 21, 8, 0, 0, 0, time.UTC)},
			{
				ID: "m[1]", Permalink: "https://mm.test/a>b\\c", User: "[admin](https://evil.test)", Text: "# heading\n---\na | b\n*bold*\ncontrol \\u001b", Timestamp: time.Date(2026, 2, 20, 10, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 2, 20, 10, 0, 0, 123000000, time.FixedZone("x", 3*60*60)),
				EditedAt: &edited, DeletedAt: &deleted, IsDeleted: true, IsSystem: true, PostType: "system_webhook", IsPinned: true,
				FileDetails: []File{{ID: "f(1)", Name: "a*.txt", MIME: "text/plain", Extension: "txt", Size: &size}},
				Attachments: []Attachment{{
					Pretext: "pre\ntext", Title: "[click](evil)", TitleLink: "https://example.test/a>b\\c", AuthorName: "a_b", Text: "first\nsecond", Fields: []AttachmentField{{Title: "key*", Value: "v|"}, {Value: "bare"}}, Fallback: "fall#", Footer: "foot_", Color: "#fff", Timestamp: "now!", AuthorLink: "https://example.test/<author>", AuthorIcon: "javascript:bad", FooterIcon: "https://example.test/footer", ImageURL: "ftp://bad", ThumbURL: "https://example.test/thumb",
				}},
				Reactions: []Reaction{{Emoji: "white_check_mark", Count: 2, Actors: []ReactionActor{{Username: "b*b"}, {ID: "u[2]"}}}},
				Replies:   []Message{{ID: "r", Permalink: "not a url", User: "eve", Text: "reply\nline", Timestamp: time.Date(2026, 2, 20, 10, 3, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 2, 20, 10, 3, 0, 0, time.UTC), Replies: []Message{{ID: "rr", Permalink: "https://mm.test/rr", User: "zed", Text: "deep", Timestamp: time.Date(2026, 2, 20, 10, 4, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 2, 20, 10, 4, 0, 0, time.UTC)}}}},
			},
		},
		Redactions: []Redaction{{Type: "token"}},
		Retrieval:  Retrieval{Selection: Selection{SelectedCount: 2, QueryTruncated: &complete, NextCursor: &next}, VisiblePostCount: 4},
	}
	want := "## #secret\\-stuff (\\[General\\])\n\n" +
		"### Friday, 20 February 2026\n\n" +
		"**\\[admin\\]\\(https://evil\\.test\\)** [deleted] [system:system\\_webhook] [pinned] (10:00, [m\\[1\\]](<https://mm.test/a%3Eb%5Cc>)):\n" +
		"> \\# heading\n> \\-\\-\\-\n> a \\| b\n> \\*bold\\*\n> control \\\\u001b\n" +
		"> _Updated 2026-02-20T07:00:00.123Z; edited 2026-02-20T10:01:00.000Z; deleted 2026-02-20T10:02:00.000Z_\n" +
		"> _Files: a\\*\\.txt \\(f\\(1\\)\\), text/plain, txt, 42 B_\n" +
		"> pre\n> text\n" +
		"> **[\\[click\\]\\(evil\\)](<https://example.test/a%3Eb%5Cc>)**\n" +
		"> By: a\\_b\n> first\n> second\n> **key\\*:** v\\|\n> bare\n> Fallback: fall\\#\n> _foot\\__\n> Color: \\#fff\n> Timestamp: now\\!\n" +
		"> <https://example.test/%3Cauthor%3E>\n> <https://example.test/footer>\n> <https://example.test/thumb>\n" +
		"> _Reactions: :white\\_check\\_mark: 2 (b\\*b, u\\[2\\])_\n\n" +
		"> **eve** (10:03, r):\n> > reply\n> > line\n> > _Updated 2026-02-20T10:03:00.000Z_\n\n" +
		"> > **zed** (10:04, [rr](<https://mm.test/rr>)):\n> > > deep\n> > > _Updated 2026-02-20T10:04:00.000Z_\n\n\n\n" +
		"### Saturday, 21 February 2026\n\n**mallory** (08:00, later):\n> last\n> _Updated 2026-02-21T08:00:00.000Z_\n\n\n" +
		"_1 secret(s) redacted_\n\n_Coverage: 2 selected, 4 visible; query complete_\nNext cursor: `opaque_123`"
	if got := FormatMarkdown([]MessageOutput{output}, markdownDates(), MarkdownOptions{}); got != want {
		t.Fatalf("markdown bytes mismatch\n--- got ---\n%q\n--- want ---\n%q", got, want)
	}
}

func TestFormatMarkdownHeadersCoverageRelativeAndSections(t *testing.T) {
	truncated := true
	unknown := MessageOutput{Channel: Channel{ID: "id(1)", Type: "unknown"}, Retrieval: Retrieval{}}
	dm := MessageOutput{Channel: Channel{Type: "dm", Name: "@bob"}, Messages: []Message{{ID: "m", User: "a", Text: "hi", Timestamp: time.Date(2026, 2, 21, 10, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 2, 21, 10, 0, 0, 0, time.UTC)}}, Retrieval: Retrieval{Selection: Selection{SelectedCount: 1, QueryTruncated: &truncated}, VisiblePostCount: 1}}
	got := FormatMarkdown([]MessageOutput{unknown, dm}, markdownDates(), MarkdownOptions{Relative: true})
	for _, want := range []string{"## Unknown channel (id\\(1\\))", "\n\n---\n\n", "## DMs with @bob", "(2 hours ago, m)", "query completeness unknown", "query truncated"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	if got := FormatMarkdown(nil, markdownDates(), MarkdownOptions{}); got != "" {
		t.Fatalf("empty output = %q", got)
	}
}

func TestFormatMarkdownUnsafeAttachmentTitleIsUnlinked(t *testing.T) {
	output := MessageOutput{Channel: Channel{Type: "group", Name: "crew"}, Messages: []Message{{ID: "m", User: "u", Text: "", Timestamp: time.Unix(0, 0), UpdatedAt: time.Unix(0, 0), Attachments: []Attachment{{Title: "[click](https://evil.test)", TitleLink: "javascript:alert(1)"}, {}}}}, Retrieval: Retrieval{}}
	got := FormatMarkdown([]MessageOutput{output}, markdownDates(), MarkdownOptions{})
	if strings.Contains(got, "javascript:") || strings.Contains(got, "[click](https://evil.test)") {
		t.Fatalf("unsafe markdown survived: %q", got)
	}
	if !strings.Contains(got, "> **\\[click\\]\\(https://evil\\.test\\)**\n> **Attachment**") {
		t.Fatalf("safe fallback missing: %q", got)
	}
}

func TestFormatMarkdownMultilineAttachmentTitlesStayQuoted(t *testing.T) {
	message := Message{ID: "m", User: "u", Text: "body", Timestamp: time.Unix(0, 0), UpdatedAt: time.Unix(0, 0), Attachments: []Attachment{
		{Title: "linked\n\n    forged", TitleLink: "https://example.test"},
		{Title: "plain\n\n    forged"},
	}}
	got := FormatMarkdown([]MessageOutput{{Channel: Channel{Type: "dm", Name: "@x"}, Messages: []Message{message}}}, markdownDates(), MarkdownOptions{})
	for _, want := range []string{
		"> **[linked\n> \n>     forged](<https://example.test>)**",
		"> **plain\n> \n>     forged**",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing safely quoted title %q in %q", want, got)
		}
	}
	if strings.Contains(got, "\n\n    forged") {
		t.Fatalf("title escaped blockquote: %q", got)
	}
}

type markdownControlledWriter struct {
	n, calls int
	err      error
}

func (w *markdownControlledWriter) Write(p []byte) (int, error) {
	w.calls++
	if w.n > len(p) {
		return len(p), w.err
	}
	return w.n, w.err
}

func TestWriteMarkdownPropagatesWriterResultsWithoutRetry(t *testing.T) {
	output := []MessageOutput{{Channel: Channel{Type: "dm", Name: "@x"}}}
	for _, test := range []struct {
		name string
		n    int
		err  error
		want error
	}{{"short", 3, nil, io.ErrShortWrite}, {"failure", 2, errors.New("disk gone"), errors.New("disk gone")}} {
		t.Run(test.name, func(t *testing.T) {
			writer := &markdownControlledWriter{n: test.n, err: test.err}
			n, err := WriteMarkdown(writer, output, markdownDates(), MarkdownOptions{})
			if n != test.n || writer.calls != 1 || err == nil || err.Error() != test.want.Error() {
				t.Fatalf("n=%d calls=%d error=%v, want %v", n, writer.calls, err, test.want)
			}
		})
	}
}
