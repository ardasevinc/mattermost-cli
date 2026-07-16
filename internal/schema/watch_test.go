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
	for _, mutation := range []string{strings.Replace(validDiagnostic, `"backfill":false`, `"backfill":true`, 1), strings.Replace(validDiagnostic, `,"attempt":1,"delayMs":1000`, `,"expected":1,"received":2`, 1), strings.Replace(validDiagnostic, `"timestamp":`, `"code":"watch_failed","timestamp":`, 1), strings.Replace(validDiagnostic, `"timestamp":`, `"recovery":"none","timestamp":`, 1)} {
		if err := registry.Validate("mm/v2/watch-diagnostic", strings.NewReader(mutation)); err == nil {
			t.Fatalf("accepted mutation %s", mutation)
		}
	}
}

func TestWatchWarningAndTerminalDiagnosticVariantsAreExact(t *testing.T) {
	registry, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	warning := `{"schema":"mm/v2/watch-diagnostic","type":"warning","code":"redaction_disabled","recovery":"none","timestamp":"2026-07-16T00:00:00.000Z","message":"warning","backfill":false,"fatal":false,"redactions":[]}`
	terminal := `{"schema":"mm/v2/watch-diagnostic","type":"terminal","code":"authentication","recovery":"check_token","timestamp":"2026-07-16T00:00:00.000Z","message":"failed","backfill":false,"fatal":true,"redactions":[]}`
	for _, value := range []string{warning, terminal} {
		if err := registry.Validate("mm/v2/watch-diagnostic", strings.NewReader(value)); err != nil {
			t.Fatal(err)
		}
	}
	mutations := []string{strings.Replace(warning, `"recovery":"none"`, `"recovery":"retry_later"`, 1), strings.Replace(warning, `"fatal":false`, `"fatal":true`, 1), strings.Replace(terminal, `"recovery":"check_token"`, `"recovery":"none"`, 1), strings.Replace(terminal, `"code":"authentication"`, `"code":"redaction_disabled"`, 1), strings.Replace(terminal, `"redactions":[]`, `"redactions":[],"attempt":1`, 1)}
	for _, value := range mutations {
		if err := registry.Validate("mm/v2/watch-diagnostic", strings.NewReader(value)); err == nil {
			t.Fatalf("accepted %s", value)
		}
	}
}
