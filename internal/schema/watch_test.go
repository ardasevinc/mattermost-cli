package schema

import (
	"strings"
	"testing"
)

func TestWatchSchemasAreStrictAndDiscriminatorBound(t *testing.T) {
	registry, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	validEvent := `{"schema":"mm/v2/watch-event","type":"posted","sequence":{"connectionId":"c","number":1},"postId":"p","channelId":"ch","channelName":"town","senderId":"u","sender":"arda","message":"hello\nworld","timestamp":"1970-01-01T00:00:00.001Z","rootId":null,"fileIds":[],"redactions":[]}`
	if err := registry.Validate("mm/v2/watch-event", strings.NewReader(validEvent)); err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []string{
		strings.Replace(validEvent, `"number":1`, `"number":-1`, 1),
		strings.Replace(validEvent, `"fileIds":[]`, `"fileIds":[""]`, 1),
		strings.Replace(validEvent, `"redactions":[]`, `"redactions":[],"unknown":true`, 1),
	} {
		if err := registry.Validate("mm/v2/watch-event", strings.NewReader(mutation)); err == nil {
			t.Fatalf("accepted mutation %s", mutation)
		}
	}
	validDiagnostic := `{"schema":"mm/v2/watch-diagnostic","type":"reconnect","timestamp":"2026-07-16T00:00:00.000Z","message":"retry","backfill":false,"fatal":false,"redactions":[],"attempt":1,"delayMs":1000}`
	if err := registry.Validate("mm/v2/watch-diagnostic", strings.NewReader(validDiagnostic)); err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []string{strings.Replace(validDiagnostic, `"backfill":false`, `"backfill":true`, 1), strings.Replace(validDiagnostic, `,"attempt":1,"delayMs":1000`, `,"expected":1,"received":2`, 1)} {
		if err := registry.Validate("mm/v2/watch-diagnostic", strings.NewReader(mutation)); err == nil {
			t.Fatalf("accepted mutation %s", mutation)
		}
	}
}
