package schema

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"

	publicschemas "github.com/ardasevinc/mattermost-cli/v2/schemas"
)

func TestReadAndValidateReturnsExactDocument(t *testing.T) {
	r, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	document := []byte(" \n" + `{"schema":"mm/v2/stage-cancel-request","requestId":"r","stageId":"stg_abcdefghijklmnopqrstuvwxyzABCDEF","expectedRevision":1,"expectedDigest":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}` + "\n")
	got, err := r.ReadAndValidate("mm/v2/stage-cancel-request", bytes.NewReader(document))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, document) {
		t.Fatalf("bytes changed: %q", got)
	}
	got[0] = 'x'
	second, err := r.ReadAndValidate("mm/v2/stage-cancel-request", bytes.NewReader(document))
	if err != nil || !bytes.Equal(second, document) {
		t.Fatalf("returned bytes were not independent: %q, %v", second, err)
	}
}

func TestValidateRejectsInvalidUnicodeWithoutReplacement(t *testing.T) {
	r, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	base := `{"schema":"mm/v2/stage-request","persist":true,"requestId":"r","operation":"edit_post","target":{"kind":"post","postId":"p"},"body":"%s","emoji":null,"attachments":[]}`
	invalidUTF8 := []byte(fmt.Sprintf(base, "x"))
	invalidUTF8[bytes.Index(invalidUTF8, []byte(`"x"`))+1] = 0xff
	for name, document := range map[string][]byte{
		"raw invalid UTF-8":   invalidUTF8,
		"lone high surrogate": []byte(fmt.Sprintf(base, `\ud800`)),
		"lone low surrogate":  []byte(fmt.Sprintf(base, `\udc00`)),
	} {
		t.Run(name, func(t *testing.T) {
			if err := r.Validate("mm/v2/stage-request", bytes.NewReader(document)); err == nil {
				t.Fatal("invalid Unicode accepted")
			}
		})
	}
}

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

func TestStoreSchemasRejectSemanticContradictions(t *testing.T) {
	registry, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	absent := `{"schema":"mm/v2/store-doctor","path":"/tmp/stages.sqlite3","report":{"exists":false,"filesystemSafe":false,"applicationId":null,"integrity":null,"integrityTruncated":null,"foreignKeyIssues":null,"foreignKeyRows":null,"foreignKeyTruncated":null,"migrations":{"applied":null,"latest":3,"valid":null},"journalMode":null,"synchronous":null,"secureDelete":null,"foreignKeys":null,"trustedSchema":null,"queryOnly":null,"walFallback":null,"permissionModelLimitations":[]}}`
	issueWithoutRow := `{"schema":"mm/v2/store-doctor","path":"/tmp/stages.sqlite3","report":{"exists":true,"filesystemSafe":true,"applicationId":1296913970,"integrity":["ok"],"integrityTruncated":false,"foreignKeyIssues":1,"foreignKeyRows":[],"foreignKeyTruncated":false,"migrations":{"applied":3,"latest":3,"valid":true},"journalMode":"wal","synchronous":2,"secureDelete":2,"foreignKeys":true,"trustedSchema":false,"queryOnly":true,"walFallback":false,"permissionModelLimitations":[]}}`
	contradictoryWAL := `{"schema":"mm/v2/store-doctor","path":"/tmp/stages.sqlite3","report":{"exists":true,"filesystemSafe":true,"applicationId":1296913970,"integrity":["ok"],"integrityTruncated":false,"foreignKeyIssues":0,"foreignKeyRows":[],"foreignKeyTruncated":false,"migrations":{"applied":3,"latest":3,"valid":true},"journalMode":"wal","synchronous":2,"secureDelete":2,"foreignKeys":true,"trustedSchema":false,"queryOnly":true,"walFallback":true,"permissionModelLimitations":[]}}`
	twentyRows := `"row",` + strings.Repeat(`"row",`, 18) + `"row"`
	unmarkedTruncation := `{"schema":"mm/v2/store-doctor","path":"/tmp/stages.sqlite3","report":{"exists":true,"filesystemSafe":true,"applicationId":1296913970,"integrity":["ok"],"integrityTruncated":false,"foreignKeyIssues":21,"foreignKeyRows":[` + twentyRows + `],"foreignKeyTruncated":false,"migrations":{"applied":3,"latest":3,"valid":true},"journalMode":"wal","synchronous":2,"secureDelete":2,"foreignKeys":true,"trustedSchema":false,"queryOnly":true,"walFallback":false,"permissionModelLimitations":[]}}`
	mixedIntegrity := `{"schema":"mm/v2/store-doctor","path":"/tmp/stages.sqlite3","report":{"exists":true,"filesystemSafe":true,"applicationId":1296913970,"integrity":["ok","damaged"],"integrityTruncated":false,"foreignKeyIssues":0,"foreignKeyRows":[],"foreignKeyTruncated":false,"migrations":{"applied":3,"latest":3,"valid":true},"journalMode":"wal","synchronous":2,"secureDelete":2,"foreignKeys":true,"trustedSchema":false,"queryOnly":true,"walFallback":false,"permissionModelLimitations":[]}}`
	migration3 := `{"version":3,"name":"caller-intent-stage-create-replay","checksum":"237315b734e034951a7394ef708273af5f91ea6a44abe9927537571fbcee78d5"}`
	wrongLatest := `{"schema":"mm/v2/store-migrations","latest":1,"migrations":[{"version":1,"name":"core-stage-state","checksum":"e69a3e2524903dbdb4ef5a9691ce8674fac8fe284d09e95bc5983ec9e9fef92f"},{"version":2,"name":"immutable-local-request-receipts","checksum":"ac1c291e7201786935cce68dd459f61150048c505e4f29ad813304c309009b8c"},` + migration3 + `]}`
	wrongOrder := `{"schema":"mm/v2/store-migrations","latest":3,"migrations":[{"version":2,"name":"immutable-local-request-receipts","checksum":"ac1c291e7201786935cce68dd459f61150048c505e4f29ad813304c309009b8c"},{"version":1,"name":"core-stage-state","checksum":"e69a3e2524903dbdb4ef5a9691ce8674fac8fe284d09e95bc5983ec9e9fef92f"},` + migration3 + `]}`
	for id, documents := range map[string][]string{"mm/v2/store-doctor": {absent, issueWithoutRow, contradictoryWAL, unmarkedTruncation, mixedIntegrity}, "mm/v2/store-migrations": {wrongLatest, wrongOrder}} {
		for _, document := range documents {
			if err := registry.Validate(id, strings.NewReader(document)); err == nil {
				t.Fatalf("%s accepted contradiction: %s", id, document)
			}
		}
	}
}
