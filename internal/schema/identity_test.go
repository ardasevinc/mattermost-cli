package schema

import (
	"strings"
	"testing"
)

func TestIdentitySchemasRejectMissingAndContradictoryFields(t *testing.T) {
	registry, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		schemaID string
		document string
	}{
		{
			name:     "whoami optional field must be explicit",
			schemaID: "mm/v2/whoami",
			document: `{"schema":"mm/v2/whoami","data":{"id":"u","username":"arda","nickname":null,"roles":[]}}`,
		},
		{
			name:     "whoami roles are strings",
			schemaID: "mm/v2/whoami",
			document: `{"schema":"mm/v2/whoami","data":{"id":"u","username":"arda","displayName":null,"nickname":null,"roles":[7]}}`,
		},
		{
			name:     "whoami absent optional value is null",
			schemaID: "mm/v2/whoami",
			document: `{"schema":"mm/v2/whoami","data":{"id":"u","username":"arda","displayName":"","nickname":null,"roles":[]}}`,
		},
		{
			name:     "team display name must be explicit",
			schemaID: "mm/v2/teams",
			document: `{"schema":"mm/v2/teams","teams":[{"id":"t","name":"core","type":"open"}]}`,
		},
		{
			name:     "unknown team type",
			schemaID: "mm/v2/teams",
			document: `{"schema":"mm/v2/teams","teams":[{"id":"t","name":"core","displayName":null,"type":"private"}]}`,
		},
		{
			name:     "team absent display name is null",
			schemaID: "mm/v2/teams",
			document: `{"schema":"mm/v2/teams","teams":[{"id":"t","name":"core","displayName":"","type":"open"}]}`,
		},
		{
			name:     "users require retrieval",
			schemaID: "mm/v2/users",
			document: `{"schema":"mm/v2/users","users":[]}`,
		},
		{
			name:     "users preserve unknown truncation only as null",
			schemaID: "mm/v2/users",
			document: `{"schema":"mm/v2/users","users":[],"retrieval":{"selectedCount":0,"requestedLimit":20,"query":null,"teamId":null,"truncated":"unknown"}}`,
		},
		{
			name:     "users absent query is null",
			schemaID: "mm/v2/users",
			document: `{"schema":"mm/v2/users","users":[],"retrieval":{"selectedCount":0,"requestedLimit":20,"query":"","teamId":null,"truncated":false}}`,
		},
		{
			name:     "direct channel cannot carry a team",
			schemaID: "mm/v2/channels",
			document: `{"schema":"mm/v2/channels","channels":[{"id":"c","type":"dm","name":"@arda","displayName":null,"team":{"id":"t","name":"core","displayName":null},"lastPost":null,"messageCount":0}]}`,
		},
		{
			name:     "direct channel has no separate display name",
			schemaID: "mm/v2/channels",
			document: `{"schema":"mm/v2/channels","channels":[{"id":"c","type":"dm","name":"@arda","displayName":"Arda","team":null,"lastPost":null,"messageCount":0}]}`,
		},
		{
			name:     "public channel requires proven team",
			schemaID: "mm/v2/channels",
			document: `{"schema":"mm/v2/channels","channels":[{"id":"c","type":"public","name":"town-square","displayName":null,"team":null,"lastPost":null,"messageCount":0}]}`,
		},
		{
			name:     "channel absent display name is null",
			schemaID: "mm/v2/channels",
			document: `{"schema":"mm/v2/channels","channels":[{"id":"c","type":"public","name":"town-square","displayName":"","team":{"id":"t","name":"core","displayName":null},"lastPost":null,"messageCount":0}]}`,
		},
		{
			name:     "last post is canonical UTC milliseconds",
			schemaID: "mm/v2/channels",
			document: `{"schema":"mm/v2/channels","channels":[{"id":"c","type":"group","name":"group","displayName":null,"team":null,"lastPost":"2026-07-16T10:20:30Z","messageCount":0}]}`,
		},
		{
			name:     "message count is nonnegative",
			schemaID: "mm/v2/channels",
			document: `{"schema":"mm/v2/channels","channels":[{"id":"c","type":"group","name":"group","displayName":null,"team":null,"lastPost":null,"messageCount":-1}]}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := registry.Validate(test.schemaID, strings.NewReader(test.document)); err == nil {
				t.Fatalf("accepted contradictory %s document: %s", test.schemaID, test.document)
			}
		})
	}
}
