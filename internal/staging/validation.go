package staging

import (
	"bytes"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ardasevinc/mattermost-cli/internal/mattermost"
	"github.com/ardasevinc/mattermost-cli/internal/stagestore"
)

func targetStrings(t Target) []string {
	v := []string{t.Value}
	if t.Team != nil {
		v = append(v, t.Team.Value)
	}
	return v
}

func credentialBytes(values []string) [][]byte {
	out := make([][]byte, 0, len(values))
	for _, v := range values {
		if v != "" {
			out = append(out, []byte(v))
		}
	}
	return out
}

func cloneCredentials(values [][]byte) [][]byte {
	out := make([][]byte, len(values))
	for i := range values {
		out[i] = bytes.Clone(values[i])
	}
	return out
}

func credentialsByLength(values [][]byte) [][]byte {
	out := make([][]byte, 0, len(values))
	for _, value := range values {
		if len(value) > 0 {
			out = append(out, bytes.Clone(value))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if len(out[i]) != len(out[j]) {
			return len(out[i]) > len(out[j])
		}
		return bytes.Compare(out[i], out[j]) < 0
	})
	return out
}

func containsCredential(credentials [][]byte, value []byte) bool {
	for _, credential := range credentials {
		if bytes.Contains(value, credential) {
			return true
		}
	}
	return false
}

func contaminated(credentials [][]byte, values ...string) bool {
	for _, value := range values {
		if containsCredential(credentials, []byte(value)) {
			return true
		}
	}
	return false
}

func attachmentsContaminated(credentials [][]byte, values []stagestore.Attachment) bool {
	for _, value := range values {
		if contaminated(credentials, value.SuppliedPath, value.CanonicalPath, value.RemoteFilename, value.MediaType) {
			return true
		}
	}
	return false
}

func validSelectorValue(value string) bool {
	if value == "" || len(value) > 256 || !utf8.ValidString(value) || value != strings.TrimSpace(value) {
		return false
	}
	for _, r := range value {
		if unsafeIdentityRune(r) || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

func validResolvedUser(user mattermost.User) bool {
	return validSelectorValue(user.ID) && validSelectorValue(user.Username)
}

func validResolvedChannel(channel mattermost.Channel) bool {
	if !validSelectorValue(channel.ID) || !validSelectorValue(channel.Name) ||
		(channel.DisplayName != "" && !validSafeText(channel.DisplayName, 256)) {
		return false
	}
	switch channel.Type {
	case "D", "G":
		return channel.TeamID == ""
	case "O", "P":
		return validSelectorValue(channel.TeamID)
	default:
		return false
	}
}

func validResolvedTeam(team mattermost.Team) bool {
	return validSelectorValue(team.ID) && validSelectorValue(team.Name) &&
		(team.DisplayName == "" || validSafeText(team.DisplayName, 256)) && (team.Type == "O" || team.Type == "I")
}

func validBoundAttachments(values []stagestore.Attachment) bool {
	if len(values) > 5 {
		return false
	}
	for _, value := range values {
		if !validBoundText(value.SuppliedPath, 4096) || !validBoundText(value.CanonicalPath, 4096) ||
			!validBoundText(value.RemoteFilename, 255) || (value.MediaType != "" && !validBoundText(value.MediaType, 255)) ||
			value.ByteLength <= 0 || value.ContentDigest == ([32]byte{}) {
			return false
		}
	}
	return true
}

func validBoundText(value string, maximum int) bool {
	if value == "" || len(value) > maximum || !utf8.ValidString(value) || value != strings.TrimSpace(value) {
		return false
	}
	for _, r := range value {
		if unsafeRune(r) {
			return false
		}
	}
	return true
}

func validSafeText(value string, maximum int) bool {
	return value != "" && len(value) <= maximum && utf8.ValidString(value) && value == strings.TrimSpace(value) && !strings.ContainsFunc(value, unsafeRune)
}

func unsafeRune(r rune) bool {
	return unicode.IsControl(r) || r == '\u061c' || r == '\u200e' || r == '\u200f' ||
		r >= '\u202a' && r <= '\u202e' || r >= '\u2066' && r <= '\u2069'
}

func unsafeIdentityRune(r rune) bool {
	return unsafeRune(r) || r >= '\u200b' && r <= '\u200d' || r == '\ufeff'
}

func validRequestID(value string) bool {
	if value == "" {
		return true
	}
	if len(value) > 256 || !requestCharacter(value[0], true) {
		return false
	}
	for i := 1; i < len(value); i++ {
		if !requestCharacter(value[i], false) {
			return false
		}
	}
	return true
}

func requestCharacter(value byte, first bool) bool {
	if value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z' || value >= '0' && value <= '9' {
		return true
	}
	return !first && strings.ContainsRune("._~:-", rune(value))
}
