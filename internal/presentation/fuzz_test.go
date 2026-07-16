package presentation

import (
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func FuzzSanitizeControlsIsIdempotentAndRemovesUnsafeCodePoints(f *testing.F) {
	for _, seed := range []string{
		"ordinary text",
		"first\r\nsecond\rforged\x1b[2J",
		"مرحبا\u200e 👩‍💻 café",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		once := SanitizeControls(input)
		if twice := SanitizeControls(once); twice != once {
			t.Fatalf("sanitization is not idempotent: once=%q twice=%q", once, twice)
		}
		for _, character := range once {
			if unsafeControl(character) {
				t.Fatalf("sanitized output retained unsafe code point U+%04X", character)
			}
		}
	})
}

func FuzzPreprocessNeverEmitsExactActiveCredential(f *testing.F) {
	f.Add("before ", " after", []byte("token"))
	f.Add("👩‍💻\x1b", "\u202eend", []byte{0, 1, 2, 255})
	f.Fuzz(func(t *testing.T, prefix, suffix string, credentialBytes []byte) {
		credential := "ACTIVE-" + hex.EncodeToString(credentialBytes)
		result := PreprocessWithOptions(prefix+credential+suffix, Options{
			Credentials:       []string{credential},
			DisableHeuristics: true,
		})
		if strings.Contains(result.Text, credential) {
			t.Fatal("preprocessed output retained the exact active credential")
		}
		encoded, err := json.Marshal(result.Redactions)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), credential) {
			t.Fatal("redaction provenance retained the exact active credential")
		}
		position := strings.Index(result.Text, credentialMask)
		if position < 0 || len(result.Redactions) == 0 {
			t.Fatalf("credential mask missing: %+v", result)
		}
		if got, want := result.Redactions[0].Position, utf16Length(result.Text[:position]); got != want {
			t.Fatalf("position = %d, want %d", got, want)
		}
	})
}
