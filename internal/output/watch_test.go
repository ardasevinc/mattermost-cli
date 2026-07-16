package output

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/ardasevinc/mattermost-cli/internal/mattermost"
	"github.com/ardasevinc/mattermost-cli/internal/presentation"
	"io"
	"strings"
	"testing"
	"time"
)

type shortWatchWriter struct{}
type nilMapWriter map[string]string

func (nilMapWriter) Write([]byte) (int, error) { panic("nil map writer invoked") }

type nilSliceWriter []byte

func (nilSliceWriter) Write([]byte) (int, error) { panic("nil slice writer invoked") }

func (shortWatchWriter) Write(value []byte) (int, error) { return len(value) - 1, nil }
func TestWatchEventSealedSanitizedMultilineAndAtomic(t *testing.T) {
	credential := "credential-secret"
	release := presentation.ActiveCredentials.Register(credential)
	defer release()
	document, err := NewWatchEvent(mattermost.WatchPost{ID: "p\x1b", ChannelID: "c", UserID: "u", Message: "**markdown**\n" + credential, CreateAt: 1, FileIDs: []string{}}, mattermost.Sequence{ConnectionID: "one", Number: 2}, true)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err = WriteWatchDocument(&output, document); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), credential) || strings.ContainsRune(output.String(), '\x1b') || !strings.Contains(output.String(), `**markdown**\n`) {
		t.Fatalf("%q", output.String())
	}
	if err = WriteWatchDocument(shortWatchWriter{}, document); !errors.Is(err, ErrPartialWatchLine) {
		t.Fatal(err)
	}
}
func TestWatchDocumentRejectsInvalidAndOversized(t *testing.T) {
	if err := WriteWatchDocument(&bytes.Buffer{}, nil); !errors.Is(err, ErrInvalidWatchDocument) {
		t.Fatal(err)
	}
	if _, err := NewWatchEvent(mattermost.WatchPost{ID: "p", ChannelID: "c", UserID: "u", Message: strings.Repeat("x", MaxWatchLineBytes), CreateAt: 1, FileIDs: []string{}}, mattermost.Sequence{ConnectionID: "one", Number: 1}, true); !errors.Is(err, ErrInvalidWatchDocument) {
		t.Fatalf("oversize error=%v", err)
	}
}
func TestWriteWatchDocumentRejectsNilWriters(t *testing.T) {
	document, err := NewWatchEvent(mattermost.WatchPost{ID: "p", ChannelID: "c", UserID: "u", CreateAt: 1, FileIDs: []string{}}, mattermost.Sequence{ConnectionID: "one", Number: 1}, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, writer := range []io.Writer{nil, (*bytes.Buffer)(nil), nilMapWriter(nil), nilSliceWriter(nil)} {
		if err := WriteWatchDocument(writer, document); !errors.Is(err, ErrWatchOutput) {
			t.Fatalf("error=%v", err)
		}
	}
}
func TestWatchPresentationOwnsCredentialInConnectionAndDiagnostic(t *testing.T) {
	credential := "connection-secret"
	release := presentation.ActiveCredentials.Register(credential)
	defer release()
	document, err := NewWatchEvent(mattermost.WatchPost{ID: "p", ChannelID: "c", UserID: "u", CreateAt: 1, FileIDs: []string{}}, mattermost.Sequence{ConnectionID: credential + "\n", Number: mattermost.MaxSafeSequence}, true)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := WriteWatchDocument(&out, document); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), credential) || !strings.Contains(out.String(), "watch.sequence.connectionId") {
		t.Fatalf("%s", out.String())
	}
	diagnostic, err := NewWatchDiagnostic(mattermost.WatchDiagnostic{Type: "connection_changed", Timestamp: time.UnixMilli(1), Message: "changed " + credential, PreviousID: credential, CurrentID: "new\n", Expected: pointerInt64(1), Received: pointerInt64(0)})
	if err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := WriteWatchDocument(&out, diagnostic); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), credential) || !strings.Contains(out.String(), `"redactions"`) {
		t.Fatalf("%s", out.String())
	}
}
func pointerInt64(value int64) *int64 { return &value }
func TestWatchSequenceSafeIntegerBoundary(t *testing.T) {
	post := mattermost.WatchPost{ID: "p", ChannelID: "c", UserID: "u", CreateAt: 1, FileIDs: []string{}}
	if _, err := NewWatchEvent(post, mattermost.Sequence{ConnectionID: "c", Number: mattermost.MaxSafeSequence}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := NewWatchEvent(post, mattermost.Sequence{ConnectionID: "c", Number: mattermost.MaxSafeSequence + 1}, true); !errors.Is(err, ErrInvalidWatchDocument) {
		t.Fatal(err)
	}
}

func TestFormatWatchHumanLineMatchesJSWhitespaceAndColor(t *testing.T) {
	stamp := time.Date(2026, 1, 1, 3, 4, 0, 0, time.UTC)
	plain := FormatWatchHumanLine(stamp, "😀", "a\tb\u00a0c\ufeffd\n e", false)
	if plain != "[03:04] 😀: a b c d e" {
		t.Fatalf("plain=%q", plain)
	}
	colored := FormatWatchHumanLine(stamp, "😀", "x", true)
	if colored != "\x1b[2m[03:04]\x1b[0m \x1b[32m😀\x1b[0m: x" {
		t.Fatalf("color=%q", colored)
	}
}

func TestWatchWarningAndTerminalConstructorsAreClosed(t *testing.T) {
	now := time.UnixMilli(1)
	for _, value := range []mattermost.WatchDiagnostic{{Type: "warning", Code: "configuration_warning", Recovery: "none", Timestamp: now, Message: "warning"}, {Type: "terminal", Code: "authentication", Recovery: "check_token", Timestamp: now, Message: "failed", Fatal: true}} {
		if _, err := NewWatchDiagnostic(value); err != nil {
			t.Fatal(err)
		}
	}
	for _, value := range []mattermost.WatchDiagnostic{{Type: "warning", Code: "authentication", Recovery: "none", Timestamp: now, Message: "bad"}, {Type: "terminal", Code: "authentication", Recovery: "none", Timestamp: now, Message: "bad", Fatal: true}, {Type: "terminal", Code: "redaction_disabled", Recovery: "none", Timestamp: now, Message: "bad", Fatal: true}} {
		if _, err := NewWatchDiagnostic(value); !errors.Is(err, ErrInvalidWatchDocument) {
			t.Fatalf("accepted %#v", value)
		}
	}
	attempt := 1
	delay := time.Second
	for _, value := range []mattermost.WatchDiagnostic{
		{Type: "reconnect", Code: "watch_failed", Timestamp: now, Message: "retry", Attempt: &attempt, Delay: &delay},
		{Type: "reconnect", Recovery: "none", Timestamp: now, Message: "retry", Attempt: &attempt, Delay: &delay},
	} {
		if _, err := NewWatchDiagnostic(value); !errors.Is(err, ErrInvalidWatchDocument) {
			t.Fatalf("accepted ordinary diagnostic metadata %#v", value)
		}
	}
}

func TestWatchLabelRedactionPositionsMatchEmittedText(t *testing.T) {
	credential := "credential-position-secret"
	release := presentation.ActiveCredentials.Register(credential)
	defer release()
	document, err := NewWatchEvent(mattermost.WatchPost{ID: "\n\t" + credential, ChannelID: "c", UserID: "u", Message: "line one\n" + credential, CreateAt: 1, FileIDs: []string{}}, mattermost.Sequence{ConnectionID: "\n\t" + credential, Number: 1}, true)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := WriteWatchDocument(&output, document); err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		PostID     string                   `json:"postId"`
		Message    string                   `json:"message"`
		Sequence   WatchSequence            `json:"sequence"`
		Redactions []presentation.Redaction `json:"redactions"`
	}
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(decoded.PostID, `\n\t`) || !strings.HasPrefix(decoded.Sequence.ConnectionID, `\n\t`) || !strings.HasPrefix(decoded.Message, "line one\n") {
		t.Fatalf("presentation=%#v", decoded)
	}
	positions := map[string]int{}
	for _, redaction := range decoded.Redactions {
		positions[redaction.Field] = redaction.Position
	}
	if positions["watch.postId"] != 4 || positions["watch.sequence.connectionId"] != 4 || positions["watch.message"] != 9 {
		t.Fatalf("positions=%v", positions)
	}
	expected := pointerInt64(1)
	received := pointerInt64(0)
	diagnostic, err := NewWatchDiagnostic(mattermost.WatchDiagnostic{Type: "connection_changed", Timestamp: time.UnixMilli(1), Message: "\n\t" + credential, PreviousID: "\n\t" + credential, CurrentID: "new", Expected: expected, Received: received})
	if err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := WriteWatchDocument(&output, diagnostic); err != nil {
		t.Fatal(err)
	}
	var diagnosticDecoded struct {
		Redactions []presentation.Redaction `json:"redactions"`
	}
	if err := json.Unmarshal(output.Bytes(), &diagnosticDecoded); err != nil {
		t.Fatal(err)
	}
	for _, redaction := range diagnosticDecoded.Redactions {
		if redaction.Field == "watch.diagnostic.message" || redaction.Field == "watch.diagnostic.previousConnectionId" {
			if redaction.Position != 4 {
				t.Fatalf("diagnostic redaction=%#v", redaction)
			}
		}
	}
}
