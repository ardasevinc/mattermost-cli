package presentation

import (
	"strings"
	"testing"
)

func TestDetectSecretsPatterns(t *testing.T) {
	repeat := strings.Repeat
	cases := []struct{ kind, text, value string }{
		{"aws_access_key", "x AKIAIOSFODNN7EXAMPLE y", "AKIAIOSFODNN7EXAMPLE"},
		{"aws_secret_key", "aws_secret_access_key = " + repeat("a", 40), repeat("a", 40)},
		{"github_stateless_token", "(" + "ghs_" + repeat("a.b_-", 8) + ")", "ghs_" + repeat("a.b_-", 8)},
		{"github_token", "ghp_" + repeat("a", 36), "ghp_" + repeat("a", 36)},
		// github_oauth is intentionally suppressed by the earlier, identical-span
		// github_token match, matching the TypeScript detector's seen-span rule.
		{"github_token", "gho_" + repeat("a", 36), "gho_" + repeat("a", 36)},
		{"github_fine_grained_token", "github_pat_" + repeat("a", 22), "github_pat_" + repeat("a", 22)},
		{"gitlab_token", "glpat-" + repeat("a", 20), "glpat-" + repeat("a", 20)},
		{"slack_app_token", "xapp-1-" + repeat("a-", 8), "xapp-1-" + repeat("a-", 8)},
		{"slack_token", "xoxb-1234567890-1234567890-" + repeat("a", 24), "xoxb-1234567890-1234567890-" + repeat("a", 24)},
		{"slack_webhook", "https://hooks.slack.com/services/TABC/BDEF/abc123", "https://hooks.slack.com/services/TABC/BDEF/abc123"},
		{"discord_token", "M" + repeat("a", 23) + ".abcdef." + repeat("z", 27), "M" + repeat("a", 23) + ".abcdef." + repeat("z", 27)},
		{"discord_webhook", "https://discordapp.com/api/webhooks/123/abc_DEF", "https://discordapp.com/api/webhooks/123/abc_DEF"},
		{"jwt", "eyJ" + repeat("a", 10) + ".eyJ" + repeat("b", 10) + ".ccc", "eyJ" + repeat("a", 10) + ".eyJ" + repeat("b", 10) + ".ccc"},
		{"bearer_token", "Authorization: Bearer " + repeat("a", 20), repeat("a", 20)},
		{"basic_auth", "Basic " + repeat("A", 20), repeat("A", 20)},
		{"connection_string", "postgres://user:password@db.example/prod", "postgres://user:password@db.example/prod"},
		{"api_key", "api-key='" + repeat("A", 20) + "'", repeat("A", 20)},
		{"password", "passwd: hunter22", "hunter22"},
		{"private_key", "-----BEGIN EC PRIVATE KEY-----\nabc\n-----END EC PRIVATE KEY-----", "-----BEGIN EC PRIVATE KEY-----\nabc\n-----END EC PRIVATE KEY-----"},
		{"mattermost_token", "Mattermost token: 9xuqwrwgstrb3mzrxb83nb357a", "9xuqwrwgstrb3mzrxb83nb357a"},
		{"stripe_key", "sk_live_" + repeat("a", 24), "sk_live_" + repeat("a", 24)},
		{"stripe_restricted_key", "rk_test_" + repeat("a", 24), "rk_test_" + repeat("a", 24)},
		{"sendgrid_key", "SG." + repeat("a", 22) + "." + repeat("b", 43), "SG." + repeat("a", 22) + "." + repeat("b", 43)},
		{"twilio_key", "SK" + repeat("a", 32), "SK" + repeat("a", 32)},
		{"openai_key", "sk-" + repeat("A", 32), "sk-" + repeat("A", 32)},
		{"openai_project_key", "sk-proj-" + repeat("A", 32), "sk-proj-" + repeat("A", 32)},
		{"anthropic_key", "sk-ant-" + repeat("a", 32), "sk-ant-" + repeat("a", 32)},
		{"google_api_key", "AIza" + repeat("a", 35), "AIza" + repeat("a", 35)},
		// api_key owns this exact span before heroku_key gets to it.
		{"api_key", "HEROKU_API_KEY=12345678-1234-abcd-9876-123456789abc", "12345678-1234-abcd-9876-123456789abc"},
		{"npm_token", "npm_" + repeat("a", 36), "npm_" + repeat("a", 36)},
		{"high_entropy_secret", "credential=" + repeat("Z", 32), repeat("Z", 32)},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			for _, got := range DetectSecrets(tc.text) {
				if got.Type == tc.kind && got.Value == tc.value && tc.text[got.Start:got.End] == tc.value {
					return
				}
			}
			t.Fatalf("missing %s=%q in %#v", tc.kind, tc.value, DetectSecrets(tc.text))
		})
	}
}

