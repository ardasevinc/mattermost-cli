package serverurl

import (
	"errors"
	"strings"
	"testing"
)

func TestNormalizeAllowsHTTPSAndLoopbackHTTP(t *testing.T) {
	for _, input := range []string{
		"https://mattermost.example.com",
		"https://mattermost.example.com:8443/chat",
		"http://localhost:8065",
		"http://127.0.0.1:8065",
		"http://127.0.0.2:8065",
		"http://[::1]:8065",
	} {
		t.Run(input, func(t *testing.T) {
			if _, err := Normalize(input); err != nil {
				t.Fatalf("Normalize() error = %v", err)
			}
		})
	}
}

func TestNormalizeRejectsRemotePlaintext(t *testing.T) {
	for _, input := range []string{
		"http://mattermost.example.com",
		"http://localhost.evil:8065",
		"http://127.evil:8065",
	} {
		t.Run(input, func(t *testing.T) {
			if _, err := Normalize(input); !errors.Is(err, ErrPlaintext) {
				t.Fatalf("Normalize() error = %v, want ErrPlaintext", err)
			}
		})
	}
}

func TestNormalizeRejectsUnsafeOrAmbiguousURLsWithoutReflection(t *testing.T) {
	secret := "secret-token-value"
	tests := []struct {
		input string
		want  error
	}{
		{"not a url with " + secret, ErrInvalid},
		{"ftp://mattermost.example.com", ErrScheme},
		{"https://user:" + secret + "@mattermost.example.com", ErrAmbiguous},
		{"https://mattermost.example.com?token=" + secret, ErrAmbiguous},
		{"https://mattermost.example.com#" + secret, ErrAmbiguous},
		{"https://mattermost.example.com:invalid", ErrInvalid},
		{"https://mattermost.example.com:65536", ErrInvalid},
		{"http://127.0.0.256:8065", ErrInvalid},
		{"http://127.000.000.001:8065", ErrInvalid},
		{"http://127.1:8065", ErrInvalid},
		{"https://127.000.000.001", ErrInvalid},
		{"https://127.1", ErrInvalid},
		{"https://0177.0.0.1", ErrInvalid},
		{"https://0x7f.0.0.1", ErrInvalid},
		{"https://127.0.0.1.", ErrInvalid},
		{"https://127.000.000.001.", ErrInvalid},
		{"https://127.1.", ErrInvalid},
		{"https://0x7f.", ErrInvalid},
		{"https://2130706433.", ErrInvalid},
		{"https:\\mattermost.example.com", ErrInvalid},
	}
	for _, test := range tests {
		_, err := Normalize(test.input)
		if !errors.Is(err, test.want) {
			t.Errorf("Normalize(%q) error = %v, want %v", test.input, err, test.want)
		}
		if err != nil && strings.Contains(err.Error(), secret) {
			t.Errorf("Normalize() reflected secret in error: %v", err)
		}
	}
}

func TestNormalizeCanonicalizesWithoutLosingBasePath(t *testing.T) {
	tests := map[string]string{
		"  HTTPS://Mattermost.Example.Com///  ":   "https://mattermost.example.com",
		"https://Mattermost.Example.Com/chat/":    "https://mattermost.example.com/chat",
		"https://mattermost.example.com?":         "https://mattermost.example.com",
		"https://mattermost.example.com#":         "https://mattermost.example.com",
		"https://mattermost.example.com:0443/":    "https://mattermost.example.com",
		"http://localhost:080/":                   "http://localhost",
		"https://mattermost.example.com:00081/":   "https://mattermost.example.com:81",
		"https://bücher.example/":                 "https://xn--bcher-kva.example",
		"https://mattermost.example.com/a/../b/":  "https://mattermost.example.com/b",
		"https://mattermost.example.com/%2e%2e/b": "https://mattermost.example.com/b",
	}
	for input, want := range tests {
		got, err := Normalize(input)
		if err != nil {
			t.Fatalf("Normalize(%q) error = %v", input, err)
		}
		if got != want {
			t.Fatalf("Normalize(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestBuildPostPermalinkEncodesIDAndPreservesBasePath(t *testing.T) {
	got, err := BuildPostPermalink("https://mattermost.example.com/chat///", "post(special)")
	if err != nil {
		t.Fatal(err)
	}
	if want := "https://mattermost.example.com/chat/_redirect/pl/post%28special%29"; got != want {
		t.Fatalf("BuildPostPermalink() = %q, want %q", got, want)
	}
}

func FuzzNormalizeIsStable(f *testing.F) {
	for _, seed := range []string{
		"https://mattermost.example.com",
		"http://127.0.0.2:8065/chat/",
		"https://bücher.example/a/../b",
		"https://127.000.000.001",
		"https://user:secret@example.com",
		"not a url",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		first, err := Normalize(input)
		if err != nil {
			return
		}
		second, err := Normalize(first)
		if err != nil {
			t.Fatalf("Normalize(normalized) error = %v; normalized = %q", err, first)
		}
		if second != first {
			t.Fatalf("Normalize() is not stable: first %q, second %q", first, second)
		}
	})
}
