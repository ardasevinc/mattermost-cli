package schema

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"reflect"
	"strconv"
	"strings"
	"testing"

	publicschemas "github.com/ardasevinc/mattermost-cli/v2/schemas"
)

// These tests deliberately stop at document-local invariants. The runtime must
// enforce stored-operation applicability for revise, contiguous plan ordinals,
// stageRef/revision agreement, timestamp ordering, and identity-only duplicate
// detection across stage-list rows because JSON Schema cannot observe that state.

func TestStageMachineSchemasAreRegistered(t *testing.T) {
	r, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{
		"mm/v2/stage-request", "mm/v2/stage-revise-request", "mm/v2/stage-cancel-request",
		"mm/v2/stages", "mm/v2/stage", "mm/v2/stage-receipt", "mm/v2/stage-preview",
	} {
		if _, err := r.Show(id); err != nil {
			t.Fatalf("Show(%q): %v", id, err)
		}
	}
}

func TestStageMachineSchemasRejectContradictionsAndLeaks(t *testing.T) {
	r, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("a", 64)
	stageID := "stg_0123456789abcdefghijklmnopqrstuv"
	stage := `{"stageId":"` + stageID + `","stageRef":"` + stageID + `@1","revision":1,"operation":"create_post","semanticDigest":"` + digest + `","lifecycle":"open","recovery":"none","createdAt":"2026-07-17T10:00:00.000Z","updatedAt":"2026-07-17T10:00:00.000Z","binding":{"serverUrl":"https://mattermost.example/api/v4","serverId":null,"userId":"user-1"},"destination":{"kind":"conversation","channelId":"channel-1","channelType":"public","teamId":"team-1","postId":null,"rootPostId":null,"participantIds":[],"emoji":null,"postState":null,"reactionPresent":null}}`
	cases := map[string][]string{
		"mm/v2/stage-request": {
			stageRequest(false, "r1", "create_post", conversationTarget("dm", "username", "arda", "null"), "null", "null", `[]`),
			stageRequest(false, "null", "create_post", conversationTarget("dm", "username", "arda", "null"), `"secret"`, "null", `[]`),
			stageRequest(false, "null", "create_post", conversationTarget("dm", "username", "arda", "null"), "null", "null", `[{"path":"/tmp/a","remoteFilename":null,"mediaType":null}]`),
			stageRequest(true, "r1", "create_post", conversationTarget("dm", "id", "user-id", "null"), `"hello"`, "null", `[]`),
			stageRequest(true, "r1", "create_post", conversationTarget("group", "name", "group", "null"), `"hello"`, "null", `[]`),
			stageRequest(true, "r1", "create_post", conversationTarget("channel", "name", "town-square", "null"), `"hello"`, "null", `[]`),
			stageRequest(true, "r1", "reply", `{"kind":"user","username":"arda"}`, `"hello"`, "null", `[]`),
			stageRequest(true, "r1", "delete_post", `{"kind":"post","postId":"p"}`, `"body"`, "null", `[]`),
			stageRequest(true, "r1", "react", `{"kind":"post","postId":"p"}`, "null", "null", `[]`),
			stageRequest(true, "r1", "resolve_dm", `{"kind":"user","username":"arda"}`, "null", `"wave"`, `[]`),
			stageRequest(true, "r1", "resolve_group_dm", `{"kind":"users","usernames":["a","a"]}`, "null", "null", `[]`),
			stageRequest(true, "r1", "resolve_group_dm", `{"kind":"users","usernames":["a"]}`, "null", "null", `[]`),
			stageRequest(true, "r1", "resolve_group_dm", `{"kind":"users","usernames":["a","b","c","d","e","f","g","h"]}`, "null", "null", `[]`),
			stageRequest(true, "r1", "create_post", conversationTarget("dm", "username", "arda", "null"), `"hello"`, "null", `[{"path":"/tmp/a","remoteFilename":null,"mediaType":null,"contentDigest":"`+digest+`"}]`),
		},
		"mm/v2/stages": {
			`{"schema":"mm/v2/stages","stages":[` + strings.Replace(stage, `"recovery":"none"`, `"recovery":"forbidden"`, 1) + `],"nextCursor":null}`,
			`{"schema":"mm/v2/stages","stages":[` + strings.Replace(stage, `"destination":`, `"body":"secret","destination":`, 1) + `],"nextCursor":null}`,
		},
		"mm/v2/stage-receipt": {
			`{"schema":"mm/v2/stage-receipt","action":"canceled","revived":false,"replayed":false,"recordedAt":"2026-07-17T10:00:00.000Z","stage":` + stage + `}`,
			`{"schema":"mm/v2/stage-receipt","action":"created","revived":true,"replayed":false,"recordedAt":"2026-07-17T10:00:00.000Z","stage":` + stage + `}`,
			`{"schema":"mm/v2/stage-receipt","action":"created","revived":false,"replayed":false,"recordedAt":"2026-07-17T10:00:00Z","stage":` + stage + `}`,
			`{"schema":"mm/v2/stage-receipt","action":"created","revived":false,"replayed":false,"recordedAt":"2026-07-17T10:00:00.000Z","stage":` + strings.Replace(stage, `"destination":`, `"plan":[],"destination":`, 1) + `}`,
		},
		"mm/v2/stage-preview": {
			preview("create_post", `{"kind":"reaction","channelId":"c","channelType":"public","teamId":"t","postId":"p","rootPostId":null,"participantIds":[],"emoji":"wave","postState":null,"reactionPresent":true}`, "create_post", true),
			preview("create_post", `{"kind":"conversation","channelId":"c","channelType":"public","teamId":"t","postId":null,"rootPostId":null,"participantIds":[],"emoji":null,"postState":null,"reactionPresent":null}`, "delete_post", false),
			strings.TrimSuffix(preview("create_post", `{"kind":"conversation","channelId":"c","channelType":"public","teamId":"t","postId":null,"rootPostId":null,"participantIds":[],"emoji":null,"postState":null,"reactionPresent":null}`, "create_post", false), "}") + `,"body":"secret"}`,
		},
	}
	for id, documents := range cases {
		for _, document := range documents {
			if err := r.Validate(id, strings.NewReader(document)); err == nil {
				t.Fatalf("%s accepted contradiction: %s", id, document)
			}
		}
	}
}

