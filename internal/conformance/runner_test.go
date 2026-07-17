package conformance

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

func expected(exit int, stdout, stderr string) *ProcessExpected {
	return &ProcessExpected{ExitCode: &exit, Stdout: &stdout, Stderr: &stderr}
}

func TestRunPairIdentifiesFailingSide(t *testing.T) {
	pair := PairScenario{
		Oracle:    Scenario{Args: []string{"-c", "printf oracle"}, Expected: expected(0, "oracle", "")},
		Candidate: Scenario{Args: []string{"-c", "printf candidate"}, Expected: expected(0, "wrong", "")},
	}
	err := RunPair(context.Background(), Command{Path: "sh"}, Command{Path: "sh"}, pair)
	if err == nil || !strings.Contains(err.Error(), "candidate: stdout mismatch") {
		t.Fatalf("RunPair() error = %v", err)
	}
}

func TestLimitedBufferBoundsStoredOutput(t *testing.T) {
	buffer := newLimitedBuffer(4)
	input := []byte("abcdefgh")

	n, err := buffer.Write(input)

	if err != nil || n != len(input) {
		t.Fatalf("Write() = (%d, %v), want (%d, nil)", n, err, len(input))
	}
	if got := buffer.String(); got != "abcd" {
		t.Fatalf("String() = %q, want %q", got, "abcd")
	}
	if !buffer.Exceeded() {
		t.Fatal("Exceeded() = false, want true")
	}
}

func TestIsolatedEnvRejectsReservedOverride(t *testing.T) {
	_, err := isolatedEnv(t.TempDir(), t.TempDir(), "http://127.0.0.1", map[string]string{"MM_URL": "https://real.example"})
	if err == nil || !strings.Contains(err.Error(), "reserved variable") {
		t.Fatalf("isolatedEnv() error = %v, want reserved-variable failure", err)
	}
}

func TestIsolatedEnvDoesNotInheritParentSecrets(t *testing.T) {
	t.Setenv("AWS_SECRET_ACCESS_KEY", "must-not-cross-boundary")
	env, err := isolatedEnv(t.TempDir(), t.TempDir(), "http://127.0.0.1", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range env {
		if strings.Contains(entry, "must-not-cross-boundary") {
			t.Fatalf("isolated env inherited parent secret in %q", entry)
		}
	}
}

func TestSequentialServerRejectsDuplicateExpectedHeader(t *testing.T) {
	server := newSequentialServer([]HTTPExchange{{
		Request: HTTPRequestExpected{
			Method:  "GET",
			URI:     "/api/v4/users/me",
			Headers: map[string]string{"Authorization": "Bearer fixture"},
		},
		Response: HTTPResponse{Status: 200},
	}})
	req := httptest.NewRequest(http.MethodGet, "http://example.test/api/v4/users/me", nil)
	req.Header.Add("Authorization", "Bearer fixture")
	req.Header.Add("Authorization", "Bearer second")
	response := httptest.NewRecorder()

	server.ServeHTTP(response, req)

	if err := server.verify(); err == nil || !strings.Contains(err.Error(), "header \"Authorization\" mismatched") {
		t.Fatalf("verify() error = %v, want duplicate-header failure", err)
	}
}

func TestSequentialServerRejectsUnexpectedHeader(t *testing.T) {
	server := newSequentialServer([]HTTPExchange{{
		Request:  HTTPRequestExpected{Method: "GET", URI: "/"},
		Response: HTTPResponse{Status: 200},
	}})
	req := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
	req.Header.Set("X-Unexpected", "value")
	response := httptest.NewRecorder()

	server.ServeHTTP(response, req)

	if err := server.verify(); err == nil || !strings.Contains(err.Error(), "unexpected header \"X-Unexpected\"") {
		t.Fatalf("verify() error = %v, want unexpected-header failure", err)
	}
}

func TestSequentialServerRejectsCredentialInIgnoredHeader(t *testing.T) {
	const token = "fixture-active-token"
	server := newSequentialServer([]HTTPExchange{{
		Request: HTTPRequestExpected{
			Method:        "GET",
			URI:           "/",
			Headers:       map[string]string{"Authorization": "Bearer " + token},
			IgnoreHeaders: []string{"User-Agent"},
		},
		Response: HTTPResponse{Status: 200},
	}}, token)
	req := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", "accidental/"+token)
	response := httptest.NewRecorder()

	server.ServeHTTP(response, req)
	err := server.verify()

	if err == nil || !strings.Contains(err.Error(), "protected credential") {
		t.Fatalf("verify() error = %v, want protected-credential failure", err)
	}
	if strings.Contains(err.Error(), token) {
		t.Fatal("verify() reflected protected credential")
	}
}

func TestSequentialServerRejectsCredentialInURIWithoutReflection(t *testing.T) {
	const token = "fixture-active-token"
	server := newSequentialServer([]HTTPExchange{{
		Request:  HTTPRequestExpected{Method: "GET", URI: "/safe"},
		Response: HTTPResponse{Status: 200},
	}}, token)
	req := httptest.NewRequest(http.MethodGet, "http://example.test/leak?token="+token, nil)
	response := httptest.NewRecorder()

	server.ServeHTTP(response, req)
	err := server.verify()

	if err == nil || !strings.Contains(err.Error(), "protected credential") {
		t.Fatalf("verify() error = %v, want protected-credential failure", err)
	}
	if strings.Contains(err.Error(), token) {
		t.Fatal("verify() reflected protected credential")
	}
}

func TestProtectedCredentialsIncludesEnvAndExpectedAuthorization(t *testing.T) {
	scenario := Scenario{
		Env: map[string]string{"MM_TOKEN": "lower-precedence-env-token"},
		HTTP: []HTTPExchange{{
			Request: HTTPRequestExpected{
				Headers: map[string]string{"authorization": "Bearer active-cli-or-config-token"},
			},
		}},
	}

	got := protectedCredentials(scenario)
	for _, want := range []string{
		"lower-precedence-env-token",
		"Bearer active-cli-or-config-token",
		"active-cli-or-config-token",
	} {
		if !slices.Contains(got, want) {
			t.Fatalf("protectedCredentials() = %q, missing %q", got, want)
		}
	}
}
