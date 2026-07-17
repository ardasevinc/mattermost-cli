package output_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/ardasevinc/mattermost-cli/v2/internal/output"
	"github.com/ardasevinc/mattermost-cli/v2/internal/presentation"
	"github.com/ardasevinc/mattermost-cli/v2/internal/schema"
)

func TestUnreadMachineGoldenSchemaOrderingAndActiveCredential(t *testing.T) {
	release := presentation.ActiveCredentials.Register("active-token")
	defer release()
	raw := []output.RawUnreadItem{
		{Channel: output.RawChannel{ID: "z", Type: "G", Name: "z", TotalMsgCount: 4}, UnreadCount: 2, MentionCount: 1},
		{Channel: output.RawChannel{ID: "a", Type: "G", Name: "active-token\u202e", TotalMsgCount: 4}, UnreadCount: 2, MentionCount: 1},
	}
	doc, err := output.NewUnreadEnvelope(raw, nil, output.UnreadProof{}, presentation.Options{DisableHeuristics: true})
	if err != nil {
		t.Fatal(err)
	}
	var wire bytes.Buffer
	if _, err := output.WriteMachineJSON(&wire, doc); err != nil {
		t.Fatal(err)
	}
	want := `{"schema":"mm/v2/unread","data":{"unread":[{"channel":{"id":"a","type":"group","name":"[REDACTED:mattermost_credential]\\u202e","displayName":null,"team":null,"lastPost":null,"messageCount":4},"unreadCount":2,"mentionCount":1,"lastViewedAt":null},{"channel":{"id":"z","type":"group","name":"z","displayName":null,"team":null,"lastPost":null,"messageCount":4},"unreadCount":2,"mentionCount":1,"lastViewedAt":null}],"peek":[]}}` + "\n"
	if wire.String() != want {
		t.Fatalf("got %s\nwant %s", wire.String(), want)
	}
	registry, err := schema.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Validate("mm/v2/unread", bytes.NewReader(wire.Bytes())); err != nil {
		t.Fatal(err)
	}
}

func TestUnreadPeekRequiresOneExactCompleteHistoryPerSummary(t *testing.T) {
	limit, no := 2, false
	since := "1970-01-01T00:00:01.000Z"
	peek := []output.MessageOutput{{
		Channel:  output.Channel{ID: "c", Type: "group", Name: "crew", MetadataStatus: "resolved"},
		Messages: []output.Message{}, Redactions: []output.Redaction{},
		Retrieval: output.Retrieval{
			Selection:      output.Selection{Source: "unread", SelectedCount: 0, RequestedLimit: &limit, Since: &since, QueryTruncated: &no},
			VisibleThreads: output.VisibleThreads{Status: "not_requested", FailedRootIDs: []string{}},
		},
	}}
	raw := []output.RawUnreadItem{{Channel: output.RawChannel{ID: "c", Type: "G", Name: "crew"}, UnreadCount: 1, LastViewedAt: 1000}}
	doc, err := output.NewUnreadEnvelope(raw, peek, output.UnreadProof{PeekLimit: &limit}, presentation.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Data.Peek) != 1 || doc.Data.Peek[0].Messages == nil || doc.Data.Peek[0].Metadata.Completeness != output.MachineComplete {
		t.Fatalf("peek = %+v", doc.Data.Peek)
	}
	for name, mutate := range map[string]func([]output.MessageOutput){
		"unknown":       func(v []output.MessageOutput) { v[0].Retrieval.Selection.QueryTruncated = nil },
		"wrong channel": func(v []output.MessageOutput) { v[0].Channel.ID = "other" },
		"unsafe":        func(v []output.MessageOutput) { v[0].Channel.Name = "bad\u202e" },
		"wrong since":   func(v []output.MessageOutput) { v[0].Retrieval.Selection.Since = nil },
	} {
		t.Run(name, func(t *testing.T) {
			copy := append([]output.MessageOutput(nil), peek...)
			mutate(copy)
			w := &countingWriter{}
			bad, err := output.NewUnreadEnvelope(raw, copy, output.UnreadProof{PeekLimit: &limit}, presentation.Options{})
			if err == nil {
				_, err = output.WriteMachineJSON(w, bad)
			}
			if err == nil || w.calls != 0 {
				t.Fatalf("error=%v writes=%d", err, w.calls)
			}
		})
	}
}

