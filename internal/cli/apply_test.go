//go:build darwin || linux

package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	mmSchema "github.com/ardasevinc/mattermost-cli/internal/schema"
	"github.com/ardasevinc/mattermost-cli/internal/stagerequest"
	"github.com/ardasevinc/mattermost-cli/internal/stagestore"
	"github.com/ardasevinc/mattermost-cli/internal/staging"
)

func createCLIApplyStage(t *testing.T, stateRoot, serverURL, body string) stagestore.MutationResult {
	t.Helper()
	paths, err := stagestore.ResolvePaths(t.TempDir(), func(key string) (string, bool) { return stateRoot, key == "XDG_STATE_HOME" })
	if err != nil {
		t.Fatal(err)
	}
	store, err := stagestore.Open(t.Context(), paths.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	destination, err := json.Marshal(staging.Destination{Kind: "conversation", ChannelID: "channel-1", ChannelType: "public", TeamID: stringPointerCLI("team-1"), ParticipantIDs: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.Create(t.Context(), stagestore.CreateInput{
		RequestDigest: sha256.Sum256([]byte("cli-apply\x00" + body)), Operation: stagestore.CreatePost,
		ServerURL: serverURL + "/api/v4", UserID: "self",
		Content: stagestore.RevisionContent{Body: []byte(body), Destination: destination, Plan: json.RawMessage(`{"steps":[{"ordinal":1,"type":"create_post","condition":"always"}]}`)},
	})
	if closeErr := store.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	return result.MutationResult
}

func stringPointerCLI(value string) *string { return &value }

func setApplyEnvironment(t *testing.T, serverURL, stateRoot string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_STATE_HOME", stateRoot)
	t.Setenv("MM_URL", serverURL)
	t.Setenv("MM_TOKEN", "test-token")
}

func postApplyServer(t *testing.T, message string, status int, writes *atomic.Int32, warningSeen *atomic.Bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.RequestURI() {
		case "/api/v4/users/me":
			_, _ = io.WriteString(response, `{"id":"self","username":"arda"}`)
		case "/api/v4/channels/channel-1":
			_, _ = io.WriteString(response, `{"id":"channel-1","team_id":"team-1","type":"O","name":"town-square","display_name":"Town Square"}`)
		case "/api/v4/channels/channel-1/members/self":
			_, _ = io.WriteString(response, `{"channel_id":"channel-1","user_id":"self"}`)
		case "/api/v4/posts":
			writes.Add(1)
			if warningSeen != nil && !warningSeen.Load() {
				t.Error("force warning was not emitted before mutation dispatch")
			}
			var input struct {
				ChannelID     string `json:"channel_id"`
				Message       string `json:"message"`
				PendingPostID string `json:"pending_post_id"`
			}
			if request.Method != http.MethodPost || json.NewDecoder(request.Body).Decode(&input) != nil || input.ChannelID != "channel-1" || input.Message != message || input.PendingPostID == "" {
				response.WriteHeader(http.StatusBadRequest)
				return
			}
			if status != http.StatusCreated {
				response.WriteHeader(status)
				return
			}
			response.WriteHeader(http.StatusCreated)
			raw, _ := json.Marshal(map[string]any{"id": "post-1", "channel_id": "channel-1", "user_id": "self", "message": message, "create_at": 100, "update_at": 100, "delete_at": 0, "root_id": "", "file_ids": []string{}, "pending_post_id": input.PendingPostID, "type": ""})
			_, _ = response.Write(raw)
		default:
			http.NotFound(response, request)
		}
	}))
}

