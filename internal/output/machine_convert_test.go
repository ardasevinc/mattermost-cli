package output_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/ardasevinc/mattermost-cli/v2/internal/output"
	"github.com/ardasevinc/mattermost-cli/v2/internal/schema"
)

func TestMachineMessageFromMessagePreservesRichRecursiveDataWithoutAliasing(t *testing.T) {
	location := time.FixedZone("offset", 3*60*60)
	edited := time.Date(2026, 7, 16, 13, 14, 15, 987654321, location)
	count, size, short := 0, int64(42), false
	reply := output.Message{ID: "reply", RootID: "root", Text: "nested", Timestamp: edited, UpdatedAt: edited}
	message := output.Message{
		ID: "root", Permalink: "https://mm.test/_redirect/pl/root", User: "arda", UserID: "u1", Text: "hello",
		Timestamp: edited, UpdatedAt: edited, EditedAt: &edited, IsPinned: true, PostType: "custom", ReplyCount: &count,
		Files: []string{"report.txt"}, FileDetails: []output.File{{ID: "f1", Name: "report.txt", Size: &size}},
		Attachments: []output.Attachment{{Title: "rich", Fields: []output.AttachmentField{{Title: "field", Value: "value", Short: &short}}}},
		Reactions:   []output.Reaction{{Emoji: "wave", Count: 1, Actors: []output.ReactionActor{{ID: "u2", Username: "bob"}}}},
		Replies:     []output.Message{reply},
	}

	converted := output.MachineMessageFromMessage(message)
	if converted.Timestamp.Location() != time.UTC || converted.EditedAt == nil || converted.EditedAt.Location() != time.UTC {
		t.Fatalf("timestamps were not normalized to UTC: %#v", converted)
	}
	if !converted.Timestamp.Equal(edited) || !converted.EditedAt.Equal(edited) {
		t.Fatalf("timestamp instant changed: got %v / %v, want %v", converted.Timestamp, converted.EditedAt, edited)
	}
	if converted.RootID != nil || converted.ReplyCount == nil || *converted.ReplyCount != 0 || len(converted.Replies) != 1 || converted.Replies[0].RootID == nil || *converted.Replies[0].RootID != "root" {
		t.Fatalf("nullable or recursive fields changed: %#v", converted)
	}

	converted.Files[0] = "changed"
	*converted.FileDetails[0].Size = 99
	*converted.Attachments[0].Fields[0].Short = true
	converted.Reactions[0].Actors[0].Username = "changed"
	converted.Replies[0].Text = "changed"
	*converted.ReplyCount = 2
	if message.Files[0] != "report.txt" || *message.FileDetails[0].Size != 42 || *message.Attachments[0].Fields[0].Short || message.Reactions[0].Actors[0].Username != "bob" || message.Replies[0].Text != "nested" || *message.ReplyCount != 0 {
		t.Fatal("converted message aliases input storage")
	}
}

func TestMachineEnvelopeFactoriesWriteSchemaValidDocuments(t *testing.T) {
	registry, err := schema.Load()
	if err != nil {
		t.Fatal(err)
	}
	value := validOutput()

	channel, err := output.NewChannelEnvelope(value, output.MachineComplete)
	if err != nil {
		t.Fatal(err)
	}
	dmValue := value
	dmValue.Channel.Type = "dm"
	dms, err := output.NewDMSEnvelope([]output.MessageOutput{dmValue}, output.MachineComplete)
	if err != nil {
		t.Fatal(err)
	}
	groupValue := value
	groupValue.Channel.Type = "group"
	groups, err := output.NewGroupDMSEnvelope([]output.MessageOutput{groupValue}, output.MachineComplete)
	if err != nil {
		t.Fatal(err)
	}
	searchValue := value
	searchValue.Retrieval.Selection.Source = "search"
	search, err := output.NewSearchEnvelope([]output.MessageOutput{searchValue}, output.MachineComplete)
	if err != nil {
		t.Fatal(err)
	}
	mentionsValue := value
	mentionsValue.Retrieval.Selection.Source = "mentions"
	mentions, err := output.NewMentionsEnvelope([]output.MessageOutput{mentionsValue}, output.MachineComplete)
	if err != nil {
		t.Fatal(err)
	}
	threadValue := value
	threadValue.Retrieval.Selection.Source = "thread"
	thread, err := output.NewThreadEnvelope(threadValue, output.MachineComplete)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		id       string
		document output.MachineDocument
	}{
		{"mm/v2/channel", channel}, {"mm/v2/dms", dms}, {"mm/v2/group-dms", groups},
		{"mm/v2/search", search}, {"mm/v2/mentions", mentions}, {"mm/v2/thread", thread},
	}
	for _, test := range cases {
		t.Run(test.id, func(t *testing.T) {
			var wire bytes.Buffer
			if _, err := output.WriteMachineJSON(&wire, test.document); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(wire.String(), ":null") && (strings.Contains(wire.String(), `"files":null`) || strings.Contains(wire.String(), `"redactions":null`) || strings.Contains(wire.String(), `"replies":null`)) {
				t.Fatalf("writer did not canonicalize nil collections: %s", wire.String())
			}
			if err := registry.Validate(test.id, bytes.NewReader(wire.Bytes())); err != nil {
				t.Fatalf("schema validation: %v\n%s", err, wire.String())
			}
			if !strings.Contains(wire.String(), `"timestamp":"2026-07-16T10:14:15.987Z"`) {
				t.Fatalf("timestamp is not exact UTC milliseconds: %s", wire.String())
			}
		})
	}
}