func TestUnreadPeekAllowsMarkdownMultilineAndRejectsNestedCredentialsAndControls(t *testing.T) {
	release := presentation.ActiveCredentials.Register("active-token")
	defer release()
	limit, complete := 2, false
	message := output.Message{ID: "p", CanonicalID: "p", Permalink: "https://mm.test/_redirect/pl/p", User: "arda", UserID: "u", Text: "**short**\n\tlong markdown line", Timestamp: time.Unix(2, 0), UpdatedAt: time.Unix(2, 0), Files: []string{}, FileDetails: []output.File{}, Attachments: []output.Attachment{}, Reactions: []output.Reaction{}, Replies: []output.Message{}}
	peek := output.MessageOutput{Channel: output.Channel{ID: "c", Type: "group", Name: "crew", MetadataStatus: "resolved"}, Messages: []output.Message{message}, Redactions: []output.Redaction{}, Retrieval: output.Retrieval{Selection: output.Selection{Source: "unread", SelectedCount: 1, RequestedLimit: &limit, QueryTruncated: &complete}, VisibleThreads: output.VisibleThreads{Status: "not_requested", FailedRootIDs: []string{}}, VisiblePostCount: 1}}
	raw := []output.RawUnreadItem{{Channel: output.RawChannel{ID: "c", Type: "G", Name: "crew"}, UnreadCount: 1}}
	if _, err := output.NewUnreadEnvelope(raw, []output.MessageOutput{peek}, output.UnreadProof{PeekLimit: &limit}, presentation.Options{}); err != nil {
		t.Fatalf("valid markdown: %v", err)
	}

	for name, mutate := range map[string]func(*output.MessageOutput){
		"nested credential": func(v *output.MessageOutput) {
			v.Messages[0].Attachments = []output.Attachment{{Fields: []output.AttachmentField{{Title: "key", Value: "active-token"}}}}
		},
		"bidi": func(v *output.MessageOutput) { v.Messages[0].Text = "bad\u061cvalue" },
		"duplicate identity": func(v *output.MessageOutput) {
			v.Messages = append(v.Messages, v.Messages[0])
			v.Retrieval.Selection.SelectedCount = 2
			v.Retrieval.VisiblePostCount = 2
		},
		"forged metadata": func(v *output.MessageOutput) { v.Channel.Name = "other" },
	} {
		t.Run(name, func(t *testing.T) {
			copy := peek
			copy.Messages = append([]output.Message(nil), peek.Messages...)
			mutate(&copy)
			if _, err := output.NewUnreadEnvelope(raw, []output.MessageOutput{copy}, output.UnreadProof{PeekLimit: &limit}, presentation.Options{}); err == nil {
				t.Fatal("invalid peek accepted")
			}
		})
	}
}

func TestUnreadMessageGraphRejectsCanonicalCollisionsAndContradictoryRoots(t *testing.T) {
	limit, complete := 4, false
	base := output.Message{ID: "p", CanonicalID: "cp", Permalink: "https://mm.test/_redirect/pl/p", User: "arda", UserID: "u", Text: "root", Timestamp: time.Unix(2, 0), UpdatedAt: time.Unix(2, 0), Files: []string{}, FileDetails: []output.File{}, Attachments: []output.Attachment{}, Reactions: []output.Reaction{}, Replies: []output.Message{}}
	makePeek := func(messages []output.Message) output.MessageOutput {
		return output.MessageOutput{Channel: output.Channel{ID: "c", Type: "group", Name: "crew", MetadataStatus: "resolved"}, Messages: messages, Redactions: []output.Redaction{}, Retrieval: output.Retrieval{Selection: output.Selection{Source: "unread", SelectedCount: len(messages), RequestedLimit: &limit, QueryTruncated: &complete}, VisibleThreads: output.VisibleThreads{Status: "not_requested", FailedRootIDs: []string{}}, VisiblePostCount: len(messages)}}
	}
	raw := []output.RawUnreadItem{{Channel: output.RawChannel{ID: "c", Type: "G", Name: "crew"}, UnreadCount: 1}}
	for name, messages := range map[string][]output.Message{
		"canonical collision":    {base, func() output.Message { v := base; v.ID = "q"; return v }()},
		"contradictory top root": {func() output.Message { v := base; v.RootID = "p"; return v }()},
		"parent canonical reuse": {func() output.Message {
			v := base
			v.Replies = []output.Message{{ID: "r", CanonicalID: "cp", RootID: "p", CanonicalRootID: "cp", Permalink: "https://mm.test/r", User: "arda", UserID: "u", Text: "reply", Timestamp: time.Unix(3, 0), UpdatedAt: time.Unix(3, 0), Files: []string{}, FileDetails: []output.File{}, Attachments: []output.Attachment{}, Reactions: []output.Reaction{}, Replies: []output.Message{}}}
			return v
		}()},
		"nonroot parent with child": {func() output.Message {
			v := base
			v.RootID, v.CanonicalRootID = "x", "cx"
			child := base
			child.ID, child.CanonicalID, child.RootID, child.CanonicalRootID = "r", "cr", "p", "cp"
			v.Replies = []output.Message{child}
			return v
		}()},
	} {
		t.Run(name, func(t *testing.T) {
			peek := makePeek(messages)
			flattened := len(messages)
			for _, message := range messages {
				flattened += len(message.Replies)
			}
			peek.Retrieval.Selection.SelectedCount, peek.Retrieval.VisiblePostCount = flattened, flattened
			if _, err := output.NewUnreadEnvelope(raw, []output.MessageOutput{peek}, output.UnreadProof{PeekLimit: &limit}, presentation.Options{}); err == nil {
				t.Fatal("invalid graph accepted")
			}
		})
	}
}

