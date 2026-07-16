package schema

import (
	"encoding/json"
	"io/fs"
	"strings"
	"testing"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"

	publicschemas "github.com/ardasevinc/mattermost-cli/schemas"
)

func TestReadSchemasAreRegisteredAndStrict(t *testing.T) {
	registry, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"dms", "group-dms", "channel", "thread", "search", "mentions"} {
		id := "mm/v2/" + command
		if _, err := registry.Show(id); err != nil {
			t.Fatalf("Show(%q): %v", id, err)
		}
	}
	if _, err := registry.Show("mm/v2/read-defs"); err == nil {
		t.Fatal("shared definitions resource must not be public")
	}
	for _, id := range registry.IDs() {
		if !strings.HasPrefix(id, "mm/v2/") || id == "mm/v2/error" {
			continue
		}
		raw, err := registry.Show(id)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), `"$ref":"urn:`) {
			t.Fatalf("Show(%q) returned a schema with an external reference", id)
		}
		var document any
		if err := json.Unmarshal(raw, &document); err != nil {
			t.Fatal(err)
		}
		compiler := jsonschema.NewCompiler()
		compiler.AssertFormat()
		if err := compiler.AddResource("standalone.json", document); err != nil {
			t.Fatalf("standalone AddResource(%q): %v", id, err)
		}
		if _, err := compiler.Compile("standalone.json"); err != nil {
			t.Fatalf("standalone Compile(%q): %v", id, err)
		}
	}

	invalid := map[string]string{
		"mm/v2/dms":       `{"schema":"mm/v2/dms","channels":[],"unknown":true}`,
		"mm/v2/group-dms": `{"schema":"mm/v2/dms","channels":[]}`,
		"mm/v2/channel":   `{"schema":"mm/v2/search","data":{}}`,
		"mm/v2/thread":    `{"schema":"mm/v2/thread","data":{"root":null,"replies":[],"metadata":{"completeness":"maybe","nextCursor":null,"queryTruncated":null}}}`,
		"mm/v2/search":    `{"schema":"mm/v2/search","results":[],"metadata":{"completeness":"complete","nextCursor":null,"queryTruncated":null,"unknown":true}}`,
		"mm/v2/mentions":  `{"schema":"mm/v2/search","results":[],"metadata":{"completeness":"complete","nextCursor":null,"queryTruncated":null}}`,
	}
	for id, document := range invalid {
		if err := registry.Validate(id, strings.NewReader(document)); err == nil {
			t.Errorf("Validate(%q) accepted %s", id, document)
		}
	}
}

func TestReadSchemaRejectsNullThreadRootAndInvalidTimestamps(t *testing.T) {
	registry, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	valid, err := fs.ReadFile(publicschemas.FS, "v2/examples/thread.json")
	if err != nil {
		t.Fatal(err)
	}
	for name, replacement := range map[string]string{
		"null root":       `"root":null`,
		"impossible date": `"timestamp":"2026-02-30T01:02:03.456Z"`,
		"short year":      `"timestamp":"026-07-16T01:02:03.456Z"`,
		"year zero":       `"timestamp":"0000-07-16T01:02:03.456Z"`,
	} {
		document := string(valid)
		switch name {
		case "null root":
			start := strings.Index(document, `"root":{`)
			end := strings.Index(document[start:], `,"redactions":[]`)
			document = document[:start] + replacement + document[start+end:]
		default:
			document = strings.Replace(document, `"timestamp":"2026-07-16T01:02:03.456Z"`, replacement, 1)
		}
		if err := registry.Validate("mm/v2/thread", strings.NewReader(document)); err == nil {
			t.Errorf("accepted %s", name)
		}
	}
}