func TestMachineConversionRejectsInvalidVocabularyAndAmbiguousThreads(t *testing.T) {
	value := validOutput()
	value.Channel.Type = "town-square"
	if _, err := output.MachineHistoryFromOutput(value, output.MachineComplete); err == nil {
		t.Fatal("invalid channel type accepted")
	}
	value = validOutput()
	value.Channel.MetadataStatus = "maybe"
	if _, err := output.MachineHistoryFromOutput(value, output.MachineComplete); err == nil {
		t.Fatal("invalid metadata status accepted")
	}
	value = validOutput()
	if _, err := output.MachineHistoryFromOutput(value, output.MachineCompleteness("maybe")); err == nil {
		t.Fatal("invalid completeness accepted")
	}
	value = validOutput()
	value.Channel.Type = "unknown"
	if _, err := output.MachineHistoryFromOutput(value, output.MachineComplete); err == nil {
		t.Fatal("inconsistent channel type/status accepted")
	}

	badRetrievals := map[string]func(*output.MessageOutput){
		"source":            func(v *output.MessageOutput) { v.Retrieval.Selection.Source = "other" },
		"selected count":    func(v *output.MessageOutput) { v.Retrieval.Selection.SelectedCount = -1 },
		"requested limit":   func(v *output.MessageOutput) { n := 0; v.Retrieval.Selection.RequestedLimit = &n },
		"visible count":     func(v *output.MessageOutput) { v.Retrieval.VisiblePostCount = -1 },
		"hydrated count":    func(v *output.MessageOutput) { v.Retrieval.VisibleThreads.HydratedRootCount = -1 },
		"visible status":    func(v *output.MessageOutput) { v.Retrieval.VisibleThreads.Status = "other" },
		"complete failures": func(v *output.MessageOutput) { v.Retrieval.VisibleThreads.FailedRootIDs = []string{"root"} },
		"deleted posts":     func(v *output.MessageOutput) { v.Retrieval.DeletedPostsIncluded = true },
	}
	for name, corrupt := range badRetrievals {
		t.Run(name, func(t *testing.T) {
			value := validOutput()
			corrupt(&value)
			if _, err := output.MachineHistoryFromOutput(value, output.MachineComplete); err == nil {
				t.Fatal("invalid retrieval accepted")
			}
		})
	}

	badMessages := map[string]func(*output.Message){
		"timestamp":       func(m *output.Message) { m.Timestamp = time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC) },
		"reply count":     func(m *output.Message) { n := -1; m.ReplyCount = &n },
		"file size":       func(m *output.Message) { n := int64(-1); m.FileDetails = []output.File{{ID: "f", Size: &n}} },
		"reaction count":  func(m *output.Message) { m.Reactions = []output.Reaction{{Count: -1}} },
		"recursive reply": func(m *output.Message) { m.Replies[0].Reactions = []output.Reaction{{Count: -1}} },
	}
	value = validOutput()
	value.Redactions[0].Position = -1
	if _, err := output.MachineHistoryFromOutput(value, output.MachineComplete); err == nil {
		t.Fatal("negative redaction position accepted")
	}
	for name, corrupt := range badMessages {
		t.Run(name, func(t *testing.T) {
			value := validOutput()
			corrupt(&value.Messages[0])
			if _, err := output.MachineHistoryFromOutput(value, output.MachineComplete); err == nil {
				t.Fatal("schema-invalid message accepted")
			}
		})
	}
	if _, err := output.NewDMSEnvelope(nil, output.MachineCompleteness("bad")); err == nil {
		t.Fatal("empty factory skipped completeness validation")
	}
	if _, err := output.NewSearchEnvelope([]output.MessageOutput{validOutput()}, output.MachineComplete); err == nil {
		t.Fatal("search factory accepted recent source")
	}
	value = validOutput()
	value.Retrieval.VisibleThreads.Status = "partial"
	if _, err := output.MachineHistoryFromOutput(value, output.MachineComplete); err != nil {
		t.Fatalf("partial retrieval with unknown failed root identities rejected: %v", err)
	}
	if _, err := output.NewDMSEnvelope([]output.MessageOutput{validOutput()}, output.MachineComplete); err == nil {
		t.Fatal("DMS factory accepted public channel")
	}
	value = validOutput()
	value.Channel.Type = "dm"
	if _, err := output.NewGroupDMSEnvelope([]output.MessageOutput{value}, output.MachineComplete); err == nil {
		t.Fatal("group-DMS factory accepted DM channel")
	}

	for name, messages := range map[string][]output.Message{
		"zero roots":      {},
		"multiple roots":  {{ID: "one"}, {ID: "two"}},
		"top-level reply": {{ID: "reply", RootID: "root"}},
	} {
		t.Run(name, func(t *testing.T) {
			value := validOutput()
			value.Retrieval.Selection.Source = "thread"
			value.Messages = messages
			if _, err := output.NewThreadEnvelope(value, output.MachineComplete); err == nil {
				t.Fatal("ambiguous thread accepted")
			}
		})
	}

	threadCases := map[string]func(*output.Message){
		"orphan":           func(root *output.Message) { root.Replies[0].RootID = "other" },
		"canonical orphan": func(root *output.Message) { root.Replies[0].CanonicalRootID = "other" },
		"nested":           func(root *output.Message) { root.Replies[0].Replies = []output.Message{{ID: "nested"}} },
		"duplicate":        func(root *output.Message) { root.Replies = append(root.Replies, root.Replies[0]) },
	}
	for name, corrupt := range threadCases {
		t.Run(name, func(t *testing.T) {
			value := validOutput()
			value.Retrieval.Selection.Source = "thread"
			corrupt(&value.Messages[0])
			if _, err := output.NewThreadEnvelope(value, output.MachineComplete); err == nil {
				t.Fatal("invalid thread accepted")
			}
		})
	}
	value = validOutput()
	value.Retrieval.Selection.Source = "thread"
	value.Messages[0].CanonicalID = ""
	value.Messages[0].Replies[0].CanonicalID = ""
	value.Messages[0].Replies[0].CanonicalRootID = ""
	if _, err := output.NewThreadEnvelope(value, output.MachineComplete); err != nil {
		t.Fatalf("thread identity fallback rejected valid grouped model: %v", err)
	}
}