func TestUnreadAttachmentMultilineNormalizationContract(t *testing.T) {
	limit, complete := 1, false
	message := output.Message{ID: "p", CanonicalID: "p", Permalink: "https://mm.test/p", User: "arda", UserID: "u", Text: "body", Timestamp: time.Unix(2, 0), UpdatedAt: time.Unix(2, 0), Files: []string{}, FileDetails: []output.File{}, Reactions: []output.Reaction{}, Replies: []output.Message{}, Attachments: []output.Attachment{{Title: "title\ncontinued", AuthorName: "author\tname", Fields: []output.AttachmentField{{Title: "field\nname", Value: "line 1\nline 2"}}}}}
	peek := output.MessageOutput{Channel: output.Channel{ID: "c", Type: "group", Name: "crew", MetadataStatus: "resolved"}, Messages: []output.Message{message}, Redactions: []output.Redaction{}, Retrieval: output.Retrieval{Selection: output.Selection{Source: "unread", SelectedCount: 1, RequestedLimit: &limit, QueryTruncated: &complete}, VisibleThreads: output.VisibleThreads{Status: "not_requested", FailedRootIDs: []string{}}, VisiblePostCount: 1}}
	raw := []output.RawUnreadItem{{Channel: output.RawChannel{ID: "c", Type: "G", Name: "crew"}, UnreadCount: 1}}
	doc, err := output.NewUnreadEnvelope(raw, []output.MessageOutput{peek}, output.UnreadProof{PeekLimit: &limit}, presentation.Options{})
	if err != nil {
		t.Fatal(err)
	}
	field := doc.Data.Peek[0].Messages[0].Attachments[0]
	if field.Title != "title\ncontinued" || field.AuthorName != "author\tname" || field.Fields[0].Title != "field\nname" {
		t.Fatalf("attachment = %+v", field)
	}
}

func TestUnreadAttachmentFieldShortMutationDoesNotAlterProof(t *testing.T) {
	limit, complete, short := 1, false, true
	message := output.Message{ID: "p", CanonicalID: "p", Permalink: "https://mm.test/p", User: "arda", UserID: "u", Text: "body", Timestamp: time.Unix(2, 0), UpdatedAt: time.Unix(2, 0), Files: []string{}, FileDetails: []output.File{}, Reactions: []output.Reaction{}, Replies: []output.Message{}, Attachments: []output.Attachment{{Fields: []output.AttachmentField{{Title: "field", Value: "value", Short: &short}}}}}
	peek := output.MessageOutput{Channel: output.Channel{ID: "c", Type: "group", Name: "crew", MetadataStatus: "resolved"}, Messages: []output.Message{message}, Redactions: []output.Redaction{}, Retrieval: output.Retrieval{Selection: output.Selection{Source: "unread", SelectedCount: 1, RequestedLimit: &limit, QueryTruncated: &complete}, VisibleThreads: output.VisibleThreads{Status: "not_requested", FailedRootIDs: []string{}}, VisiblePostCount: 1}}
	raw := []output.RawUnreadItem{{Channel: output.RawChannel{ID: "c", Type: "G", Name: "crew"}, UnreadCount: 1}}
	doc, err := output.NewUnreadEnvelope(raw, []output.MessageOutput{peek}, output.UnreadProof{PeekLimit: &limit}, presentation.Options{})
	if err != nil {
		t.Fatal(err)
	}
	live := doc.Data.Peek[0].Messages[0].Attachments[0].Fields[0].Short
	*live = false
	w := &countingWriter{}
	if _, err := output.WriteMachineJSON(w, doc); err == nil || w.calls != 0 {
		t.Fatalf("error=%v writes=%d", err, w.calls)
	}
}

