package output

import (
	"errors"
	"io"
	"testing"
	"time"
)

var prettyNow = time.Date(2026, 2, 21, 12, 0, 0, 0, time.UTC)

func prettyDates() DateFormatter {
	return NewDateFormatter(func() time.Time { return prettyNow }, time.UTC)
}

func TestFormatPrettyPlainGolden(t *testing.T) {
	complete := false
	next := "opaque_123"
	size := int64(42)
	edited := time.Date(2026, 2, 20, 10, 1, 0, 0, time.UTC)
	deleted := time.Date(2026, 2, 20, 10, 2, 0, 0, time.UTC)
	output := MessageOutput{
		Channel: Channel{ID: "ch", Type: "public", Name: "general\\n", DisplayName: "Gen\\u001b"},
		Messages: []Message{{
			ID: "123456789-hostile", Permalink: "https://mm.test/_redirect/pl/x", User: "a\\tb", Text: "one\ntwo\\u202e", Timestamp: time.Date(2026, 2, 20, 10, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 2, 20, 10, 0, 0, 0, time.UTC),
			EditedAt: &edited, DeletedAt: &deleted, IsDeleted: true, IsSystem: true, PostType: "system_x", IsPinned: true,
			FileDetails: []File{{ID: "f1", Name: "a.txt", MIME: "text/plain", Extension: "txt", Size: &size}},
			Attachments: []Attachment{{Pretext: "pre", Title: "title", TitleLink: "link", AuthorName: "author", Text: "body", Fields: []AttachmentField{{Title: "key", Value: "value"}, {Value: "bare"}}, Footer: "foot", Fallback: "fallback", Color: "red", Timestamp: "stamp", AuthorLink: "author-link", AuthorIcon: "author-icon", FooterIcon: "footer-icon", ImageURL: "image", ThumbURL: "thumb"}},
			Reactions:   []Reaction{{Emoji: "eyes", Count: 2, Actors: []ReactionActor{{ID: "u1", Username: "bob"}, {ID: "u2"}}}},
			Replies:     []Message{{ID: "r", Permalink: "reply-link", User: "eve", Text: "reply", Timestamp: time.Date(2026, 2, 20, 10, 3, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 2, 20, 10, 3, 0, 0, time.UTC), Replies: []Message{{ID: "rr", Permalink: "deep-link", User: "zed", Text: "deep", Timestamp: time.Date(2026, 2, 20, 10, 4, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 2, 20, 10, 4, 0, 0, time.UTC)}}}},
		}},
		Redactions: []Redaction{{Type: "token"}},
		Retrieval:  Retrieval{Selection: Selection{SelectedCount: 1, QueryTruncated: &complete, NextCursor: &next}, VisiblePostCount: 3},
	}
	want := "#general\\n (Gen\\u001b)\n" +
		"────────────────────────────────────────\n" +
		"  -- Yesterday --\n\n" +
		"  [10:00] a\\tb [deleted] [system:system_x] [pinned] 12345678 https://mm.test/_redirect/pl/x\n" +
		"    one\n    two\\u202e\n" +
		"    Updated 2026-02-20T10:00:00.000Z; edited 2026-02-20T10:01:00.000Z; deleted 2026-02-20T10:02:00.000Z\n" +
		"    Files: a.txt (f1), text/plain, txt, 42 B\n" +
		"    pre\n    Attachment: title\n      Link: link\n      By: author\n      body\n      key: value\n      bare\n      foot\n      Fallback: fallback\n      Color: red\n      Timestamp: stamp\n      author-link\n      author-icon\n      footer-icon\n      image\n      thumb\n" +
		"    Reactions: :eyes: 2 (bob, u2)\n\n" +
		"    > [10:03] eve r reply-link\n    >   reply\n    >   Updated 2026-02-20T10:03:00.000Z\n\n" +
		"    >   > [10:04] zed rr deep-link\n    >   >   deep\n    >   >   Updated 2026-02-20T10:04:00.000Z\n\n" +
		"  [1 secret(s) redacted]\n" +
		"  Coverage: 1 selected, 3 visible; query complete\n" +
		"  Next cursor: opaque_123"
	if got := FormatPretty([]MessageOutput{output}, prettyDates(), PrettyOptions{}); got != want {
		t.Fatalf("plain bytes mismatch\n--- got ---\n%q\n--- want ---\n%q", got, want)
	}
}

func TestFormatPrettyColorMultipleAndRelativeGolden(t *testing.T) {
	truncated := true
	message := Message{ID: "m", Permalink: "p", User: "alice", Text: "hi", Timestamp: prettyNow.Add(-2 * time.Hour), UpdatedAt: prettyNow}
	outputs := []MessageOutput{
		{Channel: Channel{Type: "dm", Name: "@bob"}, Messages: []Message{message}, Retrieval: Retrieval{Selection: Selection{SelectedCount: 1, QueryTruncated: &truncated}, VisiblePostCount: 1}},
		{Channel: Channel{ID: "cid", Type: "unknown"}, Retrieval: Retrieval{}},
	}
	want := "\x1b[1m💬 DMs with \x1b[36m@bob\x1b[0m\x1b[0m\n\n" +
		"\x1b[2m  ── Today ──\x1b[0m\n\n" +
		"  \x1b[2m2 hours ago\x1b[0m \x1b[1m\x1b[36malice\x1b[0m\x1b[0m \x1b[2mm p\x1b[0m\n" +
		"    hi\n\x1b[2m    Updated 2026-02-21T12:00:00.000Z\x1b[0m\n\n" +
		"  Coverage: 1 selected, 1 visible; query truncated\n" +
		"\x1b[2m────────────────────────────────────────────────────────────\x1b[0m\n\n" +
		"\x1b[1m⚠ Unknown channel (\x1b[36mcid\x1b[0m)\x1b[0m\n\n" +
		"  Coverage: 0 selected, 0 visible; query completeness unknown"
	if got := FormatPretty(outputs, prettyDates(), PrettyOptions{Color: true, Relative: true}); got != want {
		t.Fatalf("color bytes mismatch\n--- got ---\n%q\n--- want ---\n%q", got, want)
	}
	if got := FormatPretty(nil, prettyDates(), PrettyOptions{Color: true}); got != "" {
		t.Fatalf("empty output = %q", got)
	}
}

func TestUserColorMatchesJavaScriptSupplementaryHash(t *testing.T) {
	if got, want := userColor("😀"), "\x1b[32m😀\x1b[0m"; got != want {
		t.Fatalf("userColor = %q, want %q", got, want)
	}
}

type controlledWriter struct {
	n     int
	err   error
	calls int
}

func (w *controlledWriter) Write(p []byte) (int, error) {
	w.calls++
	if w.n > len(p) {
		return len(p), w.err
	}
	return w.n, w.err
}

func TestWritePrettyPropagatesWriterResultsWithoutRetry(t *testing.T) {
	output := []MessageOutput{{Channel: Channel{Type: "dm", Name: "@x"}, Retrieval: Retrieval{}}}
	for _, test := range []struct {
		name string
		n    int
		err  error
		want error
	}{
		{name: "short", n: 3, want: io.ErrShortWrite},
		{name: "failure", n: 2, err: errors.New("disk gone"), want: errors.New("disk gone")},
	} {
		t.Run(test.name, func(t *testing.T) {
			writer := &controlledWriter{n: test.n, err: test.err}
			n, err := WritePretty(writer, output, prettyDates(), PrettyOptions{})
			if n != test.n || writer.calls != 1 {
				t.Fatalf("n=%d calls=%d", n, writer.calls)
			}
			if err == nil || err.Error() != test.want.Error() {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}
