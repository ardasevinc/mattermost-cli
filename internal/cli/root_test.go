package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/ardasevinc/mattermost-cli/internal/api"
)

func TestExecuteVersion(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := Execute(context.Background(), []string{"--version"}, strings.NewReader(""), &stdout, &stderr)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if got, want := stdout.String(), "mm version 2.0.0-dev (dev)\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestExecuteRejectsUnknownCommandWithoutUsage(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := Execute(context.Background(), []string{"nope"}, strings.NewReader(""), &stdout, &stderr)

	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), "unknown command") {
		t.Fatalf("stderr = %q, want unknown-command error", stderr.String())
	}
	if strings.Contains(stderr.String(), "Usage:") {
		t.Fatalf("stderr = %q, must not contain usage", stderr.String())
	}
}

func TestSchemaList(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := Execute(context.Background(), []string{"schema", "list"}, strings.NewReader(""), &stdout, &stderr)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr = %q", code, stderr.String())
	}
	if got, want := stdout.String(), "mm/v2/apply-receipt\nmm/v2/apply-request\nmm/v2/channel\nmm/v2/channels\nmm/v2/config\nmm/v2/dms\nmm/v2/doctor\nmm/v2/error\nmm/v2/group-dms\nmm/v2/mentions\nmm/v2/search\nmm/v2/stage\nmm/v2/stage-cancel-request\nmm/v2/stage-preview\nmm/v2/stage-prune-request\nmm/v2/stage-prune-result\nmm/v2/stage-receipt\nmm/v2/stage-request\nmm/v2/stage-revise-request\nmm/v2/stages\nmm/v2/store-doctor\nmm/v2/store-migrations\nmm/v2/teams\nmm/v2/thread\nmm/v2/unread\nmm/v2/users\nmm/v2/watch-diagnostic\nmm/v2/watch-event\nmm/v2/whoami\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestSchemaValidate(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	document := `{"schema":"mm/v2/error","code":"invalid_input","message":"bad input","exitCode":2,"recovery":"none"}`

	code := Execute(context.Background(), []string{"schema", "validate", "mm/v2/error"}, strings.NewReader(document), &stdout, &stderr)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr = %q", code, stderr.String())
	}
	if got, want := stdout.String(), "valid: mm/v2/error\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestSchemaValidatePhysicalReadFailureIsExitThree(t *testing.T) {
	var stdout, stderr bytes.Buffer
	physical := errors.New("hostile reader detail \x1b[2J")
	code := Execute(context.Background(), []string{"schema", "validate", "mm/v2/error"}, errorInput{err: physical}, &stdout, &stderr)
	if code != 3 {
		t.Fatalf("exit code = %d, want 3; stderr=%q", code, stderr.String())
	}
	if stdout.Len() != 0 || strings.Contains(stderr.String(), physical.Error()) || !strings.Contains(stderr.String(), "could not read JSON document") {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestSchemaValidateInvalidJSONRemainsExitTwo(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), []string{"schema", "validate", "mm/v2/error"}, strings.NewReader("{"), &stdout, &stderr)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2; stderr=%q", code, stderr.String())
	}
}

type errorInput struct{ err error }

func (r errorInput) Read([]byte) (int, error) { return 0, r.err }

func TestSchemaLookupDoesNotReflectActiveCredential(t *testing.T) {
	const token = "super-secret-mm-token"
	t.Setenv("MM_TOKEN", token)
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := Execute(context.Background(), []string{"schema", "show", token}, strings.NewReader(""), &stdout, &stderr)

	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if strings.Contains(stderr.String(), token) {
		t.Fatalf("stderr reflected active credential: %q", stderr.String())
	}
}