func TestSecretPatternOrderMatchesV1(t *testing.T) {
	want := []string{
		"aws_access_key", "aws_secret_key", "github_stateless_token", "github_token", "github_oauth",
		"github_fine_grained_token", "gitlab_token", "slack_app_token", "slack_token", "slack_webhook",
		"discord_token", "discord_webhook", "jwt", "bearer_token", "basic_auth", "connection_string",
		"api_key", "password", "mattermost_token", "stripe_key", "stripe_restricted_key", "sendgrid_key",
		"twilio_key", "openai_key", "openai_project_key", "anthropic_key", "google_api_key", "heroku_key",
		"npm_token", "high_entropy_secret",
	}
	if len(secretPatterns) != len(want) {
		t.Fatalf("pattern count = %d, want %d", len(secretPatterns), len(want))
	}
	for index := range want {
		if secretPatterns[index].name != want[index] {
			t.Fatalf("pattern[%d] = %q, want %q", index, secretPatterns[index].name, want[index])
		}
	}
}

func TestDetectSecretsBoundariesAndSpecialCases(t *testing.T) {
	stateless := "ghs_" + strings.Repeat("a", 36)
	slack := "xapp-1-" + strings.Repeat("a", 16)
	mm := "9xuqwrwgstrb3mzrxb83nb357a"
	for _, text := range []string{"prefix" + stateless, "a" + slack, "xapp-this-is-not-a-token", "post id: " + mm, "token=" + mm + "z"} {
		if got := DetectSecrets(text); len(got) != 0 {
			t.Errorf("DetectSecrets(%q) = %#v", text, got)
		}
	}
	for _, text := range []string{"token=" + mm + `"z`, "token=" + mm + `'z`} {
		got := DetectSecrets(text)
		if len(got) != 1 || got[0].Type != "mattermost_token" || got[0].Value != mm {
			t.Errorf("quoted Mattermost token boundary %q: %#v", text, got)
		}
	}

	truncated := "-----BEGIN OPENSSH PRIVATE KEY-----\n秘密🙂"
	got := DetectSecrets(truncated)
	if len(got) != 1 || got[0].Type != "private_key" || got[0].Value != truncated {
		t.Fatalf("truncated key: %#v", got)
	}
	for _, label := range []string{"PUBLIC KEY", "CERTIFICATE"} {
		if got := DetectSecrets("-----BEGIN " + label + "-----\nx\n-----END " + label + "-----"); len(got) != 0 {
			t.Errorf("matched %s: %#v", label, got)
		}
	}

	token := "ghp_" + strings.Repeat("a", 36)
	unicodeText := "🙂é " + token
	got = DetectSecrets(unicodeText)
	if len(got) != 1 || got[0].Start != len("🙂é ") || got[0].Value != token {
		t.Fatalf("UTF-8 offsets: %#v", got)
	}
}

func TestDetectSecretsUsesJavaScriptUnicodeWhitespace(t *testing.T) {
	token := strings.Repeat("A", 20)
	for _, text := range []string{
		"Bearer\u00a0" + token,
		"Basic\u3000" + token,
		"api_key\ufeff" + token,
		"password\u2028" + token,
	} {
		if got := DetectSecrets(text); len(got) == 0 || got[0].Value != token {
			t.Errorf("DetectSecrets(%q) = %#v", text, got)
		}
	}
}

func TestDetectSecretsOrderAndExactSpanDeduplication(t *testing.T) {
	oauth := "gho_" + strings.Repeat("a", 36)
	got := DetectSecrets(oauth)
	if len(got) != 1 || got[0].Type != "github_token" {
		t.Fatalf("precedence/dedupe: %#v", got)
	}

	overlap := "postgres://api_key=ABCDEFGHIJKLMNOPQRSTUVWXYZ123456:supersecret@db.internal/prod"
	got = DetectSecrets(overlap)
	if len(got) < 2 || got[0].Type != "connection_string" || got[1].Type != "api_key" {
		t.Fatalf("overlap order: %#v", got)
	}
}

func TestMaskSecret(t *testing.T) {
	if got := MaskSecret("abc123", "test"); got != "[REDACTED:test]" {
		t.Fatal(got)
	}
	if got := MaskSecret(strings.Repeat("a", 40), "test"); got != "aaaa...aaaa" {
		t.Fatal(got)
	}
	if got := MaskSecret("秘密🙂abcdefgh", "test"); got != "秘密...gh" {
		t.Fatal(got)
	}
	if got := MaskSecret("anything", "mattermost_credential"); got != credentialMask {
		t.Fatal(got)
	}
}
