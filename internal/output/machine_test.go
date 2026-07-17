package output

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestWriteMachineJSONWireContract(t *testing.T) {
	var output bytes.Buffer
	value := ChannelEnvelope{Schema: "mm/v2/channel", Data: MachineHistory{Messages: []MachineMessage{{
		Text: "<>&\u2028\u2029\\u2028", Timestamp: MillisTime{time.Date(2026, 7, 16, 1, 2, 3, 456789000, time.UTC)}, UpdatedAt: MillisTime{time.Date(2026, 7, 16, 1, 2, 3, 456789000, time.UTC)},
	}}}}
	if _, err := WriteMachineJSON(&output, value); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "\"text\":\"<>&\u2028\u2029\\\\u2028\"") || !strings.HasSuffix(output.String(), "\n") {
		t.Fatalf("unexpected wire bytes: %q", output.String())
	}
}

func TestDoctorEnvelopeConstructorAndPreflightRejectContradictions(t *testing.T) {
	valid := []DoctorCheck{
		{Name: "configuration", Status: "pass", Message: "credentials resolved", Details: map[string]any{"urlSource": "cli", "tokenSource": "env"}},
		{Name: "server", Status: "warn", Message: "incomplete health", Details: map[string]any{"status": "OK", "databaseStatus": "OK", "filestoreStatus": "unknown"}},
		{Name: "authentication", Status: "pass", Message: "authenticated", Details: map[string]any{"id": "id", "username": "arda"}},
	}
	document, err := NewDoctorEnvelope(true, valid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := WriteMachineJSON(io.Discard, document); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		ok     bool
		mutate func([]DoctorCheck)
	}{
		{"ok with failure", true, func(checks []DoctorCheck) { checks[1].Status = "fail" }},
		{"false without failure", false, func([]DoctorCheck) {}},
		{"wrong order", true, func(checks []DoctorCheck) { checks[0].Name = "server" }},
		{"invalid status", true, func(checks []DoctorCheck) { checks[1].Status = "healthy" }},
		{"skipped with details", false, func(checks []DoctorCheck) { checks[1].Status = "skipped" }},
		{"pass without health", true, func(checks []DoctorCheck) { checks[1].Details = nil }},
		{"auth pass wrong type", true, func(checks []DoctorCheck) { checks[2].Details["id"] = 42 }},
		{"hostile message", true, func(checks []DoctorCheck) { checks[2].Message = "bad\x1b[2J" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			checks := cloneDoctorChecks(valid)
			test.mutate(checks)
			if _, err := NewDoctorEnvelope(test.ok, checks); err == nil {
				t.Fatal("constructor accepted contradictory doctor document")
			}
			writer := &shortMachineWriter{}
			if _, err := WriteMachineJSON(writer, DoctorEnvelope{Schema: "mm/v2/doctor", OK: test.ok, Checks: checks}); err == nil || writer.calls != 0 {
				t.Fatalf("preflight err=%v writes=%d", err, writer.calls)
			}
		})
	}
}

func cloneDoctorChecks(checks []DoctorCheck) []DoctorCheck {
	result := make([]DoctorCheck, len(checks))
	for index, check := range checks {
		result[index] = check
		result[index].Details = make(map[string]any, len(check.Details))
		for key, value := range check.Details {
			result[index].Details[key] = value
		}
	}
	return result
}

type shortMachineWriter struct{ calls int }

func (w *shortMachineWriter) Write(p []byte) (int, error) { w.calls++; return len(p) - 1, nil }

func TestWriteMachineJSONNeverRetriesShortWrite(t *testing.T) {
	w := &shortMachineWriter{}
	_, err := WriteMachineJSON(w, DMSEnvelope{Schema: "mm/v2/dms"})
	if !errors.Is(err, io.ErrShortWrite) || w.calls != 1 {
		t.Fatalf("err=%v calls=%d", err, w.calls)
	}
}

func TestWriteMachineJSONBoundsBeforeWrite(t *testing.T) {
	w := &shortMachineWriter{}
	_, err := WriteMachineJSON(w, ChannelEnvelope{Schema: "mm/v2/channel", Data: MachineHistory{Messages: []MachineMessage{{Text: strings.Repeat("x", MaxMachineDocumentBytes)}}}})
	if err == nil || w.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, w.calls)
	}
}