func TestThreadEnvelopeKeepsUnknownShapeUnboundInsteadOfInventingRoot(t *testing.T) {
	value := validOutput()
	value.Retrieval.Selection.Source = "thread"
	value.Retrieval.Selection.QueryTruncated = nil
	value.Retrieval.VisibleThreads = output.VisibleThreads{Status: "partial", FailedRootIDs: []string{"requested"}}
	known := false
	value.Messages[0].CanonicalThreadShapeKnown = &known
	value.Messages[0].Replies = nil
	envelope, err := output.NewThreadEnvelope(value, output.MachineUnknown)
	if err != nil {
		t.Fatal(err)
	}
	if envelope.Data.Root != nil || len(envelope.Data.UnboundPosts) != 1 || envelope.Data.UnboundPosts[0].ID != value.Messages[0].ID {
		t.Fatalf("unexpected rootless envelope: %#v", envelope.Data)
	}
}

func TestThreadEnvelopeRejectsContradictoryRetrievalMetadata(t *testing.T) {
	for name, corrupt := range map[string]func(*output.MessageOutput, *output.MachineCompleteness){
		"complete with partial hydration": func(value *output.MessageOutput, _ *output.MachineCompleteness) {
			value.Retrieval.VisibleThreads = output.VisibleThreads{Status: "partial", FailedRootIDs: []string{"root"}}
		},
		"truncated with complete hydration": func(value *output.MessageOutput, completeness *output.MachineCompleteness) {
			*completeness = output.MachineTruncated
			truncated := true
			value.Retrieval.Selection.QueryTruncated = &truncated
		},
		"unknown with known query status": func(value *output.MessageOutput, completeness *output.MachineCompleteness) {
			*completeness = output.MachineUnknown
			value.Retrieval.VisibleThreads = output.VisibleThreads{Status: "partial", FailedRootIDs: []string{"root"}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			value := validOutput()
			value.Retrieval.Selection.Source = "thread"
			completeness := output.MachineComplete
			corrupt(&value, &completeness)
			if _, err := output.NewThreadEnvelope(value, completeness); err == nil {
				t.Fatal("contradictory thread retrieval metadata accepted")
			}
		})
	}

	value := validOutput()
	value.Retrieval.Selection.Source = "thread"
	truncated := true
	value.Retrieval.Selection.QueryTruncated = &truncated
	value.Retrieval.VisibleThreads = output.VisibleThreads{Status: "partial", FailedRootIDs: []string{"root"}}
	if _, err := output.NewThreadEnvelope(value, output.MachineTruncated); err != nil {
		t.Fatalf("consistent truncated thread rejected: %v", err)
	}
}

