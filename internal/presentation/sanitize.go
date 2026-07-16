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
	start int
	end   int
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
	spans := credentialSpans(text, credentials)
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
		raw.WriteString(credentialMask)
		redactions = append(redactions, Redaction{Type: "mattermost_credential", Masked: credentialMask})
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

func credentialSpans(text string, credentials []string) []span {
	var spans []span
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
			spans = append(spans, span{start: start, end: start + len(credential)})
			offset = start + len(credential)
		}
	}
	sort.Slice(spans, func(i, j int) bool {
		if spans[i].start != spans[j].start {
			return spans[i].start < spans[j].start
		}
		return spans[i].end > spans[j].end
	})
	merged := make([]span, 0, len(spans))
	for _, current := range spans {
		if len(merged) == 0 || current.start >= merged[len(merged)-1].end {
			merged = append(merged, current)
			continue
		}
		if current.end > merged[len(merged)-1].end {
			merged[len(merged)-1].end = current.end
		}
	}
	return merged
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