func TestMachineEmptyCollectionsAreArrays(t *testing.T) {
	values := []MachineDocument{
		DMSEnvelope{Schema: "mm/v2/dms"},
		GroupDMSEnvelope{Schema: "mm/v2/group-dms"},
		SearchEnvelope{Schema: "mm/v2/search"},
		MentionsEnvelope{Schema: "mm/v2/mentions"},
		ChannelEnvelope{Schema: "mm/v2/channel", Data: MachineHistory{}},
		ThreadEnvelope{Schema: "mm/v2/thread", Data: ThreadData{}},
	}
	for _, value := range values {
		var encoded bytes.Buffer
		_, err := WriteMachineJSON(&encoded, value)
		if err != nil {
			t.Fatalf("WriteMachineJSON(%T): %v", value, err)
		}
		if strings.Contains(encoded.String(), `"channels":null`) || strings.Contains(encoded.String(), `"results":null`) || strings.Contains(encoded.String(), `"messages":null`) || strings.Contains(encoded.String(), `"replies":null`) || strings.Contains(encoded.String(), `"files":null`) || strings.Contains(encoded.String(), `"fields":null`) || strings.Contains(encoded.String(), `"actors":null`) {
			t.Fatalf("WriteMachineJSON(%T) emitted a null collection: %s", value, encoded.String())
		}
	}
}

func TestMachineMarshalDoesNotMutateCallerAndIsConcurrentSafe(t *testing.T) {
	message := MachineMessage{
		Attachments: []Attachment{{Fields: nil}},
		Reactions:   []Reaction{{Emoji: "eyes", Actors: nil}},
	}
	var wait sync.WaitGroup
	for range 32 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if _, err := WriteMachineJSON(io.Discard, ChannelEnvelope{Data: MachineHistory{Messages: []MachineMessage{message}}}); err != nil {
				t.Errorf("WriteMachineJSON: %v", err)
			}
		}()
	}
	wait.Wait()
	if message.Attachments[0].Fields != nil || message.Reactions[0].Actors != nil {
		t.Fatalf("marshal mutated caller: %+v", message)
	}
}

func TestWriteMachineJSONAcceptsLargeASCIIWithinWireLimit(t *testing.T) {
	var output bytes.Buffer
	text := strings.Repeat("a", 700<<10)
	_, err := WriteMachineJSON(&output, ChannelEnvelope{Schema: "mm/v2/channel", Data: MachineHistory{Messages: []MachineMessage{{Text: text}}}})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(output.Bytes(), []byte(text[:1024])) {
		t.Fatal("large ASCII text missing from output")
	}
}

func TestWriteMachineJSONRejectsTypedNilBeforeWrite(t *testing.T) {
	var envelope *DMSEnvelope
	var document MachineDocument = envelope
	writer := &shortMachineWriter{}
	if _, err := WriteMachineJSON(writer, document); err == nil || writer.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, writer.calls)
	}
}

func TestMachinePreflightSeparatesTinyValueComplexityFromByteSize(t *testing.T) {
	withinLimit := make([]string, 1000)
	for index := range withinLimit {
		withinLimit[index] = "x"
	}
	if _, err := WriteMachineJSON(io.Discard, ChannelEnvelope{Data: MachineHistory{Messages: []MachineMessage{{Files: withinLimit}}}}); err != nil {
		t.Fatalf("small value graph: %v", err)
	}

	tooMany := make([]string, machinePreflightMaxValues)
	for index := range tooMany {
		tooMany[index] = "x"
	}
	writer := &shortMachineWriter{}
	_, err := WriteMachineJSON(writer, ChannelEnvelope{Data: MachineHistory{Messages: []MachineMessage{{Files: tooMany}}}})
	if err == nil || !strings.Contains(err.Error(), "exceeds complexity limit") || errors.Is(err, errMachineDocumentTooLarge) || writer.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, writer.calls)
	}
}

