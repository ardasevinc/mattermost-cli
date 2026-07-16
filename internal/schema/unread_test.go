package schema

import (
	"bytes"
	"io/fs"
	"strings"
	"testing"

	publicschemas "github.com/ardasevinc/mattermost-cli/schemas"
)

func TestUnreadSchemaRequiresNonNullArraysAndHonestHistory(t *testing.T) {
	registry, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, document := range []string{
		`{"schema":"mm/v2/unread","data":{"unread":null,"peek":[]}}`,
		`{"schema":"mm/v2/unread","data":{"unread":[],"peek":null}}`,
		`{"schema":"mm/v2/unread","data":{"unread":[{"channel":{},"unreadCount":0,"mentionCount":-1,"lastViewedAt":null}],"peek":[]}}`,
	} {
		if err := registry.Validate("mm/v2/unread", strings.NewReader(document)); err == nil {
			t.Fatalf("accepted %s", document)
		}
	}
}

func TestUnreadSchemaRejectsEveryPeekRestrictionMutation(t *testing.T) {
	registry, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	valid, err := fs.ReadFile(publicschemas.FS, "v2/examples/unread.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Validate("mm/v2/unread", bytes.NewReader(valid)); err != nil {
		t.Fatal(err)
	}
	mutations := map[string][2]string{
		"unresolved":             {`"metadataStatus":"resolved"`, `"metadataStatus":"unavailable"`},
		"unknown channel":        {`"type":"public","name":"town-square","displayName":"Town Square","metadataStatus":"resolved"`, `"type":"unknown","name":"town-square","displayName":"Town Square","metadataStatus":"unavailable"`},
		"missing limit":          {`"requestedLimit":2`, `"requestedLimit":null`},
		"unsafe limit":           {`"requestedLimit":2`, `"requestedLimit":9007199254740992`},
		"bad since":              {`"since":"2026-07-16T00:00:00.000Z"`, `"since":"not-a-time"`},
		"input cursor":           {`"inputCursor":null`, `"inputCursor":"cursor"`},
		"next cursor":            {`"nextCursor":null`, `"nextCursor":"cursor"`},
		"unknown truncation":     {`"queryTruncated":false`, `"queryTruncated":null`},
		"thread status":          {`"status":"not_requested"`, `"status":"complete"`},
		"hydrated":               {`"hydratedRootCount":0`, `"hydratedRootCount":1`},
		"failed root":            {`"failedRootIds":[]`, `"failedRootIds":["p1"]`},
		"unknown completeness":   {`"completeness":"complete"`, `"completeness":"unknown"`},
		"contradictory complete": {`"queryTruncated":false`, `"queryTruncated":true`},
	}
	for name, replacement := range mutations {
		t.Run(name, func(t *testing.T) {
			document := strings.Replace(string(valid), replacement[0], replacement[1], 1)
			if document == string(valid) {
				t.Fatal("mutation did not apply")
			}
			if err := registry.Validate("mm/v2/unread", strings.NewReader(document)); err == nil {
				t.Fatal("mutation accepted")
			}
		})
	}
}
