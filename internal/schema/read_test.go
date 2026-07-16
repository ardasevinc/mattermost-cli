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

func TestReadSchemaRejectsInvalidThreadTimestamps(t *testing.T) {
	registry, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	valid, err := fs.ReadFile(publicschemas.FS, "v2/examples/thread.json")
	if err != nil {
		t.Fatal(err)
	}
	for name, replacement := range map[string]string{
		"impossible date": `"timestamp":"2026-02-30T01:02:03.456Z"`,
		"short year":      `"timestamp":"026-07-16T01:02:03.456Z"`,
		"year zero":       `"timestamp":"0000-07-16T01:02:03.456Z"`,
	} {
		document := string(valid)
		document = strings.Replace(document, `"timestamp":"2026-07-16T01:02:03.456Z"`, replacement, 1)
		if err := registry.Validate("mm/v2/thread", strings.NewReader(document)); err == nil {
			t.Errorf("accepted %s", name)
		}
	}
}

func TestThreadSchemaEnforcesRootAndUnboundShape(t *testing.T) {
	registry, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	valid, err := fs.ReadFile(publicschemas.FS, "v2/examples/thread.json")
	if err != nil {
		t.Fatal(err)
	}

	decode := func(t *testing.T) map[string]any {
		t.Helper()
		var document map[string]any
		if err := json.Unmarshal(valid, &document); err != nil {
			t.Fatal(err)
		}
		return document
	}
	validate := func(t *testing.T, document map[string]any, wantValid bool) {
		t.Helper()
		raw, err := json.Marshal(document)
		if err != nil {
			t.Fatal(err)
		}
		err = registry.Validate("mm/v2/thread", strings.NewReader(string(raw)))
		if wantValid && err != nil {
			t.Fatalf("valid thread document rejected: %v", err)
		}
		if !wantValid && err == nil {
			t.Fatalf("invalid thread document accepted: %s", raw)
		}
	}
	data := func(document map[string]any) map[string]any {
		return document["data"].(map[string]any)
	}

	t.Run("root must be canonical root", func(t *testing.T) {
		document := decode(t)
		data(document)["root"].(map[string]any)["rootId"] = "other"
		validate(t, document, false)
	})

	t.Run("unbound post cannot contain nested replies", func(t *testing.T) {
		document := decode(t)
		thread := data(document)
		unbound := thread["root"].(map[string]any)
		nested := data(decode(t))["root"].(map[string]any)
		unbound["rootId"] = "missing-root"
		unbound["replies"] = []any{nested}
		thread["root"] = nil
		thread["unboundPosts"] = []any{unbound}
		metadata := thread["metadata"].(map[string]any)
		metadata["completeness"] = "unknown"
		metadata["visibleThreads"].(map[string]any)["status"] = "partial"
		validate(t, document, false)
	})

	t.Run("unbound post requires partial hydration", func(t *testing.T) {
		document := decode(t)
		thread := data(document)
		unbound := thread["root"].(map[string]any)
		unbound["rootId"] = "missing-root"
		thread["root"] = nil
		thread["unboundPosts"] = []any{unbound}
		thread["metadata"].(map[string]any)["completeness"] = "unknown"
		validate(t, document, false)
	})

	t.Run("rootless partial output is valid", func(t *testing.T) {
		document := decode(t)
		thread := data(document)
		unbound := thread["root"].(map[string]any)
		unbound["rootId"] = "missing-root"
		thread["root"] = nil
		thread["unboundPosts"] = []any{unbound}
		metadata := thread["metadata"].(map[string]any)
		metadata["completeness"] = "unknown"
		metadata["selection"].(map[string]any)["queryTruncated"] = nil
		visible := metadata["visibleThreads"].(map[string]any)
		visible["status"] = "partial"
		visible["hydratedRootCount"] = float64(0)
		visible["failedRootIds"] = []any{"missing-root"}
		validate(t, document, true)
	})

	t.Run("complete retrieval cannot report partial hydration", func(t *testing.T) {
		document := decode(t)
		visible := data(document)["metadata"].(map[string]any)["visibleThreads"].(map[string]any)
		visible["status"] = "partial"
		visible["hydratedRootCount"] = float64(0)
		visible["failedRootIds"] = []any{"p1"}
		validate(t, document, false)
	})

	t.Run("truncated retrieval cannot report complete hydration", func(t *testing.T) {
		document := decode(t)
		metadata := data(document)["metadata"].(map[string]any)
		metadata["completeness"] = "truncated"
		metadata["selection"].(map[string]any)["queryTruncated"] = true
		validate(t, document, false)
	})

	t.Run("query truncation must match completeness", func(t *testing.T) {
		document := decode(t)
		data(document)["metadata"].(map[string]any)["selection"].(map[string]any)["queryTruncated"] = true
		validate(t, document, false)
	})
}

func TestSearchSchemaBindsSourceAndCompleteness(t *testing.T) {
	registry, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	valid, err := fs.ReadFile(publicschemas.FS, "v2/examples/search.json")
	if err != nil {
		t.Fatal(err)
	}
	for name, document := range map[string]string{
		"wrong source":          strings.Replace(string(valid), `"source":"search"`, `"source":"recent"`, 1),
		"complete is truncated": strings.Replace(string(valid), `"queryTruncated":false`, `"queryTruncated":true`, 1),
		"complete is unknown":   strings.Replace(string(valid), `"queryTruncated":false`, `"queryTruncated":null`, 1),
	} {
		if err := registry.Validate("mm/v2/search", strings.NewReader(document)); err == nil {
			t.Errorf("accepted %s", name)
		}
	}
}

func TestMentionsSchemaBindsSourceAndCompleteness(t *testing.T) {
	registry, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	valid, err := fs.ReadFile(publicschemas.FS, "v2/examples/mentions.json")
	if err != nil {
		t.Fatal(err)
	}
	for name, document := range map[string]string{
		"wrong source":          strings.Replace(string(valid), `"source":"mentions"`, `"source":"search"`, 1),
		"complete is truncated": strings.Replace(string(valid), `"queryTruncated":false`, `"queryTruncated":true`, 1),
		"complete is unknown":   strings.Replace(string(valid), `"queryTruncated":false`, `"queryTruncated":null`, 1),
	} {
		if err := registry.Validate("mm/v2/mentions", strings.NewReader(document)); err == nil {
			t.Errorf("accepted %s", name)
		}
	}
}