func TestWriteMachineJSONHandlesDeepRepliesWithOneCanonicalPass(t *testing.T) {
	message := MachineMessage{Text: "leaf"}
	for range 100 {
		message = MachineMessage{Replies: []MachineMessage{message}}
	}
	var output bytes.Buffer
	if _, err := WriteMachineJSON(&output, ChannelEnvelope{Data: MachineHistory{Messages: []MachineMessage{message}}}); err != nil {
		t.Fatal(err)
	}
	if got := bytes.Count(output.Bytes(), []byte(`"replies"`)); got != 101 {
		t.Fatalf("reply arrays=%d, want 101", got)
	}
}

func TestSeparatorWriterRetainsAtMostFiveBytesAcrossLargeChunks(t *testing.T) {
	var output bytes.Buffer
	writer := separatorWriter{destination: &output}
	chunk := []byte(strings.Repeat("x", 1<<20) + `\u2028`)
	if _, err := writer.Write(chunk); err != nil {
		t.Fatal(err)
	}
	if writer.pendingLen > 5 {
		t.Fatalf("pendingLen=%d", writer.pendingLen)
	}
	if err := writer.flush(); err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(output.Bytes(), []byte("\u2028")) {
		t.Fatal("line separator was not preserved literally")
	}
}

func TestWriteMachineJSONRejectsRichOversizeAndCyclesBeforeWrite(t *testing.T) {
	for name, message := range map[string]MachineMessage{
		"rich oversized":   {Attachments: []Attachment{{Text: strings.Repeat("x", MaxMachineDocumentBytes)}}},
		"nested oversized": {Replies: []MachineMessage{{Replies: []MachineMessage{{Text: strings.Repeat("y", MaxMachineDocumentBytes)}}}}},
	} {
		writer := &shortMachineWriter{}
		_, err := WriteMachineJSON(writer, ChannelEnvelope{Schema: "mm/v2/channel", Data: MachineHistory{Messages: []MachineMessage{message}}})
		if !errors.Is(err, errMachineDocumentTooLarge) || writer.calls != 0 {
			t.Errorf("%s: err=%v calls=%d", name, err, writer.calls)
		}
	}
	cycle := make([]MachineMessage, 1)
	cycle[0].Replies = cycle
	writer := &shortMachineWriter{}
	_, err := WriteMachineJSON(writer, ChannelEnvelope{Data: MachineHistory{Messages: cycle}})
	if err == nil || writer.calls != 0 {
		t.Fatalf("cycle: err=%v calls=%d", err, writer.calls)
	}
}

func TestMachineMessagePreservesRichAgentFields(t *testing.T) {
	message := MachineMessage{
		Files: []string{}, FileDetails: []File{{ID: "f"}}, Attachments: []Attachment{{Fields: nil}},
		Reactions: []Reaction{{Emoji: "eyes", Actors: nil}}, Replies: []MachineMessage{{Files: nil}},
	}
	var encoded bytes.Buffer
	_, err := WriteMachineJSON(&encoded, ChannelEnvelope{Data: MachineHistory{Messages: []MachineMessage{message}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"fileDetails":[`, `"attachments":[`, `"reactions":[`, `"actors":[]`, `"replies":[`, `"files":[]`} {
		if !strings.Contains(encoded.String(), field) {
			t.Fatalf("missing %s in %s", field, encoded.String())
		}
	}
}

func TestMillisTimeNormalizesZeroOffsetAndRejectsInvalidYears(t *testing.T) {
	zeroOffset := time.FixedZone("UTC spelled differently", 0)
	encoded, err := json.Marshal(MillisTime{time.Date(2026, 7, 16, 1, 2, 3, 999999999, zeroOffset)})
	if err != nil || string(encoded) != `"2026-07-16T01:02:03.999Z"` {
		t.Fatalf("encoded=%s err=%v", encoded, err)
	}
	for _, value := range []time.Time{time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 1, 1, 0, 0, 0, 0, time.FixedZone("plus one", 3600))} {
		if _, err := json.Marshal(MillisTime{value}); err == nil {
			t.Fatalf("accepted %v", value)
		}
	}
}
