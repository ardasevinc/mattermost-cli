package stagerequest

import (
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/ardasevinc/mattermost-cli/v2/internal/schema"
	"github.com/ardasevinc/mattermost-cli/v2/internal/stagestore"
	"github.com/ardasevinc/mattermost-cli/v2/internal/staging"
)

func decoder(t *testing.T) *Decoder {
	t.Helper()
	d, err := NewDecoder()
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func stageJSON(persist bool, operation, target, body, emoji, attachments string) string {
	requestID := "null"
	if persist {
		requestID = `"req-1"`
	}
	return `{"schema":"mm/v2/stage-request","persist":` + map[bool]string{true: "true", false: "false"}[persist] +
		`,"requestId":` + requestID + `,"operation":"` + operation + `","target":` + target +
		`,"body":` + body + `,"emoji":` + emoji + `,"attachments":` + attachments + `}`
}

func TestDecodeStageEveryOperationPersistAndDryRun(t *testing.T) {
	post := `{"kind":"post","postId":"post-1"}`
	conversation := `{"kind":"conversation","conversationType":"channel","selector":{"by":"name","value":"town-square"},"team":{"by":"id","value":"team-1"}}`
	user := `{"kind":"user","username":"alice"}`
	users := `{"kind":"users","usernames":["alice","bob"]}`
	cases := []struct{ operation, target, body, emoji, attachments string }{
		{"create_post", conversation, `"hello"`, "null", `[{"path":"/tmp/a","remoteFilename":null,"mediaType":null}]`},
		{"reply", post, `"hello"`, "null", `[{"path":"/tmp/a","remoteFilename":"a.txt","mediaType":"text/plain"}]`},
		{"edit_post", post, `"hello"`, "null", `[]`},
		{"delete_post", post, "null", "null", `[]`},
		{"react", post, "null", `"wave"`, `[]`},
		{"unreact", post, "null", `"wave"`, `[]`},
		{"resolve_dm", user, "null", "null", `[]`},
		{"resolve_group_dm", users, "null", "null", `[]`},
	}
	for _, tc := range cases {
		t.Run(tc.operation+"/persist", func(t *testing.T) {
			got, err := decoder(t).DecodeStage(strings.NewReader(stageJSON(true, tc.operation, tc.target, tc.body, tc.emoji, tc.attachments)))
			if err != nil || !got.Persist || got.RequestID == nil || got.Operation != Operation(tc.operation) {
				t.Fatalf("got %#v, %v", got, err)
			}
		})
		t.Run(tc.operation+"/dry", func(t *testing.T) {
			got, err := decoder(t).DecodeStage(strings.NewReader(stageJSON(false, tc.operation, tc.target, "null", tc.emoji, `[]`)))
			if err != nil || got.Persist || got.RequestID != nil || got.Body != nil || got.Attachments == nil {
				t.Fatalf("got %#v, %v", got, err)
			}
		})
	}
}

func TestDecodeStageMarkdownBoundsAndExactReader(t *testing.T) {
	for _, size := range []int{1, 16383} {
		body := strings.Repeat("x", size)
		r, err := decoder(t).DecodeStage(strings.NewReader(stageJSON(true, "edit_post", `{"kind":"post","postId":"p"}`, `"`+body+`"`, "null", `[]`)))
		if err != nil {
			t.Fatalf("size %d: %v", size, err)
		}
		in, err := r.EditPostInput()
		if err != nil {
			t.Fatal(err)
		}
		got, _ := io.ReadAll(in.Body)
		if string(got) != body {
			t.Fatalf("body changed at size %d", size)
		}
	}
	body := strings.Repeat("x", 16384)
	if _, err := decoder(t).DecodeStage(strings.NewReader(stageJSON(true, "edit_post", `{"kind":"post","postId":"p"}`, `"`+body+`"`, "null", `[]`))); !errors.Is(err, ErrInvalid) {
		t.Fatalf("long body: %v", err)
	}
}

func TestDecodeRejectsMalformedTrailingDuplicateAndUnknown(t *testing.T) {
	valid := stageJSON(false, "delete_post", `{"kind":"post","postId":"p"}`, "null", "null", `[]`)
	cases := []string{
		`{`, valid + `{}`, strings.Replace(valid, `"persist":false`, `"persist":false,"persist":false`, 1),
		strings.Replace(valid, `"attachments":[]`, `"attachments":[],"unknown":true`, 1),
	}
	for i, input := range cases {
		if _, err := decoder(t).DecodeStage(strings.NewReader(input)); !errors.Is(err, ErrInvalid) {
			t.Fatalf("case %d: %v", i, err)
		}
	}
}

type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

func TestDecodePreservesPhysicalReadFailure(t *testing.T) {
	sentinel := errors.New("disk vanished")
	_, err := decoder(t).DecodeStage(failingReader{sentinel})
	if !schema.IsInputReadError(err) || !errors.Is(err, sentinel) || errors.Is(err, ErrInvalid) {
		t.Fatalf("wrong error identity: %v", err)
	}
}

func TestStageConversionsAndNullAttachmentSemantics(t *testing.T) {
	r, err := decoder(t).DecodeStage(strings.NewReader(stageJSON(true, "create_post",
		`{"kind":"conversation","conversationType":"channel","selector":{"by":"name","value":"town-square"},"team":{"by":"name","value":"main"}}`,
		`"line 1\nline 2"`, "null", `[{"path":"relative.txt","remoteFilename":null,"mediaType":null}]`)))
	if err != nil {
		t.Fatal(err)
	}
	in, err := r.CreatePostInput()
	if err != nil {
		t.Fatal(err)
	}
	if in.Target.Conversation != staging.Channel || in.Target.Selector != staging.ByName || in.Target.Team == nil || in.Target.Team.By != staging.ByName || in.Attachments[0].RemoteFilename != "" || in.Attachments[0].MediaType != "" {
		t.Fatalf("bad conversion: %#v", in)
	}
	body, _ := io.ReadAll(in.Body)
	if string(body) != "line 1\nline 2" {
		t.Fatalf("body changed: %q", body)
	}

	mutated := r
	mutated.Operation = Reply
	if _, err := mutated.CreatePostInput(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("mutated DTO accepted: %v", err)
	}
}

func TestReviseAndCancelDecodeAndStoreConversions(t *testing.T) {
	digestText := strings.Repeat("ab", 32)
	reviseJSON := `{"schema":"mm/v2/stage-revise-request","requestId":"r","stageId":"stg_abcdefghijklmnopqrstuvwxyzABCDEF","expectedRevision":2,"expectedDigest":"` + digestText + `","revive":true,"body":null,"attachments":[]}`
	revise, err := decoder(t).DecodeRevise(strings.NewReader(reviseJSON))
	if err != nil || revise.Body != nil || revise.Attachments == nil {
		t.Fatalf("decode revise: %#v %v", revise, err)
	}
	reviseInput, err := revise.ReviseInput()
	if err != nil || reviseInput.ExpectedDigest[0] != 0xab || reviseInput.ExpectedRevision != 2 || !reviseInput.Revive || reviseInput.Body != nil {
		t.Fatalf("revise conversion: %#v %v", reviseInput, err)
	}
	preserve, err := decoder(t).DecodeRevise(strings.NewReader(strings.Replace(reviseJSON, `"attachments":[]`, `"attachments":null`, 1)))
	if err != nil || preserve.Attachments != nil {
		t.Fatalf("decode attachment preservation: %#v %v", preserve, err)
	}
	preserveInput, err := preserve.ReviseInput()
	if err != nil || preserveInput.Attachments != nil {
		t.Fatalf("convert attachment preservation: %#v %v", preserveInput, err)
	}

	cancelJSON := `{"schema":"mm/v2/stage-cancel-request","requestId":"r","stageId":"stg_abcdefghijklmnopqrstuvwxyzABCDEF","expectedRevision":3,"expectedDigest":"` + digestText + `"}`
	cancel, err := decoder(t).DecodeCancel(strings.NewReader(cancelJSON))
	if err != nil {
		t.Fatal(err)
	}
	cancelInput, err := cancel.CancelInput()
	if err != nil || cancelInput.ExpectedDigest != reviseInput.ExpectedDigest || cancelInput.ExpectedRevision != 3 {
		t.Fatalf("cancel conversion: %#v %v", cancelInput, err)
	}
	pruneJSON := `{"schema":"mm/v2/stage-prune-request","requestId":"p","stageId":"stg_abcdefghijklmnopqrstuvwxyzABCDEF","expectedRevision":4,"expectedDigest":"` + digestText + `","abandonRecovery":true}`
	prune, err := decoder(t).DecodePrune(strings.NewReader(pruneJSON))
	if err != nil {
		t.Fatal(err)
	}
	pruneInput, err := prune.PruneInput()
	if err != nil || pruneInput.ExpectedRevision != 4 || pruneInput.ExpectedDigest != reviseInput.ExpectedDigest || !pruneInput.AbandonRecovery {
		t.Fatalf("prune conversion: %#v %v", pruneInput, err)
	}

	cancel.ExpectedDigest = strings.ToUpper(digestText)
	if _, err := cancel.CancelInput(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("uppercase digest accepted: %v", err)
	}
	revise.Attachments = []Attachment{{Path: "/tmp/a"}}
	if _, err := revise.ReviseInput(); err != nil {
		t.Fatalf("raw attachment rejected: %v", err)
	}
}

func TestPruneDecodeRejectsUnknownFieldsDuplicateMembersAndZeroDigest(t *testing.T) {
	base := `{"schema":"mm/v2/stage-prune-request","requestId":"p","stageId":"stg_abcdefghijklmnopqrstuvwxyzABCDEF","expectedRevision":1,"expectedDigest":"` + strings.Repeat("01", 32) + `","abandonRecovery":false}`
	for name, raw := range map[string]string{
		"unknown":   strings.Replace(base, `"abandonRecovery":false`, `"abandonRecovery":false,"extra":true`, 1),
		"duplicate": strings.Replace(base, `"requestId":"p"`, `"requestId":"p","requestId":"q"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decoder(t).DecodePrune(strings.NewReader(raw)); !errors.Is(err, ErrInvalid) {
				t.Fatalf("accepted: %v", err)
			}
		})
	}
	request, err := decoder(t).DecodePrune(strings.NewReader(strings.Replace(base, strings.Repeat("01", 32), strings.Repeat("0", 64), 1)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = request.PruneInput(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("zero digest accepted: %v", err)
	}
}

func TestApplyDecodeConversionAndCallerIntentReplayDigest(t *testing.T) {
	digestText := strings.Repeat("ab", 32)
	raw := `{"schema":"mm/v2/apply-request","requestId":"apply-1","stageId":"stg_abcdefghijklmnopqrstuvwxyzABCDEF","revision":2,"expectedDigest":"` + digestText + `","recoveryMode":"resume_partial"}`
	request, err := decoder(t).DecodeApply(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	input, err := request.ApplyClaimInput()
	if err != nil || input.Revision != 2 || input.ExpectedDigest[0] != 0xab || input.RecoveryMode != stagestore.RecoveryModePartial || input.RequestDigest == ([32]byte{}) {
		t.Fatalf("input=%#v err=%v", input, err)
	}
	replayed := request
	replayed.RequestID = "apply-2"
	replayInput, err := replayed.ApplyClaimInput()
	if err != nil || replayInput.RequestDigest != input.RequestDigest {
		t.Fatalf("request id changed caller intent: %#v %v", replayInput, err)
	}
	replayed.RecoveryMode = string(stagestore.RecoveryModeUnknown)
	changed, err := replayed.ApplyClaimInput()
	if err != nil || changed.RequestDigest == input.RequestDigest {
		t.Fatalf("recovery mode did not change caller intent: %#v %v", changed, err)
	}
}

func TestApplyDecodeRejectsDuplicateAndMutatedContracts(t *testing.T) {
	valid := `{"schema":"mm/v2/apply-request","requestId":"apply-1","stageId":"stg_abcdefghijklmnopqrstuvwxyzABCDEF","revision":1,"expectedDigest":"` + strings.Repeat("ab", 32) + `","recoveryMode":"ordinary"}`
	for _, raw := range []string{strings.Replace(valid, `"requestId":"apply-1"`, `"requestId":"apply-1","requestId":"apply-1"`, 1), valid + `{}`} {
		if _, err := decoder(t).DecodeApply(strings.NewReader(raw)); !errors.Is(err, ErrInvalid) {
			t.Fatalf("malformed apply accepted: %v", err)
		}
	}
	request, err := decoder(t).DecodeApply(strings.NewReader(valid))
	if err != nil {
		t.Fatal(err)
	}
	request.ExpectedDigest = strings.ToUpper(request.ExpectedDigest)
	if _, err := request.ApplyClaimInput(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("mutated apply accepted: %v", err)
	}
}

func TestResolveConversionsCloneUsernames(t *testing.T) {
	dm := StageRequest{Schema: StageSchema, Operation: ResolveDM, Target: Target{Kind: "user", Username: "alice"}, Attachments: []Attachment{}}
	target, err := dm.ResolveDMTarget()
	if err != nil || target.Conversation != staging.Direct || target.Selector != staging.ByUsername || target.Value != "alice" {
		t.Fatalf("dm: %#v %v", target, err)
	}
	group := StageRequest{Schema: StageSchema, Operation: ResolveGroupDM, Target: Target{Kind: "users", Usernames: []string{"a", "b"}}, Attachments: []Attachment{}}
	names, err := group.ResolveGroupUsernames()
	if err != nil || !reflect.DeepEqual(names, group.Target.Usernames) {
		t.Fatalf("group: %#v %v", names, err)
	}
	names[0] = "changed"
	if group.Target.Usernames[0] != "a" {
		t.Fatal("conversion leaked mutable slice")
	}
}

func TestReviseInputClonesRawAttachments(t *testing.T) {
	r := ReviseRequest{Schema: ReviseSchema, RequestID: "r", StageID: "stg_abcdefghijklmnopqrstuvwxyzABCDEF", ExpectedRevision: 1, ExpectedDigest: strings.Repeat("01", 32), Attachments: []Attachment{{Path: "/a"}}}
	in, err := r.ReviseInput()
	if err != nil {
		t.Fatal(err)
	}
	r.Attachments[0].Path = "/changed"
	if in.Attachments[0].Path != "/a" {
		t.Fatal("conversion leaked attachment slice")
	}
}

func TestDecodeIntegralRevisionLexicalFormsExactly(t *testing.T) {
	for _, lexical := range []string{"1.0", "1e0", "9007199254740991.0", "9007199254740991e0"} {
		input := `{"schema":"mm/v2/stage-cancel-request","requestId":"r","stageId":"stg_abcdefghijklmnopqrstuvwxyzABCDEF","expectedRevision":` + lexical + `,"expectedDigest":"` + strings.Repeat("0", 64) + `"}`
		request, err := decoder(t).DecodeCancel(strings.NewReader(input))
		if err != nil {
			t.Fatalf("%s: %v", lexical, err)
		}
		converted, err := request.CancelInput()
		if err != nil || converted.ExpectedRevision < 1 || converted.ExpectedRevision > 9007199254740991 {
			t.Fatalf("%s: %#v, %v", lexical, converted, err)
		}
	}
}

func TestConversionsRejectMutatedContractFields(t *testing.T) {
	base := CancelRequest{Schema: CancelSchema, RequestID: "r", StageID: "stg_abcdefghijklmnopqrstuvwxyzABCDEF", ExpectedRevision: 1, ExpectedDigest: strings.Repeat("0", 64)}
	for name, mutate := range map[string]func(*CancelRequest){
		"request pattern": func(r *CancelRequest) { r.RequestID = "bad id" },
		"stage id":        func(r *CancelRequest) { r.StageID = "stg_bad" },
		"revision max":    func(r *CancelRequest) { r.ExpectedRevision = 9007199254740992 },
		"invalid UTF-8":   func(r *CancelRequest) { r.RequestID = string([]byte{0xff}) },
	} {
		t.Run(name, func(t *testing.T) {
			request := base
			mutate(&request)
			if _, err := request.CancelInput(); !errors.Is(err, ErrInvalid) {
				t.Fatalf("mutated DTO accepted: %v", err)
			}
		})
	}
}
