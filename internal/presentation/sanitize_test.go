package presentation

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestSanitizeControlsMakesTerminalHazardsVisible(t *testing.T) {
	input := "before\x1b[2Jmiddle\x1b]52;c;YXR0YWNrZXI=\aafter"
	want := `before\u001b[2Jmiddle\u001b]52;c;YXR0YWNrZXI=\u0007after`
	if got := SanitizeControls(input); got != want {
		t.Fatalf("SanitizeControls() = %q, want %q", got, want)
	}
}

func TestSanitizeControlsNormalizesOnlyCRLFAndPreservesUnicode(t *testing.T) {
	input := "first\r\nsecond\rforged\n\tindented 👩‍💻 café العربية\u202e"
	want := "first\nsecond\\u000dforged\n\tindented 👩‍💻 café العربية\\u202e"
	if got := SanitizeControls(input); got != want {
		t.Fatalf("SanitizeControls() = %q, want %q", got, want)
	}
	if got := SanitizeLabel("line\n\tvalue"); got != `line\n\tvalue` {
		t.Fatalf("SanitizeLabel() = %q", got)
	}
}

func TestCredentialRegistryOwnershipAndReplacement(t *testing.T) {
	var registry CredentialRegistry
	registry.SetDefault("default-one")
	registry.SetDefault("")
	releaseA := registry.Register("shared")
	releaseB := registry.Register("shared")
	registry.SetDefault("default-two")
	if got := registry.Values(); !slices.Equal(got, []string{"default-two", "shared"}) {
		t.Fatalf("Values() = %q", got)
	}
	releaseA()
	if got := registry.Values(); !slices.Equal(got, []string{"default-two", "shared"}) {
		t.Fatalf("Values() after one release = %q", got)
	}
	releaseB()
	releaseB()
	if got := registry.Values(); !slices.Equal(got, []string{"default-two"}) {
		t.Fatalf("Values() after release = %q", got)
	}
	registry.Clear()
	if len(registry.Values()) != 0 {
		t.Fatal("Clear() retained credentials")
	}
}

func TestCredentialRegistryPreservesOwnerInsertionOrder(t *testing.T) {
	var registry CredentialRegistry
	release := registry.Register("socket")
	defer release()
	registry.SetDefault("config")
	registry.SetDefault("replacement")
	if got := registry.Values(); !slices.Equal(got, []string{"socket", "replacement"}) {
		t.Fatalf("Values() = %q", got)
	}
}

func TestPreprocessMasksExactCredentialAndOmitsOriginal(t *testing.T) {
	const credential = "short"
	result := Preprocess("before short after short", []string{credential})
	want := "before " + credentialMask + " after " + credentialMask
	if result.Text != want || len(result.Redactions) != 2 {
		t.Fatalf("Preprocess() = %+v, want %q", result, want)
	}
	encoded, err := json.Marshal(result.Redactions)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), credential) {
		t.Fatalf("redaction provenance leaked credential: %s", encoded)
	}
}

func TestPreprocessMergesOverlappingCredentialsAndUsesUTF16Position(t *testing.T) {
	result := Preprocess("👩‍💻 xxabcdefyy", []string{"abcdef", "cdefyy"})
	if got, want := result.Text, "👩‍💻 xx"+credentialMask; got != want {
		t.Fatalf("Text = %q, want %q", got, want)
	}
	if got, want := result.Redactions[0].Position, 8; got != want {
		t.Fatalf("Position = %d, want UTF-16 offset %d", got, want)
	}
}

func TestPreprocessSanitizesPrefixBeforeComputingPosition(t *testing.T) {
	result := Preprocess("a\r\n\x1btoken", []string{"token"})
	if got, want := result.Text, "a\n\\u001b"+credentialMask; got != want {
		t.Fatalf("Text = %q, want %q", got, want)
	}
	if got, want := result.Redactions[0].Position, len("a\n\\u001b"); got != want {
		t.Fatalf("Position = %d, want %d", got, want)
	}
}

func TestPreprocessHeuristicRedactionCanBeDisabledWithoutDisablingCredentialMasking(t *testing.T) {
	token := "ghp_" + strings.Repeat("a", 36)
	active := "active-token"
	withPatterns := Preprocess(token, nil)
	if withPatterns.Text == token || len(withPatterns.Redactions) != 1 {
		t.Fatalf("heuristic redaction missing: %+v", withPatterns)
	}
	withoutPatterns := PreprocessWithOptions(token+" "+active, Options{
		Credentials: []string{active}, DisableHeuristics: true,
	})
	if got, want := withoutPatterns.Text, token+" "+credentialMask; got != want {
		t.Fatalf("disabled heuristic result = %q, want %q", got, want)
	}
}

func TestPreprocessNeverReappendsOverlappingPlaintext(t *testing.T) {
	text := "postgres://api_key=ABCDEFGHIJKLMNOPQRSTUVWXYZ123456:supersecret@db.internal/prod"
	result := Preprocess(text, nil)
	if len(result.Redactions) != 1 || result.Redactions[0].Type != "connection_string+api_key" {
		t.Fatalf("redactions = %+v", result.Redactions)
	}
	for _, secret := range []string{"ABCDEFGHIJKLMNOPQRSTUVWXYZ123456", "supersecret", "db.internal"} {
		if strings.Contains(result.Text, secret) {
			t.Fatalf("redacted text leaked %q: %q", secret, result.Text)
		}
	}
}

func TestPreprocessSanitizesRedactionProvenanceMasks(t *testing.T) {
	result := Preprocess("password=\x1bAAAAAAAAX", nil)
	if len(result.Redactions) != 1 {
		t.Fatalf("redactions = %+v", result.Redactions)
	}
	if strings.ContainsRune(result.Redactions[0].Masked, '\x1b') {
		t.Fatalf("provenance retained live escape: %+v", result.Redactions[0])
	}
	if !strings.Contains(result.Redactions[0].Masked, `\u001b`) {
		t.Fatalf("provenance did not make escape visible: %+v", result.Redactions[0])
	}
}
