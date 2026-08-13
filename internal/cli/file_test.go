package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ardasevinc/mattermost-cli/v2/internal/output"
	"github.com/ardasevinc/mattermost-cli/v2/internal/schema"
)

func TestFileDownloadEmitsStrictReceiptAndPreservesOpaqueBytes(t *testing.T) {
	payload := []byte("opaque test-token bytes\x00\xff")
	server := fileDownloadServer(t, "report-test-token.bin", payload, nil)
	defer server.Close()
	stdout, stderr, code := executeChannel(t, server.URL, "--json", "file", "download", "file-1")
	if code != 0 || stderr != "" || strings.Contains(stdout, "test-token") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	var receipt output.FileDownloadEnvelope
	if err := json.Unmarshal([]byte(stdout), &receipt); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(receipt.Path)) })
	data, err := os.ReadFile(receipt.Path)
	if err != nil || string(data) != string(payload) || !receipt.Temporary || receipt.SizeBytes != int64(len(payload)) || receipt.FileID != "file-1" {
		t.Fatalf("receipt=%+v exact=%v err=%v", receipt, string(data) == string(payload), err)
	}
	registry, err := schema.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Validate("mm/v2/file-download", strings.NewReader(stdout)); err != nil {
		t.Fatalf("schema: %v", err)
	}
}

func TestFileDownloadHumanReceiptAndExclusiveOutput(t *testing.T) {
	payload := []byte("hello")
	server := fileDownloadServer(t, "greeting.txt", payload, nil)
	defer server.Close()
	outputPath := filepath.Join(t.TempDir(), "chosen.txt")
	stdout, stderr, code := executeChannel(t, server.URL, "file", "download", "file-1", "--output", outputPath)
	if code != 0 || stderr != "" || stdout != "Downloaded greeting.txt (5 bytes)\nSaved to "+outputPath+"\n" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	stdout, stderr, code = executeChannel(t, server.URL, "--json", "file", "download", "file-1", "--output", outputPath)
	if code != 2 || stdout != "" || !strings.Contains(stderr, `"code":"invalid_input"`) || !strings.Contains(stderr, "already exists") {
		t.Fatalf("collision exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestFileDownloadRejectsLimitBeforeByteRequest(t *testing.T) {
	var byteRequests atomic.Int32
	server := fileDownloadServer(t, "five.bin", []byte("12345"), &byteRequests)
	defer server.Close()
	stdout, stderr, code := executeChannel(t, server.URL, "--json", "file", "download", "file-1", "--max-size", "4B")
	if code != 2 || stdout != "" || !strings.Contains(stderr, `"code":"invalid_input"`) || byteRequests.Load() != 0 {
		t.Fatalf("exit=%d requests=%d stdout=%q stderr=%q", code, byteRequests.Load(), stdout, stderr)
	}
}

func TestFileDownloadValidatesFlagsBeforeRuntime(t *testing.T) {
	var stdout, stderr strings.Builder
	code := Execute(context.Background(), []string{"file", "download", "id", "--max-size", "1.5GiB"}, strings.NewReader(""), &stdout, &stderr)
	if code != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "--max-size") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	code = Execute(context.Background(), []string{"file", "download", "id", "--output="}, strings.NewReader(""), &stdout, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "--output cannot be empty") {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
}

func TestParseByteSizeAndHumanFormatting(t *testing.T) {
	for raw, want := range map[string]int64{"1": 1, "2B": 2, "3KiB": 3 << 10, "4MiB": 4 << 20, "5GiB": 5 << 30, "6MB": 6_000_000} {
		got, err := parseByteSize(raw)
		if err != nil || got != want {
			t.Errorf("parseByteSize(%q)=%d,%v want %d", raw, got, err, want)
		}
	}
	for _, raw := range []string{"", "0", "-1", "1.2MiB", "wat", "999999999999999999999GiB"} {
		if _, err := parseByteSize(raw); err == nil {
			t.Errorf("parseByteSize(%q) succeeded", raw)
		}
	}
	if humanBytes(0) != "0 bytes" || humanBytes(1024) != "1 KiB" || humanBytes(1536) != "1.5 KiB" {
		t.Fatalf("human formatting: %q %q %q", humanBytes(0), humanBytes(1024), humanBytes(1536))
	}
}

func fileDownloadServer(t *testing.T, name string, payload []byte, byteRequests *atomic.Int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer test-token" {
			t.Fatalf("authorization=%q", request.Header.Get("Authorization"))
		}
		switch request.URL.Path {
		case "/api/v4/files/file-1/info":
			info := map[string]any{"id": "file-1", "user_id": "user-1", "channel_id": "channel-1", "post_id": "post-1", "create_at": 1, "update_at": 1, "delete_at": 0, "name": name, "size": len(payload), "mime_type": "application/octet-stream"}
			if err := json.NewEncoder(writer).Encode(info); err != nil {
				t.Fatal(err)
			}
		case "/api/v4/files/file-1":
			if byteRequests != nil {
				byteRequests.Add(1)
			}
			writer.Header().Set("Content-Type", "application/octet-stream")
			_, _ = writer.Write(payload)
		default:
			http.NotFound(writer, request)
		}
	}))
}