func preview(operation, destination, stepType string, contentValidated bool) string {
	return `{"schema":"mm/v2/stage-preview","persist":false,"operation":"` + operation + `","binding":{"serverUrl":"https://mattermost.example/api/v4","serverId":null,"userId":"u"},"destination":` + destination + `,"plan":{"steps":[{"ordinal":1,"type":"` + stepType + `","condition":"always"}]},"contentValidated":` + boolJSON(contentValidated) + `}`
}

func TestStageRequestAcceptsEveryTargetBranch(t *testing.T) {
	r, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	valid := []string{
		stageRequest(true, "r1", "create_post", conversationTarget("dm", "username", "arda", "null"), `"hello"`, "null", `[]`),
		stageRequest(true, "r2", "create_post", conversationTarget("group", "id", "channel-id", "null"), `"hello"`, "null", `[]`),
		stageRequest(true, "r3", "create_post", conversationTarget("channel", "id", "channel-id", "null"), `"hello"`, "null", `[]`),
		stageRequest(true, "r4", "create_post", conversationTarget("channel", "name", "town-square", `{"by":"name","value":"vyvo"}`), `"hello"`, "null", `[{"path":"/tmp/a","remoteFilename":null,"mediaType":null}]`),
		stageRequest(true, "r5", "reply", `{"kind":"post","postId":"p"}`, `"hello"`, "null", `[]`),
		stageRequest(true, "r6", "edit_post", `{"kind":"post","postId":"p"}`, `"hello"`, "null", `[]`),
		stageRequest(true, "r7", "delete_post", `{"kind":"post","postId":"p"}`, "null", "null", `[]`),
		stageRequest(true, "r8", "react", `{"kind":"post","postId":"p"}`, "null", `"wave"`, `[]`),
		stageRequest(true, "r9", "unreact", `{"kind":"post","postId":"p"}`, "null", `"wave"`, `[]`),
		stageRequest(true, "r10", "resolve_dm", `{"kind":"user","username":"arda"}`, "null", "null", `[]`),
		stageRequest(true, "r11", "resolve_group_dm", `{"kind":"users","usernames":["arda","hakan"]}`, "null", "null", `[]`),
		stageRequest(true, "r12", "resolve_group_dm", `{"kind":"users","usernames":["a","b","c","d","e","f","g"]}`, "null", "null", `[]`),
		stageRequest(false, "null", "create_post", conversationTarget("channel", "id", "channel-id", "null"), "null", "null", `[]`),
	}
	for _, document := range valid {
		if err := r.Validate("mm/v2/stage-request", strings.NewReader(document)); err != nil {
			t.Fatalf("valid stage request rejected: %v\n%s", err, document)
		}
	}
}

