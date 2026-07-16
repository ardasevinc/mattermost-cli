package schema

import (
	"bytes"
	"encoding/json"
	"errors"
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

func TestValidateTypesOnlyPhysicalInputReadFailures(t *testing.T) {
	registry, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	physical := errors.New("fixture read failure")
	err = registry.Validate("mm/v2/error", errorReader{err: physical})
	if !IsInputReadError(err) || !errors.Is(err, physical) || strings.Contains(err.Error(), physical.Error()) {
		t.Fatalf("physical read error = %v", err)
	}
	for _, input := range []string{"{", `{}`} {
		if err := registry.Validate("mm/v2/error", strings.NewReader(input)); err == nil || IsInputReadError(err) {
			t.Fatalf("content error incorrectly typed as input read failure: %v", err)
		}
	}
}

type errorReader struct{ err error }

func (r errorReader) Read([]byte) (int, error) { return 0, r.err }

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

func TestDoctorSchemaRejectsSemanticContradictions(t *testing.T) {
	registry, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	configuration := `{"name":"configuration","status":"pass","message":"credentials resolved","details":{"urlSource":"cli","tokenSource":"env"}}`
	health := `{"name":"server","status":"pass","message":"healthy","details":{"status":"OK","databaseStatus":"OK","filestoreStatus":"OK"}}`
	auth := `{"name":"authentication","status":"pass","message":"authenticated","details":{"id":"id","username":"arda"}}`
	documents := []string{
		`{"schema":"mm/v2/doctor","ok":true,"checks":[` + configuration + `,{"name":"server","status":"fail","message":"failed"},` + auth + `]}`,
		`{"schema":"mm/v2/doctor","ok":false,"checks":[` + configuration + `,` + health + `,` + auth + `]}`,
		`{"schema":"mm/v2/doctor","ok":true,"checks":[` + configuration + `,{"name":"server","status":"pass","message":"healthy"},` + auth + `]}`,
		`{"schema":"mm/v2/doctor","ok":false,"checks":[` + configuration + `,{"name":"server","status":"skipped","message":"missing","details":{"httpStatus":503}},{"name":"authentication","status":"fail","message":"failed"}]}`,
		`{"schema":"mm/v2/doctor","ok":true,"checks":[` + configuration + `,` + health + `,{"name":"authentication","status":"pass","message":"authenticated","details":{"httpStatus":200}}]}`,
		`{"schema":"mm/v2/doctor","ok":false,"checks":[` + configuration + `,` + health + `,{"name":"authentication","status":"fail","message":"failed","details":{"id":"id","username":"arda"}}]}`,
		`{"schema":"mm/v2/doctor","ok":true,"checks":[{"name":"configuration","status":"pass","message":"bad\u001bvalue","details":{"urlSource":"cli","tokenSource":"env"}},` + health + `,` + auth + `]}`,
		`{"schema":"mm/v2/doctor","ok":true,"checks":[` + configuration + `,{"name":"server","status":"pass","message":"healthy","details":{"status":"OK","databaseStatus":"bad\u202evalue","filestoreStatus":"OK"}},` + auth + `]}`,
		`{"schema":"mm/v2/doctor","ok":true,"checks":[` + configuration + `,` + health + `,{"name":"authentication","status":"pass","message":"authenticated","details":{"id":"id","username":"bad\u0085value"}}]}`,
	}
	for _, document := range documents {
		if err := registry.Validate("mm/v2/doctor", strings.NewReader(document)); err == nil {
			t.Fatalf("doctor schema accepted contradiction: %s", document)
		}
	}
}
