package schema

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"testing"

	publicschemas "github.com/ardasevinc/mattermost-cli/schemas"
)

func TestApplySchemasAndExamples(t *testing.T) {
	registry, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"apply-request.json", "apply-receipt.json"} {
		raw, readErr := fs.ReadFile(publicschemas.FS, "v2/examples/"+name)
		if readErr != nil {
			t.Fatal(readErr)
		}
		var envelope struct {
			Schema string `json:"schema"`
		}
		if json.Unmarshal(raw, &envelope) != nil {
			t.Fatalf("decode apply example %s", name)
		}
		if validateErr := registry.Validate(envelope.Schema, bytes.NewReader(raw)); validateErr != nil {
			if envelope.Schema == "mm/v2/apply-receipt" {
				var receipt applyReceiptDocument
				_ = json.Unmarshal(raw, &receipt)
				var document any
				decoder := json.NewDecoder(bytes.NewReader(raw))
				decoder.UseNumber()
				_ = decoder.Decode(&document)
				t.Fatalf("invalid apply example %s: structural=%v semantic=%v", name, registry.compiled[envelope.Schema].Validate(document), validateApplyReceiptDocument(receipt))
			}
			t.Fatalf("invalid apply example %s", name)
		}
	}
}

func TestApplySchemasRejectUnsafeOrContradictoryDocuments(t *testing.T) {
	registry, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	request, _ := fs.ReadFile(publicschemas.FS, "v2/examples/apply-request.json")
	receipt, _ := fs.ReadFile(publicschemas.FS, "v2/examples/apply-receipt.json")
	cases := []struct {
		id  string
		raw []byte
	}{
		{"mm/v2/apply-request", bytes.Replace(request, []byte(`"ordinary"`), []byte(`"unsafe"`), 1)},
		{"mm/v2/apply-request", bytes.Replace(request, []byte(`"requestId":"apply-2026-07-17-1"`), []byte(`"requestId":""`), 1)},
		{"mm/v2/apply-request", bytes.Replace(request, []byte(`"requestId":"apply-2026-07-17-1"`), []byte(`"requestId":".apply"`), 1)},
		{"mm/v2/apply-receipt", bytes.Replace(receipt, []byte(`"forcedDuplicateRisk":false`), []byte(`"forcedDuplicateRisk":true`), 1)},
		{"mm/v2/apply-receipt", bytes.Replace(receipt, []byte(`"recovery":"forbidden"`), []byte(`"recovery":"none"`), 1)},
	}
	for _, tc := range cases {
		if err := registry.Validate(tc.id, bytes.NewReader(tc.raw)); err == nil {
			t.Fatalf("accepted contradictory %s: %s", tc.id, tc.raw)
		}
	}
}

func TestApplyRequestAcceptsStoreRequestIDGrammar(t *testing.T) {
	registry, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := fs.ReadFile(publicschemas.FS, "v2/examples/apply-request.json")
	if err != nil {
		t.Fatal(err)
	}
	raw = bytes.Replace(raw, []byte(`"requestId":"apply-2026-07-17-1"`), []byte(`"requestId":"a~b"`), 1)
	if err := registry.Validate("mm/v2/apply-request", bytes.NewReader(raw)); err != nil {
		t.Fatalf("rejected store-valid request ID: %s", raw)
	}
}