func TestStageSchemasRejectResidualReviewContradictions(t *testing.T) {
	r, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	request := example(t, "stage-request.json")
	preview := example(t, "stage-preview.json")
	show := example(t, "stage.json")
	receipt := example(t, "stage-receipt.json")

	for _, whitespace := range []string{"\u00a0", "\u1680", "\u2000", "\u2028", "\u2029", "\u202f", "\u205f", "\u3000", "\ufeff"} {
		document := strings.Replace(request, `"body":"hello"`, `"body":`+strconv.Quote(whitespace), 1)
		assertInvalid(t, r, "mm/v2/stage-request", document)
	}

	validDestination := `{"kind":"conversation","channelId":"channel-1","channelType":"public","teamId":"team-1","postId":null,"rootPostId":null,"participantIds":[],"emoji":null,"postState":null,"reactionPresent":null}`
	destinationContradictions := []string{
		`{"kind":"conversation","channelId":"channel-1","channelType":"public","teamId":null,"postId":null,"rootPostId":null,"participantIds":[],"emoji":null,"postState":null,"reactionPresent":null}`,
		`{"kind":"conversation","channelId":"channel-1","channelType":"private","teamId":"team-1","postId":null,"rootPostId":null,"participantIds":["peer"],"emoji":null,"postState":null,"reactionPresent":null}`,
		`{"kind":"conversation","channelId":"channel-1","channelType":"dm","teamId":null,"postId":null,"rootPostId":null,"participantIds":[],"emoji":null,"postState":null,"reactionPresent":null}`,
		`{"kind":"conversation","channelId":"channel-1","channelType":"dm","teamId":"team-1","postId":null,"rootPostId":null,"participantIds":["peer"],"emoji":null,"postState":null,"reactionPresent":null}`,
		`{"kind":"conversation","channelId":"channel-1","channelType":"group","teamId":null,"postId":null,"rootPostId":null,"participantIds":["claimed-complete"],"emoji":null,"postState":null,"reactionPresent":null}`,
		`{"kind":"conversation","channelId":null,"channelType":"public","teamId":"team-1","postId":null,"rootPostId":null,"participantIds":[],"emoji":null,"postState":null,"reactionPresent":null}`,
	}
	for _, destination := range destinationContradictions {
		assertInvalid(t, r, "mm/v2/stage-preview", strings.Replace(preview, validDestination, destination, 1))
	}
	legacyDirect := `{"kind":"conversation","channelId":"channel-1","channelType":"direct","teamId":null,"postId":null,"rootPostId":null,"participantIds":["peer"],"emoji":null,"postState":null,"reactionPresent":null}`
	assertInvalid(t, r, "mm/v2/stage-preview", strings.Replace(preview, validDestination, legacyDirect, 1))

	unresolved := strings.Replace(preview, validDestination, `{"kind":"conversation","channelId":null,"channelType":"dm","teamId":null,"postId":null,"rootPostId":null,"participantIds":["peer"],"emoji":null,"postState":null,"reactionPresent":null}`, 1)
	assertInvalid(t, r, "mm/v2/stage-preview", unresolved)
	compound := strings.Replace(unresolved, `{"ordinal":1,"type":"create_post","condition":"always"}`, `{"ordinal":1,"type":"resolve_conversation","condition":"if_missing"},{"ordinal":2,"type":"upload_attachment","condition":"always"},{"ordinal":3,"type":"create_post","condition":"always"}`, 1)
	if err := r.Validate("mm/v2/stage-preview", strings.NewReader(compound)); err != nil {
		t.Fatalf("valid unresolved compound preview rejected: %v", err)
	}
	resolvedWithResolve := strings.Replace(preview, `{"ordinal":1,"type":"create_post","condition":"always"}`, `{"ordinal":1,"type":"resolve_conversation","condition":"if_missing"},{"ordinal":2,"type":"create_post","condition":"always"}`, 1)
	assertInvalid(t, r, "mm/v2/stage-preview", resolvedWithResolve)
	resolvedWrongCondition := strings.Replace(resolvedWithResolve, `"condition":"if_missing"`, `"condition":"always"`, 1)
	assertInvalid(t, r, "mm/v2/stage-preview", resolvedWrongCondition)

	assertInvalid(t, r, "mm/v2/stage", strings.Replace(show, `"state":"present","body":"hello"`, `"state":"pruned","body":null`, 1))
	applying := strings.Replace(show, `"lifecycle":"open"`, `"lifecycle":"applying"`, 1)
	if err := r.Validate("mm/v2/stage", strings.NewReader(applying)); err != nil {
		t.Fatalf("valid applying stage with present content rejected: %v", err)
	}
	assertInvalid(t, r, "mm/v2/stage", strings.Replace(applying, `"state":"present","body":"hello"`, `"state":"pruned","body":null`, 1))
	nonContent := strings.NewReplacer(
		`"operation":"create_post"`, `"operation":"delete_post"`,
		validDestination, `{"kind":"post","channelId":"channel-1","channelType":"public","teamId":"team-1","postId":"post-1","rootPostId":null,"participantIds":[],"emoji":null,"postState":{"authorUserId":"user-1","updateAt":1,"contentDigest":"`+strings.Repeat("a", 64)+`"},"reactionPresent":null}`,
		`"state":"present","body":"hello"`, `"state":"none","body":null`,
		`"type":"create_post"`, `"type":"delete_post"`,
	).Replace(show)
	if err := r.Validate("mm/v2/stage", strings.NewReader(nonContent)); err != nil {
		t.Fatalf("valid current non-content stage rejected: %v", err)
	}
	for _, unsafe := range []string{"\x00", "\u061c", "\u200e", "\u200f", "\u202e", "\u2066"} {
		attachment := `{"path":` + strconv.Quote("/tmp/a"+unsafe) + `,"canonicalPath":"/tmp/a","remoteFilename":"a.txt","byteLength":1,"mediaType":"text/plain","contentDigest":"` + strings.Repeat("a", 64) + `"}`
		document := strings.Replace(show, `"attachmentState":"none","attachments":[]`, `"attachmentState":"retained","attachments":[`+attachment+`]`, 1)
		assertInvalid(t, r, "mm/v2/stage", document)
	}
	for _, padded := range []string{" user-1", "user-1 ", "user id", "user\u00a0id"} {
		assertInvalid(t, r, "mm/v2/stage", strings.Replace(show, `"userId":"user-1"`, `"userId":`+strconv.Quote(padded), 1))
	}

	revived := strings.NewReplacer(`"action":"created"`, `"action":"revised"`, `"revived":false`, `"revived":true`, `"stageRef":"stg_0123456789abcdefghijklmnopqrstuv@1"`, `"stageRef":"stg_0123456789abcdefghijklmnopqrstuv@2"`, `"revision":1`, `"revision":2`, `"recovery":"none"`, `"recovery":"resume_partial"`).Replace(receipt)
	assertInvalid(t, r, "mm/v2/stage-receipt", revived)
	if err := r.Validate("mm/v2/stage-receipt", strings.NewReader(strings.Replace(revived, `"recovery":"resume_partial"`, `"recovery":"none"`, 1))); err != nil {
		t.Fatalf("valid revived receipt rejected: %v", err)
	}

	list := example(t, "stages.json")
	start := strings.Index(list, `[{`)
	end := strings.LastIndex(list, `],"nextCursor"`)
	row := list[start+1 : end]
	oversized := list[:start+1] + strings.Repeat(row+",", 100) + row + list[end:]
	assertInvalid(t, r, "mm/v2/stages", oversized)
}

