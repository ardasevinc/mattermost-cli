package conformance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scenario.json")
	content := `{"schema":"mm/conformance/v1","name":"bad","unexpected":true,"expected":{"exitCode":0,"stdout":"","stderr":""}}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("Load() error = %v, want unknown-field failure", err)
	}
}

func TestLoadRequiresCompleteProcessExpectation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scenario.json")
	content := `{"schema":"mm/conformance/v1","name":"bad","args":[],"expected":{"exitCode":0,"stdout":""}}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "expected exitCode, stdout, and stderr are required") {
		t.Fatalf("Load() error = %v, want incomplete-expectation failure", err)
	}
}

func TestLoadRejectsIgnoredAuthorizationHeader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scenario.json")
	content := `{"schema":"mm/conformance/v1","name":"bad","args":[],"http":[{"request":{"method":"GET","uri":"/","ignoreHeaders":["authorization"]},"response":{"status":200}}],"expected":{"exitCode":0,"stdout":"","stderr":""}}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "cannot ignore security-sensitive header") {
		t.Fatalf("Load() error = %v, want ignored-authorization failure", err)
	}
}

func TestSequentialServerReportsMissingRequests(t *testing.T) {
	server := newSequentialServer([]HTTPExchange{{
		Request:  HTTPRequestExpected{Method: "GET", URI: "/api/v4/users/me"},
		Response: HTTPResponse{Status: 200},
	}})

	err := server.verify()
	if err == nil || !strings.Contains(err.Error(), "observed 0 requests, want 1") {
		t.Fatalf("verify() error = %v, want missing-request failure", err)
	}
}