func TestExecuteSanitizesAndRedactsUnknownCommand(t *testing.T) {
	const token = "super-secret-mm-token"
	t.Setenv("MM_TOKEN", token)
	hostile := token + " \x1b[2J " + "AKIAIOSFODNN7EXAMPLE"
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := Execute(context.Background(), []string{hostile}, strings.NewReader(""), &stdout, &stderr)

	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if strings.Contains(stderr.String(), token) || strings.ContainsRune(stderr.String(), '\x1b') ||
		strings.Contains(stderr.String(), "AKIAIOSFODNN7EXAMPLE") {
		t.Fatalf("stderr retained hostile input: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), `[REDACTED:mattermost_credential]`) ||
		(!strings.Contains(stderr.String(), `\u001b`) && !strings.Contains(stderr.String(), `\x1b`)) {
		t.Fatalf("stderr did not expose safe provenance: %q", stderr.String())
	}
}

func TestSchemaShowTreatsShortWriteAsReadFailure(t *testing.T) {
	var stderr bytes.Buffer

	code := Execute(context.Background(), []string{"schema", "show", "mm/v2/error"}, strings.NewReader(""), shortWriter{}, &stderr)

	if code != 3 {
		t.Fatalf("exit code = %d, want 3; stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "write output failed") {
		t.Fatalf("stderr = %q, want generic output failure", stderr.String())
	}
}

func TestSchemaCommandsRejectJSONWithMachineError(t *testing.T) {
	for _, args := range [][]string{{"--json", "schema"}, {"--json", "schema", "list"}} {
		var stdout, stderr bytes.Buffer
		code := Execute(context.Background(), args, strings.NewReader(""), &stdout, &stderr)
		if code != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), `"code":"invalid_input"`) {
			t.Fatalf("args=%q exit=%d stdout=%q stderr=%q", args, code, stdout.String(), stderr.String())
		}
	}
}

func TestMachineErrorCodePreservesSemantics(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{errors.New("cobra"), "invalid_invocation"},
		{invalidFailure("bad"), "invalid_input"},
		{configFailure("bad"), "configuration"},
		{readFailure(errors.New("bad")), "read_failed"},
		{readFailure(&api.APIError{Status: 401}), "authentication"},
		{readFailure(&api.APIError{Status: 403}), "authorization"},
		{outputError{err: errors.New("bad")}, "internal"},
		{localStateFailure{err: errors.New("bad")}, "state_conflict"},
	}
	for _, test := range tests {
		if got := machineErrorCode(test.err); got != test.want {
			t.Errorf("machineErrorCode(%T) = %q, want %q", test.err, got, test.want)
		}
	}
}

func TestConfirmedEffectFailurePreservesSafePartialRecovery(t *testing.T) {
	err := applyConfirmedFailureWithRecovery("stg_0123456789abcdefghijklmnopqrstuv@1", "resume_partial", errors.New("output failed"))
	if exitCode(err) != 7 || machineErrorCode(err) != "confirmed_effect_local_failure" || machineRecovery(err) != "resume_partial" {
		t.Fatalf("exit=%d code=%q recovery=%q", exitCode(err), machineErrorCode(err), machineRecovery(err))
	}
}

func TestEarlyStructuredMachineModeOnlyRecognizesApplyFlag(t *testing.T) {
	for _, test := range []struct {
		args []string
		want bool
	}{
		{[]string{"apply", "--from-json"}, true},
		{[]string{"--url", "https://example.com", "apply", "--from-json", "--unknown"}, true},
		{[]string{"stage", "send", "dm", "alice", "--message", "--from-json"}, false},
		{[]string{"apply", "--", "--from-json"}, false},
	} {
		if got := earlyStructuredMachineMode(test.args); got != test.want {
			t.Fatalf("args=%q got=%v want=%v", test.args, got, test.want)
		}
	}
}

func TestErrorOutputShortWriteReturnsOutputFailure(t *testing.T) {
	var stdout bytes.Buffer
	code := Execute(context.Background(), []string{"unknown"}, strings.NewReader(""), &stdout, shortWriter{})
	if code != 3 {
		t.Fatalf("exit code = %d, want 3", code)
	}
}

func TestRootHelpAndVersionShortWritesReturnOutputFailure(t *testing.T) {
	tests := [][]string{nil, {"--help"}, {"--version"}}
	for _, args := range tests {
		var stderr bytes.Buffer
		if code := Execute(context.Background(), args, strings.NewReader(""), shortWriter{}, &stderr); code != 3 {
			t.Fatalf("Execute(%q) exit = %d, want 3", args, code)
		}
	}
}

type shortWriter struct{}

func (shortWriter) Write(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}
	return len(data) - 1, nil
}

var _ io.Writer = shortWriter{}