func TestStageOutputSchemasShareExactDefinitions(t *testing.T) {
	var canonical map[string]any
	for _, name := range []string{"stage.json", "stage-preview.json", "stage-receipt.json", "stages.json"} {
		schemaName := strings.TrimSuffix(name, ".json") + ".schema.json"
		data, err := fs.ReadFile(publicschemas.FS, "v2/"+schemaName)
		if err != nil {
			t.Fatal(err)
		}
		var document struct {
			Definitions map[string]any `json:"$defs"`
		}
		if err := json.Unmarshal(data, &document); err != nil {
			t.Fatal(err)
		}
		if canonical == nil {
			canonical = document.Definitions
			continue
		}
		if !reflect.DeepEqual(canonical, document.Definitions) {
			t.Fatalf("%s shared output definitions drifted", schemaName)
		}
	}
}

func TestStageDestinationRemoteStateDiscriminants(t *testing.T) {
	r, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("a", 64)
	postState := `{"authorUserId":"author-1","updateAt":1720000000000,"contentDigest":"` + digest + `"}`
	conversation := `{"kind":"conversation","channelId":"channel-1","channelType":"public","teamId":"team-1","postId":null,"rootPostId":null,"participantIds":[],"emoji":null,"postState":null,"reactionPresent":null}`
	reply := `{"kind":"post","channelId":"channel-1","channelType":"public","teamId":"team-1","postId":"post-1","rootPostId":"root-1","participantIds":[],"emoji":null,"postState":null,"reactionPresent":null}`
	post := `{"kind":"post","channelId":"channel-1","channelType":"public","teamId":"team-1","postId":"post-1","rootPostId":null,"participantIds":[],"emoji":null,"postState":` + postState + `,"reactionPresent":null}`
	reaction := `{"kind":"reaction","channelId":"channel-1","channelType":"public","teamId":"team-1","postId":"post-1","rootPostId":null,"participantIds":[],"emoji":"wave","postState":null,"reactionPresent":true}`
	dm := `{"kind":"conversation","channelId":null,"channelType":"dm","teamId":null,"postId":null,"rootPostId":null,"participantIds":["peer-1"],"emoji":null,"postState":null,"reactionPresent":null}`
	group := `{"kind":"conversation","channelId":null,"channelType":"group","teamId":null,"postId":null,"rootPostId":null,"participantIds":["peer-1","peer-2"],"emoji":null,"postState":null,"reactionPresent":null}`

	valid := []string{
		preview("create_post", conversation, "create_post", false),
		preview("reply", reply, "create_post", false),
		preview("edit_post", post, "edit_post", false),
		preview("delete_post", post, "delete_post", false),
		strings.Replace(preview("react", reaction, "add_reaction", false), `"condition":"always"`, `"condition":"if_missing"`, 1),
		strings.Replace(preview("unreact", reaction, "remove_reaction", false), `"condition":"always"`, `"condition":"if_missing"`, 1),
		strings.Replace(preview("resolve_dm", dm, "resolve_conversation", false), `"condition":"always"`, `"condition":"if_missing"`, 1),
		strings.Replace(preview("resolve_group_dm", group, "resolve_conversation", false), `"condition":"always"`, `"condition":"if_missing"`, 1),
	}
	for _, document := range valid {
		if err := r.Validate("mm/v2/stage-preview", strings.NewReader(document)); err != nil {
			t.Fatalf("valid operation destination rejected: %v\n%s", err, document)
		}
	}

	invalid := []string{
		strings.Replace(valid[0], `,"postState":null`, "", 1),
		strings.Replace(valid[0], `,"reactionPresent":null`, "", 1),
		strings.Replace(valid[0], `"postState":null`, `"postState":`+postState, 1),
		strings.Replace(valid[1], `"reactionPresent":null`, `"reactionPresent":false`, 1),
		strings.Replace(valid[2], `"postState":`+postState, `"postState":null`, 1),
		strings.Replace(valid[3], `"reactionPresent":null`, `"reactionPresent":false`, 1),
		strings.Replace(valid[4], `"reactionPresent":true`, `"reactionPresent":null`, 1),
		strings.Replace(valid[5], `"postState":null`, `"postState":`+postState, 1),
		strings.Replace(valid[6], `"postState":null`, `"postState":`+postState, 1),
		strings.Replace(valid[7], `"reactionPresent":null`, `"reactionPresent":true`, 1),
		strings.Replace(valid[2], `"authorUserId":"author-1"`, `"authorUserId":" author-1"`, 1),
		strings.Replace(valid[2], `"updateAt":1720000000000`, `"updateAt":0`, 1),
		strings.Replace(valid[2], `"updateAt":1720000000000`, `"updateAt":8640000000000001`, 1),
		strings.Replace(valid[2], digest, strings.Repeat("A", 64), 1),
		strings.Replace(valid[2], digest, strings.Repeat("a", 63), 1),
		strings.Replace(valid[4], `"condition":"if_missing"`, `"condition":"always"`, 1),
		strings.Replace(valid[5], `"condition":"if_missing"`, `"condition":"always"`, 1),
	}
	for _, document := range invalid {
		assertInvalid(t, r, "mm/v2/stage-preview", document)
	}
}