func TestApplyReceiptBindsOutcomeResultAndOperation(t *testing.T) {
	registry, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := fs.ReadFile(publicschemas.FS, "v2/examples/apply-receipt.json")
	if err != nil {
		t.Fatal(err)
	}
	var base map[string]any
	if err := json.Unmarshal(raw, &base); err != nil {
		t.Fatal(err)
	}

	cases := map[string]func(map[string]any){
		"succeeded with pending step": func(doc map[string]any) {
			step := doc["steps"].([]any)[0].(map[string]any)
			step["state"] = "pending"
			step["result"] = nil
			step["startedAt"] = nil
			step["endedAt"] = nil
		},
		"partial without recovery": func(doc map[string]any) {
			doc["outcome"] = "partial"
			doc["recovery"] = "none"
		},
		"validated create with status result": func(doc map[string]any) {
			doc["steps"].([]any)[0].(map[string]any)["result"] = map[string]any{"status": float64(409)}
		},
		"rejected create with success result": func(doc map[string]any) {
			doc["outcome"] = "rejected"
			doc["recovery"] = "none"
			doc["steps"].([]any)[0].(map[string]any)["state"] = "rejected"
		},
		"edit operation with create plan": func(doc map[string]any) {
			doc["operation"] = "edit_post"
		},
		"unknown with pending residue": func(doc map[string]any) {
			doc["outcome"] = "unknown"
			doc["recovery"] = "force_unknown"
			doc["steps"] = []any{
				map[string]any{"ordinal": float64(1), "kind": "upload_attachment", "condition": "always", "state": "outcome_unknown", "result": nil, "startedAt": "2026-07-17T02:00:00Z", "endedAt": "2026-07-17T02:00:01Z"},
				map[string]any{"ordinal": float64(2), "kind": "create_post", "condition": "always", "state": "pending", "result": nil, "startedAt": nil, "endedAt": nil},
			}
		},
		"partial with multiple rejections": func(doc map[string]any) {
			doc["outcome"] = "partial"
			doc["recovery"] = "resume_partial"
			doc["steps"] = []any{
				map[string]any{"ordinal": float64(1), "kind": "upload_attachment", "condition": "always", "state": "rejected", "result": map[string]any{"status": float64(409)}, "startedAt": "2026-07-17T02:00:00Z", "endedAt": "2026-07-17T02:00:01Z"},
				map[string]any{"ordinal": float64(2), "kind": "upload_attachment", "condition": "always", "state": "rejected", "result": map[string]any{"status": float64(409)}, "startedAt": "2026-07-17T02:00:00Z", "endedAt": "2026-07-17T02:00:01Z"},
				doc["steps"].([]any)[0],
			}
			doc["steps"].([]any)[2].(map[string]any)["ordinal"] = float64(3)
		},
		"ordinary rejection with unknown recovery": func(doc map[string]any) {
			doc["outcome"] = "rejected"
			doc["recovery"] = "force_unknown"
			step := doc["steps"].([]any)[0].(map[string]any)
			step["state"] = "rejected"
			step["result"] = map[string]any{"status": float64(409)}
		},
		"validated effect after rejection": func(doc map[string]any) {
			doc["outcome"] = "partial"
			doc["recovery"] = "resume_partial"
			doc["steps"] = []any{
				map[string]any{"ordinal": float64(1), "kind": "upload_attachment", "condition": "always", "state": "rejected", "result": map[string]any{"status": float64(409)}, "startedAt": "2026-07-17T02:00:00Z", "endedAt": "2026-07-17T02:00:01Z"},
				doc["steps"].([]any)[0],
			}
			doc["steps"].([]any)[1].(map[string]any)["ordinal"] = float64(2)
		},
		"validated effect after not dispatched": func(doc map[string]any) {
			doc["outcome"] = "partial"
			doc["recovery"] = "resume_partial"
			doc["steps"] = []any{
				map[string]any{"ordinal": float64(1), "kind": "upload_attachment", "condition": "always", "state": "not_dispatched", "result": nil, "startedAt": nil, "endedAt": "2026-07-17T02:00:01Z"},
				doc["steps"].([]any)[0],
			}
			doc["steps"].([]any)[1].(map[string]any)["ordinal"] = float64(2)
		},
		"create before upload": func(doc map[string]any) {
			doc["steps"] = append(doc["steps"].([]any), map[string]any{"ordinal": float64(2), "kind": "upload_attachment", "condition": "always", "state": "response_validated", "result": map[string]any{"fileId": "file-1"}, "startedAt": "2026-07-17T02:00:00Z", "endedAt": "2026-07-17T02:00:01Z"})
		},
		"reply without post binding": func(doc map[string]any) {
			doc["operation"] = "reply"
			destination := doc["destination"].(map[string]any)
			destination["kind"] = "post"
			destination["postId"] = nil
			destination["rootPostId"] = nil
		},
		"dm resolution with group target": func(doc map[string]any) {
			doc["operation"] = "resolve_dm"
			destination := doc["destination"].(map[string]any)
			destination["channelId"] = nil
			destination["channelType"] = "group"
			destination["participantIds"] = []any{"user-2", "user-3", "user-4"}
			doc["steps"] = []any{map[string]any{"ordinal": float64(1), "kind": "resolve_conversation", "condition": "if_missing", "state": "response_validated", "result": map[string]any{"channelId": "channel-2", "participantIds": []any{"user-2", "user-3", "user-4"}}, "startedAt": "2026-07-17T02:00:00Z", "endedAt": "2026-07-17T02:00:01Z"}}
		},
		"create result for another channel": func(doc map[string]any) {
			doc["steps"].([]any)[0].(map[string]any)["result"].(map[string]any)["channelId"] = "channel-2"
		},
		"edit result for another post": func(doc map[string]any) {
			makeEditReceipt(doc, "post-2")
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			var doc map[string]any
			encoded, marshalErr := json.Marshal(base)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if unmarshalErr := json.Unmarshal(encoded, &doc); unmarshalErr != nil {
				t.Fatal(unmarshalErr)
			}
			mutate(doc)
			encoded, marshalErr = json.Marshal(doc)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if validateErr := registry.Validate("mm/v2/apply-receipt", bytes.NewReader(encoded)); validateErr == nil {
				t.Fatalf("accepted contradictory receipt: %s", encoded)
			}
		})
	}
}

func TestApplyReceiptAcceptsRealisticEditProjection(t *testing.T) {
	registry, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := fs.ReadFile(publicschemas.FS, "v2/examples/apply-receipt.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	makeEditReceipt(doc, "post-1")
	encoded, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Validate("mm/v2/apply-receipt", bytes.NewReader(encoded)); err != nil {
		t.Fatalf("rejected realistic edit receipt: %s", encoded)
	}
}

func makeEditReceipt(doc map[string]any, resultPostID string) {
	doc["operation"] = "edit_post"
	destination := doc["destination"].(map[string]any)
	destination["kind"] = "post"
	destination["channelType"] = "private"
	destination["teamId"] = "team-1"
	destination["participantIds"] = []any{}
	destination["postId"] = "post-1"
	destination["rootPostId"] = nil
	destination["postState"] = map[string]any{"authorUserId": "user-1", "updateAt": float64(1784253599000), "contentDigest": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}
	doc["steps"] = []any{map[string]any{"ordinal": float64(1), "kind": "edit_post", "condition": "always", "state": "response_validated", "result": map[string]any{"postId": resultPostID, "updateAt": float64(1784253600000)}, "startedAt": "2026-07-17T02:00:00Z", "endedAt": "2026-07-17T02:00:01Z"}}
}
