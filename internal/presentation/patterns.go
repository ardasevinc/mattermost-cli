package presentation

import (
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// DetectedSecret uses byte offsets so callers can safely slice the original
// UTF-8 string. Presentation boundaries may convert these to UTF-16 offsets.
type DetectedSecret struct {
	Type  string
	Value string
	Start int
	End   int
}

type secretPattern struct {
	name  string
	re    *regexp.Regexp
	valid func(string, int, int) bool
}

func pattern(name, expression string) secretPattern {
	return secretPattern{name: name, re: regexp.MustCompile(expression)}
}

const jsWhitespaceClass = `\x{0009}\x{000a}\x{000b}\x{000c}\x{000d}\x{0020}\x{00a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}`

var secretPatterns = []secretPattern{
	pattern("aws_access_key", `\b((?:AKIA|ASIA)[0-9A-Z]{16})\b`),
	pattern("aws_secret_key", `(?i)(?:aws[_-]?secret[_-]?(?:access[_-]?)?key|secret[_-]?key)["`+jsWhitespaceClass+`:=]+["']?([A-Za-z0-9/+=]{40})["']?`),
	{name: "github_stateless_token", re: regexp.MustCompile(`(ghs_[A-Za-z0-9._-]{36,})`), valid: boundedBy(`[A-Za-z0-9._-]`)},
	pattern("github_token", `\b(gh[pousr]_[A-Za-z0-9_]{36,255})\b`),
	pattern("github_oauth", `\b(gho_[A-Za-z0-9]{36,255})\b`),
	pattern("github_fine_grained_token", `\b(github_pat_[A-Za-z0-9_]{22,255})\b`),
	pattern("gitlab_token", `\b(glpat-[A-Za-z0-9_-]{20,})\b`),
	{name: "slack_app_token", re: regexp.MustCompile(`(xapp-[0-9]+-[A-Za-z0-9-]{16,})`), valid: boundedBy(`[A-Za-z0-9-]`)},
	pattern("slack_token", `\b(xox[baprs]-[0-9]{10,13}-[0-9]{10,13}-[a-zA-Z0-9]{24,})\b`),
	pattern("slack_webhook", `(https://hooks\.slack\.com/services/T[A-Z0-9]+/B[A-Z0-9]+/[a-zA-Z0-9]+)`),
	pattern("discord_token", `\b([MN][A-Za-z\d]{23,}\.[\w-]{6}\.[\w-]{27,})\b`),
	pattern("discord_webhook", `(https://discord(?:app)?\.com/api/webhooks/\d+/[A-Za-z0-9_-]+)`),
	pattern("jwt", `\b(eyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]+)\b`),
	pattern("bearer_token", `(?i)\bBearer[`+jsWhitespaceClass+`]+([A-Za-z0-9_.-]{20,})\b`),
	pattern("basic_auth", `(?i)\bBasic[`+jsWhitespaceClass+`]+([A-Za-z0-9+/=]{20,})\b`),
	pattern("connection_string", `(?i)\b((?:mongodb|postgres|postgresql|mysql|redis|amqp|amqps)://[^:]+:[^@`+jsWhitespaceClass+`]+@[^`+jsWhitespaceClass+`"']+)\b`),
	pattern("api_key", `(?i)(?:api[_-]?key|apikey|api[_-]?secret)["`+jsWhitespaceClass+`:=]+["']?([A-Za-z0-9_-]{20,})["']?`),
	pattern("password", `(?i)(?:password|passwd|pwd|secret)["`+jsWhitespaceClass+`:=]+["']?([^`+jsWhitespaceClass+`"']{8,})["']?`),
	// private_key is handled separately because RE2 has no backreferences.
	pattern("mattermost_token", `(?i)(?:\bmm(?:auth)?[_-]?token\b|\bmm[_-]?pat\b|\bmattermost[`+jsWhitespaceClass+`_-]?(?:pat|token)\b|\bpat\b|\btoken\b)["`+jsWhitespaceClass+`:=]+["']?([a-z0-9]{26})["']?`),
	pattern("stripe_key", `\b(sk_(?:live|test)_[A-Za-z0-9]{24,})\b`),
	pattern("stripe_restricted_key", `\b(rk_(?:live|test)_[A-Za-z0-9]{24,})\b`),
	pattern("sendgrid_key", `\b(SG\.[A-Za-z0-9_-]{22}\.[A-Za-z0-9_-]{43})\b`),
	pattern("twilio_key", `\b(SK[a-f0-9]{32})\b`),
	pattern("openai_key", `\b(sk-[A-Za-z0-9]{32,})\b`),
	pattern("openai_project_key", `\b(sk-proj-[A-Za-z0-9_-]{32,})\b`),
	pattern("anthropic_key", `\b(sk-ant-[A-Za-z0-9_-]{32,})\b`),
	pattern("google_api_key", `\b(AIza[A-Za-z0-9_-]{35})\b`),
	pattern("heroku_key", `(?i)(?:heroku[_-]?api[_-]?key|HEROKU_API_KEY)["`+jsWhitespaceClass+`:=]+["']?([A-Fa-f0-9]{8}-[A-Fa-f0-9]{4}-[A-Fa-f0-9]{4}-[A-Fa-f0-9]{4}-[A-Fa-f0-9]{12})["']?`),
	pattern("npm_token", `\b(npm_[A-Za-z0-9]{36})\b`),
	pattern("high_entropy_secret", `(?i)(?:token|secret|key|auth|credential)["`+jsWhitespaceClass+`:=]+["']?([A-Za-z0-9_-]{32,64})["']?`),
}

var privateKeyBegin = regexp.MustCompile(`-----BEGIN ((?:[A-Z0-9]+ )*PRIVATE KEY)-----`)

func DetectSecrets(text string) []DetectedSecret {
	secrets := make([]DetectedSecret, 0)
	seen := make(map[[2]int]struct{})
	add := func(kind string, start, end int) {
		key := [2]int{start, end}
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		secrets = append(secrets, DetectedSecret{Type: kind, Value: text[start:end], Start: start, End: end})
	}

	for _, current := range secretPatterns {
		for _, match := range current.re.FindAllStringSubmatchIndex(text, -1) {
			start, end := match[0], match[1]
			if len(match) >= 4 && match[2] >= 0 {
				start, end = match[2], match[3]
			}
			if current.name == "mattermost_token" && end < len(text) && text[end] != '\'' && text[end] != '"' && isASCIIAlnum(text[end]) {
				continue
			}
			if current.valid == nil || current.valid(text, start, end) {
				add(current.name, start, end)
			}
		}
		if current.name == "password" { // Preserve the TS pattern order.
			for _, match := range privateKeyMatches(text) {
				add("private_key", match[0], match[1])
			}
		}
	}
	sort.SliceStable(secrets, func(i, j int) bool { return secrets[i].Start < secrets[j].Start })
	return secrets
}

func MaskSecret(value, kind string) string {
	if kind == "mattermost_credential" {
		return credentialMask
	}
	runes := []rune(value)
	if len(runes) <= 8 {
		return "[REDACTED:" + kind + "]"
	}
	visible := len(runes) / 10
	if visible < 2 {
		visible = 2
	}
	if visible > 4 {
		visible = 4
	}
	return string(runes[:visible]) + "..." + string(runes[len(runes)-visible:])
}

func boundedBy(class string) func(string, int, int) bool {
	re := regexp.MustCompile(`^` + class + `$`)
	return func(text string, start, end int) bool {
		beforeOK := start == 0
		if !beforeOK {
			r, _ := utf8.DecodeLastRuneInString(text[:start])
			beforeOK = !re.MatchString(string(r))
		}
		afterOK := end == len(text)
		if !afterOK {
			r, _ := utf8.DecodeRuneInString(text[end:])
			afterOK = !re.MatchString(string(r))
		}
		return beforeOK && afterOK
	}
}

func isASCIIAlnum(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9'
}

func privateKeyMatches(text string) [][2]int {
	var matches [][2]int
	for offset := 0; offset < len(text); {
		begin := privateKeyBegin.FindStringSubmatchIndex(text[offset:])
		if begin == nil {
			break
		}
		start := offset + begin[0]
		bodyStart := offset + begin[1]
		label := text[offset+begin[2] : offset+begin[3]]
		endMarker := "-----END " + label + "-----"
		end := len(text)
		if relative := strings.Index(text[bodyStart:], endMarker); relative >= 0 {
			end = bodyStart + relative + len(endMarker)
		}
		matches = append(matches, [2]int{start, end})
		offset = end
	}
	return matches
}