func applyRequestJSON(t *testing.T, stage stagestore.MutationResult, requestID string, mode stagestore.RecoveryMode) []byte {
	t.Helper()
	request := stagerequest.ApplyRequest{Schema: stagerequest.ApplySchema, RequestID: requestID, StageID: stage.Stage.ID, Revision: stagerequest.ExactInt64(stage.Stage.Revision), ExpectedDigest: hex.EncodeToString(stage.Stage.SemanticDigest[:]), RecoveryMode: string(mode)}
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func validateApplyReceipt(t *testing.T, raw []byte) {
	t.Helper()
	registry, err := mmSchema.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Validate("mm/v2/apply-receipt", bytes.NewReader(raw)); err != nil {
		t.Fatalf("invalid receipt: %v\n%s", err, raw)
	}
}

func TestStructuredApplyPreservesShortAndLongMarkdownAndReplaysWithoutNetwork(t *testing.T) {
	for name, message := range map[string]string{
		"short": "# heading\n\n- **bold** and `code`\n",
		"long":  strings.Repeat("界", 16_382) + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			var writes atomic.Int32
			server := postApplyServer(t, message, http.StatusCreated, &writes, nil)
			defer server.Close()
			stateRoot := filepath.Join(t.TempDir(), "state")
			setApplyEnvironment(t, server.URL, stateRoot)
			stage := createCLIApplyStage(t, stateRoot, server.URL, message)
			request := applyRequestJSON(t, stage, "apply-"+name, stagestore.RecoveryModeOrdinary)

			var stdout, stderr bytes.Buffer
			if code := Execute(t.Context(), []string{"apply", "--from-json"}, bytes.NewReader(request), &stdout, &stderr); code != 0 || stderr.Len() != 0 {
				t.Fatalf("first exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			validateApplyReceipt(t, stdout.Bytes())
			if strings.Contains(stdout.String(), message) || writes.Load() != 1 {
				t.Fatalf("receipt leaked content or wrong writes: %d %q", writes.Load(), stdout.String())
			}

			stdout.Reset()
			stderr.Reset()
			if code := Execute(t.Context(), []string{"apply", "--from-json"}, bytes.NewReader(request), &stdout, &stderr); code != 0 || stderr.Len() != 0 || writes.Load() != 1 {
				t.Fatalf("replay exit=%d writes=%d stdout=%q stderr=%q", code, writes.Load(), stdout.String(), stderr.String())
			}
			validateApplyReceipt(t, stdout.Bytes())
		})
	}
}

func TestHumanApplyRejectedReturnsReceiptAndSafeRetryCommand(t *testing.T) {
	var writes atomic.Int32
	server := postApplyServer(t, "rejected", http.StatusBadRequest, &writes, nil)
	defer server.Close()
	stateRoot := filepath.Join(t.TempDir(), "state")
	setApplyEnvironment(t, server.URL, stateRoot)
	stage := createCLIApplyStage(t, stateRoot, server.URL, "rejected")
	stageRef := stage.Stage.ID + "@1"

	var stdout, stderr bytes.Buffer
	code := Execute(t.Context(), []string{"apply", stageRef}, strings.NewReader(""), &stdout, &stderr)
	if code != 4 || stderr.Len() != 0 || writes.Load() != 1 || !strings.Contains(stdout.String(), "outcome: rejected") || !strings.Contains(stdout.String(), "next: mm apply "+stageRef) {
		t.Fatalf("exit=%d writes=%d stdout=%q stderr=%q", code, writes.Load(), stdout.String(), stderr.String())
	}
}

func TestApplyConfirmedEffectOutputFailureExitsSevenAndDoesNotRetry(t *testing.T) {
	var writes atomic.Int32
	server := postApplyServer(t, "one shot", http.StatusCreated, &writes, nil)
	defer server.Close()
	stateRoot := filepath.Join(t.TempDir(), "state")
	setApplyEnvironment(t, server.URL, stateRoot)
	stage := createCLIApplyStage(t, stateRoot, server.URL, "one shot")
	stageRef := stage.Stage.ID + "@1"

	var stderr bytes.Buffer
	if code := Execute(t.Context(), []string{"apply", stageRef}, strings.NewReader(""), shortWriter{}, &stderr); code != 7 || writes.Load() != 1 || !strings.Contains(stderr.String(), "do not retry") {
		t.Fatalf("exit=%d writes=%d stderr=%q", code, writes.Load(), stderr.String())
	}
	var stdout bytes.Buffer
	stderr.Reset()
	if code := Execute(t.Context(), []string{"apply", stageRef}, strings.NewReader(""), &stdout, &stderr); code != 6 || writes.Load() != 1 {
		t.Fatalf("retry exit=%d writes=%d stdout=%q stderr=%q", code, writes.Load(), stdout.String(), stderr.String())
	}

	for _, machine := range []bool{false, true} {
		stage = createCLIApplyStage(t, stateRoot, server.URL, "one shot")
		args := []string{"apply", stage.Stage.ID + "@1"}
		if machine {
			args = append([]string{"--json"}, args...)
		}
		if code := Execute(t.Context(), args, strings.NewReader(""), zeroFailWriter{}, zeroFailWriter{}); code != 7 {
			t.Fatalf("machine=%v exit=%d writes=%d", machine, code, writes.Load())
		}
	}
	if writes.Load() != 3 {
		t.Fatalf("writes=%d, want 3", writes.Load())
	}
}

func TestUnsafeConfirmedReceiptExitsSevenWithoutLeakingCredentialOrRetrying(t *testing.T) {
	var writes atomic.Int32
	server := postApplyServer(t, "safe body", http.StatusCreated, &writes, nil)
	defer server.Close()
	stateRoot := filepath.Join(t.TempDir(), "state")
	setApplyEnvironment(t, server.URL, stateRoot)
	t.Setenv("MM_TOKEN", "post-1")
	stage := createCLIApplyStage(t, stateRoot, server.URL, "safe body")
	args := []string{"apply", stage.Stage.ID + "@1", "--request-id", "unsafe-receipt-replay"}

	for attempt := 1; attempt <= 2; attempt++ {
		var stdout, stderr bytes.Buffer
		if code := Execute(t.Context(), args, strings.NewReader(""), &stdout, &stderr); code != 7 || stdout.Len() != 0 || writes.Load() != 1 || !strings.Contains(stderr.String(), "do not retry") || strings.Contains(stderr.String(), "post-1") {
			t.Fatalf("attempt=%d exit=%d writes=%d stdout=%q stderr=%q", attempt, code, writes.Load(), stdout.String(), stderr.String())
		}
	}
}

type zeroFailWriter struct{}

func (zeroFailWriter) Write([]byte) (int, error) { return 0, errors.New("closed") }

type warningObserver struct {
	buffer *bytes.Buffer
	seen   *atomic.Bool
}

func (w warningObserver) Write(data []byte) (int, error) {
	if bytes.Contains(data, []byte("may duplicate a real Mattermost side effect")) {
		w.seen.Store(true)
	}
	return w.buffer.Write(data)
}

func TestForceUnknownWarnsBeforeDispatch(t *testing.T) {
	var writes atomic.Int32
	var warningSeen atomic.Bool
	server := postApplyServer(t, "forced", http.StatusCreated, &writes, &warningSeen)
	defer server.Close()
	stateRoot := filepath.Join(t.TempDir(), "state")
	setApplyEnvironment(t, server.URL, stateRoot)
	stage := createCLIApplyStage(t, stateRoot, server.URL, "forced")
	paths, err := stagestore.ResolvePaths(t.TempDir(), func(key string) (string, bool) { return stateRoot, key == "XDG_STATE_HOME" })
	if err != nil {
		t.Fatal(err)
	}
	store, err := stagestore.Open(t.Context(), paths.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	claim := stagerequest.NewApplyClaimInput(stage.Stage.ID, "", 1, stage.Stage.SemanticDigest, stagestore.RecoveryModeOrdinary)
	attempt, err := store.ClaimApply(t.Context(), claim)
	if err == nil {
		err = store.BeginDispatch(t.Context(), attempt.ID, 1)
	}
	if closeErr := store.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := Execute(t.Context(), []string{"apply", stage.Stage.ID + "@1", "--force-unknown"}, strings.NewReader(""), &stdout, warningObserver{&stderr, &warningSeen})
	if code != 0 || writes.Load() != 1 || !warningSeen.Load() || !strings.Contains(stdout.String(), "forcedDuplicateRisk") && !strings.Contains(stdout.String(), "outcome: succeeded") {
		t.Fatalf("exit=%d writes=%d stdout=%q stderr=%q", code, writes.Load(), stdout.String(), stderr.String())
	}
}

func TestHumanRequestIDReplaysOldUnknownReceiptAfterRevision(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	stateRoot := filepath.Join(t.TempDir(), "state")
	setApplyEnvironment(t, server.URL, stateRoot)
	stage := createCLIApplyStage(t, stateRoot, server.URL, "original")
	paths, err := stagestore.ResolvePaths(t.TempDir(), func(key string) (string, bool) { return stateRoot, key == "XDG_STATE_HOME" })
	if err != nil {
		t.Fatal(err)
	}
	store, err := stagestore.Open(t.Context(), paths.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	claim := stagerequest.NewApplyClaimInput(stage.Stage.ID, "human-old-replay", 1, stage.Stage.SemanticDigest, stagestore.RecoveryModeOrdinary)
	attempt, err := store.ClaimApply(t.Context(), claim)
	if err == nil {
		err = store.BeginDispatch(t.Context(), attempt.ID, 1)
	}
	if err == nil {
		err = store.MarkStepUnknown(t.Context(), attempt.ID, 1)
	}
	if err == nil {
		_, err = store.FinalizeApply(t.Context(), attempt.ID)
	}
	detail, showErr := store.Show(t.Context(), stage.Stage.ID)
	if err == nil {
		err = showErr
	}
	if err == nil {
		_, err = store.Revise(t.Context(), stagestore.ReviseInput{StageID: stage.Stage.ID, ExpectedRevision: 1, ExpectedDigest: stage.Stage.SemanticDigest, RequestDigest: sha256.Sum256([]byte("revise after unknown")), Composition: stagestore.Composition{Body: []byte("revised"), Plan: detail.Plan}})
	}
	if closeErr := store.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := Execute(t.Context(), []string{"apply", stage.Stage.ID + "@1", "--request-id", "human-old-replay"}, strings.NewReader(""), &stdout, &stderr)
	if code != 5 || stderr.Len() != 0 || requests.Load() != 0 || !strings.Contains(stdout.String(), "outcome: unknown") || !strings.Contains(stdout.String(), "replayed: true") || !strings.Contains(stdout.String(), "--force-unknown") {
		t.Fatalf("exit=%d requests=%d stdout=%q stderr=%q", code, requests.Load(), stdout.String(), stderr.String())
	}
}

func TestApplyRejectsInvalidCombinationsAndStructuredInputBeforeStateOrNetwork(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	stateRoot := filepath.Join(t.TempDir(), "absent")
	setApplyEnvironment(t, server.URL, stateRoot)
	for _, test := range []struct {
		args  []string
		input string
	}{
		{[]string{"apply", "stg_0123456789abcdefghijklmnopqrstuv@1", "--resume-partial", "--force-unknown"}, ""},
		{[]string{"apply", "stg_0123456789abcdefghijklmnopqrstuv@1", "--request-id", "bad id"}, ""},
		{[]string{"apply", "--from-json", "--request-id", "x"}, `{}`},
		{[]string{"apply", "--from-json", "--resume-partial=false"}, `{}`},
		{[]string{"apply", "--from-json"}, `{"schema":"mm/v2/apply-request"}`},
	} {
		var stdout, stderr bytes.Buffer
		if code := Execute(t.Context(), test.args, strings.NewReader(test.input), &stdout, &stderr); code != 2 || stdout.Len() != 0 {
			t.Fatalf("args=%q exit=%d stdout=%q stderr=%q", test.args, code, stdout.String(), stderr.String())
		}
	}
	if requests.Load() != 0 {
		t.Fatalf("invalid apply used network %d times", requests.Load())
	}
}

func TestStructuredApplyArgumentFailureIsMachineError(t *testing.T) {
	registry, err := mmSchema.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"apply", "--from-json", "unexpected"}, {"apply", "--from-json", "--unknown-flag"}, {"apply", "--from-json=wat"}} {
		var stdout, stderr bytes.Buffer
		if code := Execute(t.Context(), args, strings.NewReader(""), &stdout, &stderr); code != 2 || stdout.Len() != 0 {
			t.Fatalf("args=%q exit=%d stdout=%q stderr=%q", args, code, stdout.String(), stderr.String())
		}
		if err := registry.Validate("mm/v2/error", bytes.NewReader(stderr.Bytes())); err != nil {
			t.Fatalf("args=%q invalid machine error: %v\n%s", args, err, stderr.String())
		}
	}
}

func TestStructuredApplyStateConflictUsesSchemaValidRecoveryError(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	stateRoot := filepath.Join(t.TempDir(), "state")
	setApplyEnvironment(t, server.URL, stateRoot)
	stage := createCLIApplyStage(t, stateRoot, server.URL, "not dispatched")
	request := applyRequestJSON(t, stage, "wrong-recovery", stagestore.RecoveryModePartial)

	var stdout, stderr bytes.Buffer
	if code := Execute(t.Context(), []string{"apply", "--from-json"}, bytes.NewReader(request), &stdout, &stderr); code != 6 || stdout.Len() != 0 || requests.Load() != 0 {
		t.Fatalf("exit=%d requests=%d stdout=%q stderr=%q", code, requests.Load(), stdout.String(), stderr.String())
	}
	registry, err := mmSchema.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Validate("mm/v2/error", bytes.NewReader(stderr.Bytes())); err != nil {
		t.Fatalf("invalid machine error: %v\n%s", err, stderr.String())
	}
	if !strings.Contains(stderr.String(), `"code":"state_conflict"`) || !strings.Contains(stderr.String(), `"recovery":"none"`) || !strings.Contains(stderr.String(), `"stageRef":"`+stage.Stage.ID+`@1"`) {
		t.Fatalf("stderr=%s", stderr.String())
	}
}

func TestDurableUnknownReceiptDominatesStoreCloseFailure(t *testing.T) {
	err := classifyApplyReceiptCloseFailure("stg_0123456789abcdefghijklmnopqrstuv@1", stagestore.ApplyReceipt{Outcome: stagestore.OutcomeUnknown, Recovery: stagestore.RecoveryUnknown})
	if exitCode(err) != 5 || machineErrorCode(err) != "mutation_unknown" || machineRecovery(err) != "force_unknown" {
		t.Fatalf("exit=%d code=%q recovery=%q err=%v", exitCode(err), machineErrorCode(err), machineRecovery(err), err)
	}
}
