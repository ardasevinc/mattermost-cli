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

func TestLoadPairRequiresStrictCompleteCases(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pair.json")
	content := `{"schema":"mm/conformance-pair/v1","name":"pair","oracle":{"args":[],"expected":{"exitCode":0,"stdout":"","stderr":""}},"candidate":{"schema":"mm/conformance/v1","args":[],"expected":{"exitCode":0,"stdout":"","stderr":""}}}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadPair(path)
	if err == nil || !strings.Contains(err.Error(), "nested scenario must not declare schema") {
		t.Fatalf("LoadPair() error = %v", err)
	}
}

func TestPairFixturesLoad(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "conformance", "scenarios", "pairs", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no paired conformance fixtures found")
	}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			if _, err := LoadPair(path); err != nil {
				t.Fatal(err)
			}
		})
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