func TestHistoryEnvelopeRejectsCompletenessThatContradictsQueryTruncation(t *testing.T) {
	if _, err := output.NewSearchEnvelope(nil, output.MachineUnknown); err == nil {
		t.Fatal("unknown empty search envelope was accepted without metadata")
	}
	if _, err := output.NewMentionsEnvelope(nil, output.MachineTruncated); err == nil {
		t.Fatal("truncated empty mentions envelope was accepted without metadata")
	}
	if _, err := output.NewDMSEnvelope(nil, output.MachineUnknown); err == nil {
		t.Fatal("unknown empty direct-message envelope was accepted without metadata")
	}
	if _, err := output.NewGroupDMSEnvelope(nil, output.MachineUnknown); err == nil {
		t.Fatal("unknown empty group direct-message envelope was accepted without metadata")
	}
	value := validOutput()
	value.Retrieval.Selection.Source = "search"
	truncated := true
	value.Retrieval.Selection.QueryTruncated = &truncated
	if _, err := output.NewSearchEnvelope([]output.MessageOutput{value}, output.MachineComplete); err == nil {
		t.Fatal("complete search accepted queryTruncated true")
	}
	truncated = false
	if _, err := output.NewSearchEnvelope([]output.MessageOutput{value}, output.MachineUnknown); err == nil {
		t.Fatal("unknown search accepted known queryTruncated status")
	}
}

func validOutput() output.MessageOutput {
	zone := time.FixedZone("source", 3*60*60)
	stamp := time.Date(2026, 7, 16, 13, 14, 15, 987654321, zone)
	limit, truncated := 10, false
	known := true
	return output.MessageOutput{
		Channel:    output.Channel{ID: "c1", Type: "public", Name: "town-square", DisplayName: "Town Square", MetadataStatus: "resolved"},
		Messages:   []output.Message{{ID: "root", CanonicalID: "root", CanonicalThreadShapeKnown: &known, User: "arda", UserID: "u1", Text: "root", Timestamp: stamp, UpdatedAt: stamp, Replies: []output.Message{{ID: "reply", CanonicalID: "reply", RootID: "root", CanonicalRootID: "root", CanonicalThreadShapeKnown: &known, User: "bob", UserID: "u2", Text: "reply", Timestamp: stamp, UpdatedAt: stamp}}}},
		Redactions: []output.Redaction{{Type: "token", Masked: "abc***xyz", Position: 0}},
		Retrieval: output.Retrieval{
			Selection:      output.Selection{Source: "recent", SelectedCount: 2, RequestedLimit: &limit, QueryTruncated: &truncated},
			VisibleThreads: output.VisibleThreads{Status: "complete", HydratedRootCount: 1}, VisiblePostCount: 2,
		},
	}
}
