package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
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
	if got, want := stdout.String(), "mm/v2/channel\nmm/v2/dms\nmm/v2/error\nmm/v2/group-dms\nmm/v2/mentions\nmm/v2/search\nmm/v2/thread\n"; got != want {
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