func TestStageEmojiMatchesMattermostReactionGrammar(t *testing.T) {
	r, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	valid := []string{strings.Repeat("a", 64), "Wave_ABC-123+OK"}
	invalid := []string{strings.Repeat("a", 65), "party.parrot", ":wave:"}
	for _, emoji := range valid {
		request := stageRequest(true, "reaction-request", "react", `{"kind":"post","postId":"post-1"}`, "null", strconv.Quote(emoji), `[]`)
		if err := r.Validate("mm/v2/stage-request", strings.NewReader(request)); err != nil {
			t.Fatalf("valid request emoji %q rejected: %v", emoji, err)
		}
		if err := r.Validate("mm/v2/stage-preview", strings.NewReader(reactionPreview(emoji))); err != nil {
			t.Fatalf("valid output emoji %q rejected: %v", emoji, err)
		}
	}
	for _, emoji := range invalid {
		request := stageRequest(true, "reaction-request", "react", `{"kind":"post","postId":"post-1"}`, "null", strconv.Quote(emoji), `[]`)
		assertInvalid(t, r, "mm/v2/stage-request", request)
		assertInvalid(t, r, "mm/v2/stage-preview", reactionPreview(emoji))
	}
}

func reactionPreview(emoji string) string {
	destination := `{"kind":"reaction","channelId":"channel-1","channelType":"public","teamId":"team-1","postId":"post-1","rootPostId":null,"participantIds":[],"emoji":` + strconv.Quote(emoji) + `,"postState":null,"reactionPresent":false}`
	return strings.Replace(preview("react", destination, "add_reaction", false), `"condition":"always"`, `"condition":"if_missing"`, 1)
}

