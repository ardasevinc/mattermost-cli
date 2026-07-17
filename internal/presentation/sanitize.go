package presentation

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf16"
)

const credentialMask = "[REDACTED:mattermost_credential]"

type Redaction struct {
	Type     string `json:"type"`
	Masked   string `json:"masked"`
	Position int    `json:"position"`
	Field    string `json:"field,omitempty"`
}

type Result struct {
	Text       string      `json:"text"`
	Redactions []Redaction `json:"redactions"`
}

type span struct {
	start   int
	end     int
	secrets []DetectedSecret
}

func SanitizeControls(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	var output strings.Builder
	output.Grow(len(text))
	for _, character := range text {
		if unsafeControl(character) {
			_, _ = fmt.Fprintf(&output, "\\u%04x", character)
			continue
		}
		output.WriteRune(character)
	}
	return output.String()
}

func SanitizeLabel(text string) string {
	return strings.NewReplacer("\n", "\\n", "\t", "\\t").Replace(SanitizeControls(text))
}

func Preprocess(text string, credentials []string) Result {
	return PreprocessWithOptions(text, Options{Credentials: credentials})
}

type Options struct {
	Credentials       []string
	DisableHeuristics bool
}

func PreprocessWithOptions(text string, options Options) Result {
	secrets := make([]DetectedSecret, 0)
	if !options.DisableHeuristics {
		secrets = append(secrets, DetectSecrets(text)...)
	}
	secrets = append(secrets, exactCredentialSecrets(text, options.Credentials)...)
	spans := groupOverlappingSecrets(secrets)
	if len(spans) == 0 {
		return Result{Text: SanitizeControls(text), Redactions: []Redaction{}}
	}
	var raw strings.Builder
	raw.Grow(len(text))
	redactions := make([]Redaction, 0, len(spans))
	positions := make([]int, 0, len(spans))
	cursor := 0
	for _, current := range spans {
		raw.WriteString(text[cursor:current.start])
		positions = append(positions, raw.Len())
		kind, types := dominantSecretTypes(current.secrets)
		masked := MaskSecret(text[current.start:current.end], kind)
		raw.WriteString(masked)
		redactions = append(redactions, Redaction{
			Type: strings.Join(types, "+"), Masked: SanitizeControls(masked),
		})
		cursor = current.end
	}
	raw.WriteString(text[cursor:])
	rawText := raw.String()
	for index, position := range positions {
		redactions[index].Position = utf16Length(SanitizeControls(rawText[:position]))
	}
	return Result{Text: SanitizeControls(rawText), Redactions: redactions}
}

func PreprocessActive(text string) Result {
	return Preprocess(text, ActiveCredentials.Values())
}

func exactCredentialSecrets(text string, credentials []string) []DetectedSecret {
	var secrets []DetectedSecret
	seenCredential := make(map[string]struct{}, len(credentials))
	for _, credential := range credentials {
		if credential == "" {
			continue
		}
		if _, seen := seenCredential[credential]; seen {
			continue
		}
		seenCredential[credential] = struct{}{}
		for offset := 0; offset <= len(text)-len(credential); {
			index := strings.Index(text[offset:], credential)
			if index < 0 {
				break
			}
			start := offset + index
			secrets = append(secrets, DetectedSecret{
				Type: "mattermost_credential", Value: credential, Start: start, End: start + len(credential),
			})
			offset = start + len(credential)
		}
	}
	return secrets
}

func groupOverlappingSecrets(secrets []DetectedSecret) []span {
	sort.SliceStable(secrets, func(i, j int) bool {
		if secrets[i].Start != secrets[j].Start {
			return secrets[i].Start < secrets[j].Start
		}
		return secrets[i].End > secrets[j].End
	})
	groups := make([]span, 0, len(secrets))
	for _, secret := range secrets {
		if len(groups) == 0 || secret.Start >= groups[len(groups)-1].end {
			groups = append(groups, span{start: secret.Start, end: secret.End, secrets: []DetectedSecret{secret}})
			continue
		}
		current := &groups[len(groups)-1]
		if secret.End > current.end {
			current.end = secret.End
		}
		current.secrets = append(current.secrets, secret)
	}
	return groups
}

func dominantSecretTypes(secrets []DetectedSecret) (string, []string) {
	types := make([]string, 0, len(secrets))
	seen := make(map[string]struct{}, len(secrets))
	dominant := "secret"
	for _, secret := range secrets {
		if _, exists := seen[secret.Type]; !exists {
			seen[secret.Type] = struct{}{}
			types = append(types, secret.Type)
		}
		if secret.Type == "mattermost_credential" {
			dominant = secret.Type
		}
	}
	if dominant == "secret" && len(types) > 0 {
		dominant = types[0]
	}
	if dominant == "mattermost_credential" && len(types) > 0 && types[0] != dominant {
		for index, kind := range types {
			if kind == dominant {
				types = append(types[:index], types[index+1:]...)
				break
			}
		}
		types = append([]string{dominant}, types...)
	}
	return dominant, types
}

func unsafeControl(character rune) bool {
	return (character >= 0x0000 && character <= 0x0008) ||
		(character >= 0x000b && character <= 0x001f) ||
		(character >= 0x007f && character <= 0x009f) ||
		character == 0x061c || character == 0x200e || character == 0x200f ||
		(character >= 0x202a && character <= 0x202e) ||
		(character >= 0x2066 && character <= 0x2069)
}

func utf16Length(text string) int {
	return len(utf16.Encode([]rune(text)))
}