func TestUnreadStructuralSealUsesMachineBudgetOnce(t *testing.T) {
	large := strings.Repeat("x", 3<<20)
	doc, err := output.NewUnreadEnvelope([]output.RawUnreadItem{{Channel: output.RawChannel{ID: "c", Type: "G", Name: large}, UnreadCount: 1}}, nil, output.UnreadProof{}, presentation.Options{})
	if err != nil {
		t.Fatalf("near-limit constructor: %v", err)
	}
	var wire bytes.Buffer
	if _, err := output.WriteMachineJSON(&wire, doc); err != nil {
		t.Fatalf("near-limit write: %v", err)
	}
	if _, err := output.NewUnreadEnvelope([]output.RawUnreadItem{{Channel: output.RawChannel{ID: "c", Type: "G", Name: strings.Repeat("x", 5<<20)}, UnreadCount: 1}}, nil, output.UnreadProof{}, presentation.Options{}); err == nil {
		t.Fatal("oversized candidate accepted")
	}
}

func TestUnreadPeekLimitMustBeSafeInteger(t *testing.T) {
	if int64(^uint(0)>>1) <= output.MaxSafeMachineInteger {
		t.Skip("int is not wider than safe machine integer")
	}
	limit := int(output.MaxSafeMachineInteger + 1)
	_, err := output.NewUnreadEnvelope(nil, nil, output.UnreadProof{PeekLimit: &limit}, presentation.Options{})
	if err == nil {
		t.Fatal("unsafe peek limit accepted")
	}
}

func TestUnreadMutationAndDirectConstructionAreZeroWrite(t *testing.T) {
	doc, err := output.NewUnreadEnvelope([]output.RawUnreadItem{{Channel: output.RawChannel{ID: "c", Type: "G", Name: "crew"}, UnreadCount: 1}}, nil, output.UnreadProof{}, presentation.Options{})
	if err != nil {
		t.Fatal(err)
	}
	doc.Data.Unread[0].UnreadCount = 7
	for _, value := range []output.MachineDocument{doc, output.UnreadEnvelope{Schema: "mm/v2/unread"}} {
		w := &countingWriter{}
		if _, err := output.WriteMachineJSON(w, value); err == nil || w.calls != 0 {
			t.Fatalf("error=%v writes=%d", err, w.calls)
		}
	}
}

func TestUnreadHumanGoldenAndAllCaughtUp(t *testing.T) {
	doc, err := output.NewUnreadEnvelope([]output.RawUnreadItem{{Channel: output.RawChannel{ID: "c", Type: "G", Name: "crew"}, UnreadCount: 4, MentionCount: 2}}, nil, output.UnreadProof{}, presentation.Options{})
	if err != nil {
		t.Fatal(err)
	}
	dates := output.NewDateFormatter(func() time.Time { return time.Unix(0, 0) }, time.UTC)
	got, err := output.FormatUnreadPretty(doc, nil, dates, output.PrettyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := "Unread Channels:\n\n  crew                             4 unread, 2 mentions\n\nTotal: 1 channels with unread messages"
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	empty, err := output.NewUnreadEnvelope(nil, nil, output.UnreadProof{}, presentation.Options{})
	if err != nil {
		t.Fatal(err)
	}
	got, err = output.FormatUnreadMarkdown(empty, nil, dates, output.MarkdownOptions{})
	if err != nil || strings.TrimSpace(got) != "All caught up!" {
		t.Fatalf("empty=%q error=%v", got, err)
	}
}
