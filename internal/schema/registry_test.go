package schema

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"strings"
	"testing"

	publicschemas "github.com/ardasevinc/mattermost-cli/schemas"
)

func TestEmbeddedExamplesValidate(t *testing.T) {
	registry, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	paths, err := fs.Glob(publicschemas.FS, "v2/examples/*.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no embedded schema examples")
	}
	for _, path := range paths {
		data, err := fs.ReadFile(publicschemas.FS, path)
		if err != nil {
			t.Fatal(err)
		}
		var envelope struct {
			Schema string `json:"schema"`
		}
		if err := json.Unmarshal(data, &envelope); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		if err := registry.Validate(envelope.Schema, bytes.NewReader(data)); err != nil {
			t.Fatalf("validate %s: %v", path, err)
		}
	}
}

func TestValidateRejectsUnknownFields(t *testing.T) {
	registry, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	document := `{"schema":"mm/v2/error","code":"internal","message":"failed","exitCode":3,"secret":"nope"}`

	err = registry.Validate("mm/v2/error", strings.NewReader(document))

	if err == nil || !strings.Contains(err.Error(), "document does not match mm/v2/error") {
		t.Fatalf("Validate() error = %v, want unknown-field rejection", err)
	}
}

func TestValidateRejectsTrailingJSON(t *testing.T) {
	registry, err := Load()
	if err != nil {
		t.Fatal(err)
	}

	err = registry.Validate("mm/v2/error", strings.NewReader(`{} {}`))

	if err == nil || !strings.Contains(err.Error(), "trailing JSON value") {
		t.Fatalf("Validate() error = %v, want trailing-value rejection", err)
	}
}

func TestErrorSchemaBindsCodesToExitClassesAndRequiresRecovery(t *testing.T) {
	registry, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, document := range []string{
		`{"schema":"mm/v2/error","code":"authentication","message":"failed","exitCode":7,"recovery":"none"}`,
		`{"schema":"mm/v2/error","code":"state_conflict","message":"failed","exitCode":6}`,
	} {
		if err := registry.Validate("mm/v2/error", strings.NewReader(document)); err == nil {
			t.Fatalf("Validate() accepted contradictory error envelope: %s", document)
		}
	}
}
