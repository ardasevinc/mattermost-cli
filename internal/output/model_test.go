package output

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestMessageJSONUsesSemanticFieldsAndHidesCanonicalIdentity(t *testing.T) {
	encoded, err := json.Marshal(Message{
		ID: "visible", CanonicalID: "raw", CanonicalRootID: "raw-root",
		Timestamp: time.Date(2026, time.July, 16, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, time.July, 16, 12, 1, 0, 0, time.UTC),
		Files:     []string{}, FileDetails: []File{}, Attachments: []Attachment{}, Reactions: []Reaction{},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := string(encoded)
	if strings.Contains(got, "raw") || !strings.Contains(got, `"updatedAt"`) || !strings.Contains(got, `"fileDetails":[]`) {
		t.Fatalf("JSON = %s", got)
	}
}

func TestOutputRedactionReusesPresentationUTF16Contract(t *testing.T) {
	redaction := Redaction{Type: "api_key", Masked: "[REDACTED]", Position: 2, Field: "messages[0].text"}
	encoded, err := json.Marshal(redaction)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(encoded), `{"type":"api_key","masked":"[REDACTED]","position":2,"field":"messages[0].text"}`; got != want {
		t.Fatalf("JSON = %s, want %s", got, want)
	}
}