func example(t *testing.T, name string) string {
	t.Helper()
	data, err := fs.ReadFile(publicschemas.FS, "v2/examples/"+name)
	if err != nil {
		t.Fatal(err)
	}
	return string(bytes.TrimSpace(data))
}

func assertInvalid(t *testing.T, r *Registry, id, document string) {
	t.Helper()
	if err := r.Validate(id, strings.NewReader(document)); err == nil {
		t.Fatalf("%s accepted invalid document: %s", id, document)
	}
}

func FuzzStageRequestRejectsUnknownTopLevelFields(f *testing.F) {
	seeds := []string{
		stageRequest(true, "r1", "create_post", conversationTarget("dm", "username", "arda", "null"), `"hello"`, "null", `[]`),
		stageRequest(true, "r2", "create_post", conversationTarget("group", "id", "group-id", "null"), `"hello"`, "null", `[]`),
		stageRequest(true, "r3", "create_post", conversationTarget("channel", "id", "channel-id", "null"), `"hello"`, "null", `[]`),
		stageRequest(true, "r4", "create_post", conversationTarget("channel", "name", "town-square", `{"by":"id","value":"team-id"}`), `"hello"`, "null", `[]`),
		stageRequest(true, "r5", "reply", `{"kind":"post","postId":"p"}`, `"hello"`, "null", `[]`),
		stageRequest(true, "r6", "resolve_dm", `{"kind":"user","username":"arda"}`, "null", "null", `[]`),
		stageRequest(true, "r7", "resolve_group_dm", `{"kind":"users","usernames":["arda","hakan"]}`, "null", "null", `[]`),
	}
	for _, seed := range seeds {
		f.Add(seed)
	}
	r, err := Load()
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, document string) {
		if len(document) < 2 || len(document) > 1<<20 || document[len(document)-1] != '}' {
			return
		}
		mutated := document[:len(document)-1] + `,"unknown":true}`
		if err := r.Validate("mm/v2/stage-request", strings.NewReader(mutated)); err == nil {
			t.Fatalf("stage request accepted unknown field: %s", mutated)
		}
	})
}

func stageRequest(persist bool, requestID, operation, target, body, emoji, attachments string) string {
	return `{"schema":"mm/v2/stage-request","persist":` + boolJSON(persist) + `,"requestId":` + quoteUnlessNull(requestID) + `,"operation":"` + operation + `","target":` + target + `,"body":` + body + `,"emoji":` + emoji + `,"attachments":` + attachments + `}`
}

func conversationTarget(kind, by, value, team string) string {
	return `{"kind":"conversation","conversationType":"` + kind + `","selector":{"by":"` + by + `","value":"` + value + `"},"team":` + team + `}`
}

func quoteUnlessNull(v string) string {
	if v == "null" {
		return v
	}
	return `"` + v + `"`
}

func boolJSON(v bool) string {
	if v {
		return "true"
	}
	return "false"
}
