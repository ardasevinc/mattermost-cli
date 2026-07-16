package output

import (
	"errors"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/ardasevinc/mattermost-cli/internal/presentation"
)

const MaxSafeMachineInteger int64 = 9007199254740991

type RawIdentity struct {
	ID, Username, DisplayName, Nickname string
	Roles                               []string
}
type RawTeam struct{ ID, Name, DisplayName, Type string }
type RawUser struct{ ID, Username, DisplayName, Nickname string }
type RawChannel struct {
	ID, Type, Name, DisplayName, DirectUsername, TeamID string
	Team                                                *RawTeam
	LastPostAt, TotalMsgCount                           int64
}
type UsersRetrievalProof struct {
	RequestedLimit, ProbeCount int64
	Query, TeamID              string
}

type Identity struct {
	ID          string   `json:"id"`
	Username    string   `json:"username"`
	DisplayName *string  `json:"displayName"`
	Nickname    *string  `json:"nickname"`
	Roles       []string `json:"roles"`
}
type TeamItem struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	DisplayName *string `json:"displayName"`
	Type        string  `json:"type"`
}
type UserItem struct {
	ID          string  `json:"id"`
	Username    string  `json:"username"`
	DisplayName *string `json:"displayName"`
	Nickname    *string `json:"nickname"`
}
type UsersRetrieval struct {
	SelectedCount  int64   `json:"selectedCount"`
	RequestedLimit int64   `json:"requestedLimit"`
	Query          *string `json:"query"`
	TeamID         *string `json:"teamId"`
	Truncated      *bool   `json:"truncated"`
}
type ChannelTeam struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	DisplayName *string `json:"displayName"`
}
type ChannelItem struct {
	ID           string       `json:"id"`
	Type         string       `json:"type"`
	Name         string       `json:"name"`
	DisplayName  *string      `json:"displayName"`
	Team         *ChannelTeam `json:"team"`
	LastPost     *MillisTime  `json:"lastPost"`
	MessageCount int64        `json:"messageCount"`
}

type WhoAmIEnvelope struct {
	Schema string   `json:"schema"`
	Data   Identity `json:"data"`
	proof  *WhoAmIEnvelope
}
type TeamsEnvelope struct {
	Schema string     `json:"schema"`
	Teams  []TeamItem `json:"teams"`
	proof  *TeamsEnvelope
}
type UsersEnvelope struct {
	Schema    string         `json:"schema"`
	Users     []UserItem     `json:"users"`
	Retrieval UsersRetrieval `json:"retrieval"`
	proof     *UsersEnvelope
}
type ChannelsEnvelope struct {
	Schema   string        `json:"schema"`
	Channels []ChannelItem `json:"channels"`
	proof    *ChannelsEnvelope
}

func NewWhoAmIEnvelope(raw RawIdentity, options presentation.Options) (WhoAmIEnvelope, error) {
	if !rawRequired(raw.ID) || !rawRequired(raw.Username) {
		return WhoAmIEnvelope{}, errors.New("invalid raw identity")
	}
	roles := make([]string, len(raw.Roles))
	for i, role := range raw.Roles {
		if !rawRequired(role) {
			return WhoAmIEnvelope{}, errors.New("invalid raw identity role")
		}
		roles[i] = present(role, options)
	}
	doc := WhoAmIEnvelope{Schema: "mm/v2/whoami", Data: Identity{ID: present(raw.ID, options), Username: present(raw.Username, options), DisplayName: presentNullable(raw.DisplayName, options), Nickname: presentNullable(raw.Nickname, options), Roles: roles}}
	doc.proof = cloneWhoAmI(&doc)
	if err := validateIdentityDocument(doc); err != nil {
		return WhoAmIEnvelope{}, err
	}
	return doc, nil
}

func NewTeamsEnvelope(raw []RawTeam, options presentation.Options) (TeamsEnvelope, error) {
	values := append([]RawTeam(nil), raw...)
	sort.Slice(values, func(i, j int) bool {
		if values[i].Name != values[j].Name {
			return values[i].Name < values[j].Name
		}
		return values[i].ID < values[j].ID
	})
	teams := make([]TeamItem, len(values))
	seen := map[string]bool{}
	for i, value := range values {
		if !rawRequired(value.ID) || !rawRequired(value.Name) || seen[value.ID] {
			return TeamsEnvelope{}, errors.New("invalid raw team")
		}
		seen[value.ID] = true
		kind := map[string]string{"O": "open", "I": "invite_only"}[value.Type]
		if kind == "" {
			return TeamsEnvelope{}, errors.New("invalid raw team type")
		}
		teams[i] = TeamItem{ID: present(value.ID, options), Name: present(value.Name, options), DisplayName: presentNullable(value.DisplayName, options), Type: kind}
	}
	doc := TeamsEnvelope{Schema: "mm/v2/teams", Teams: teams}
	doc.proof = cloneTeamsEnvelope(&doc)
	if err := validateIdentityDocument(doc); err != nil {
		return TeamsEnvelope{}, err
	}
	return doc, nil
}

func NewUsersEnvelope(raw []RawUser, proof UsersRetrievalProof, options presentation.Options) (UsersEnvelope, error) {
	values := append([]RawUser(nil), raw...)
	sort.Slice(values, func(i, j int) bool {
		if values[i].Username != values[j].Username {
			return values[i].Username < values[j].Username
		}
		return values[i].ID < values[j].ID
	})
	users := make([]UserItem, len(values))
	seen := map[string]bool{}
	for i, value := range values {
		if !rawRequired(value.ID) || !rawRequired(value.Username) || seen[value.ID] {
			return UsersEnvelope{}, errors.New("invalid raw user")
		}
		seen[value.ID] = true
		users[i] = UserItem{ID: present(value.ID, options), Username: present(value.Username, options), DisplayName: presentNullable(value.DisplayName, options), Nickname: presentNullable(value.Nickname, options)}
	}
	retrieval, err := deriveRetrieval(int64(len(users)), proof, options)
	if err != nil {
		return UsersEnvelope{}, err
	}
	doc := UsersEnvelope{Schema: "mm/v2/users", Users: users, Retrieval: retrieval}
	doc.proof = cloneUsersEnvelope(&doc)
	if err := validateIdentityDocument(doc); err != nil {
		return UsersEnvelope{}, err
	}
	return doc, nil
}

func NewChannelsEnvelope(raw []RawChannel, options presentation.Options) (ChannelsEnvelope, error) {
	values := append([]RawChannel(nil), raw...)
	sort.Slice(values, func(i, j int) bool {
		left, right := normalizedLastPostAt(values[i].LastPostAt), normalizedLastPostAt(values[j].LastPostAt)
		if left != right {
			return left > right
		}
		return values[i].ID < values[j].ID
	})
	channels := make([]ChannelItem, len(values))
	seen := map[string]bool{}
	for i, value := range values {
		if !rawRequired(value.ID) || !rawRequired(value.Name) || seen[value.ID] || !safeCount(value.TotalMsgCount) {
			return ChannelsEnvelope{}, errors.New("invalid raw channel")
		}
		seen[value.ID] = true
		kind := map[string]string{"D": "dm", "O": "public", "P": "private", "G": "group"}[value.Type]
		if kind == "" {
			return ChannelsEnvelope{}, errors.New("invalid raw channel type")
		}
		item := ChannelItem{ID: present(value.ID, options), Type: kind, Name: present(value.Name, options), MessageCount: value.TotalMsgCount}
		item.LastPost = presentTimestamp(normalizedLastPostAt(value.LastPostAt))
		if value.Type == "D" {
			if !rawRequired(value.DirectUsername) || value.Team != nil || value.TeamID != "" {
				return ChannelsEnvelope{}, errors.New("invalid direct channel")
			}
			item.Name = "@" + present(value.DirectUsername, options)
		} else if value.Type == "G" {
			if value.Team != nil || value.TeamID != "" {
				return ChannelsEnvelope{}, errors.New("invalid group channel")
			}
			if strings.TrimSpace(value.DisplayName) != "" {
				item.Name = present(value.DisplayName, options)
			}
		} else {
			if value.Team == nil || !rawRequired(value.TeamID) || value.TeamID != value.Team.ID || !rawRequired(value.Team.ID) || !rawRequired(value.Team.Name) {
				return ChannelsEnvelope{}, errors.New("invalid team channel")
			}
			item.DisplayName = presentNullable(value.DisplayName, options)
			item.Team = &ChannelTeam{ID: present(value.Team.ID, options), Name: present(value.Team.Name, options), DisplayName: presentNullable(value.Team.DisplayName, options)}
		}
		channels[i] = item
	}
	doc := ChannelsEnvelope{Schema: "mm/v2/channels", Channels: channels}
	doc.proof = cloneChannelsEnvelope(&doc)
	if err := validateIdentityDocument(doc); err != nil {
		return ChannelsEnvelope{}, err
	}
	return doc, nil
}

func deriveRetrieval(selected int64, proof UsersRetrievalProof, options presentation.Options) (UsersRetrieval, error) {
	proof.Query = strings.TrimSpace(proof.Query)
	ceiling := int64(200)
	if proof.Query != "" {
		ceiling = 1000
	}
	if proof.RequestedLimit < 1 || !safeCount(proof.RequestedLimit) || selected < 0 || selected > proof.RequestedLimit || proof.ProbeCount < selected || proof.ProbeCount > ceiling {
		return UsersRetrieval{}, errors.New("invalid retrieval proof")
	}
	var truncated *bool
	falseValue := false
	trueValue := true
	if proof.RequestedLimit < ceiling && selected == proof.RequestedLimit && proof.ProbeCount == selected+1 {
		truncated = &trueValue
	} else if proof.RequestedLimit >= ceiling && selected == ceiling && proof.ProbeCount == ceiling {
		truncated = nil
	} else if proof.ProbeCount == selected {
		truncated = &falseValue
	} else {
		return UsersRetrieval{}, errors.New("contradictory retrieval proof")
	}
	return UsersRetrieval{SelectedCount: selected, RequestedLimit: proof.RequestedLimit, Query: presentNullable(proof.Query, options), TeamID: presentNullable(proof.TeamID, options), Truncated: truncated}, nil
}

func validateIdentityDocument(document MachineDocument) error {
	switch value := document.(type) {
	case WhoAmIEnvelope:
		if value.Schema != "mm/v2/whoami" || value.proof == nil || !reflect.DeepEqual(withoutWhoProof(value), withoutWhoProof(*value.proof)) {
			return errors.New("invalid whoami document")
		}
		if !validIdentity(value.Data) {
			return errors.New("invalid whoami data")
		}
	case TeamsEnvelope:
		if value.Schema != "mm/v2/teams" || value.Teams == nil || value.proof == nil || !reflect.DeepEqual(withoutTeamsProof(value), withoutTeamsProof(*value.proof)) {
			return errors.New("invalid teams document")
		}
		for _, item := range value.Teams {
			if !validTeamItem(item) {
				return errors.New("invalid team item")
			}
		}
	case UsersEnvelope:
		if value.Schema != "mm/v2/users" || value.Users == nil || value.proof == nil || value.Retrieval.SelectedCount != int64(len(value.Users)) || !reflect.DeepEqual(withoutUsersProof(value), withoutUsersProof(*value.proof)) {
			return errors.New("invalid users document")
		}
		if !validUsersRetrieval(value.Retrieval) {
			return errors.New("invalid users retrieval")
		}
		for _, item := range value.Users {
			if !validUserItem(item) {
				return errors.New("invalid user item")
			}
		}
	case ChannelsEnvelope:
		if value.Schema != "mm/v2/channels" || value.Channels == nil || value.proof == nil || !reflect.DeepEqual(withoutChannelsProof(value), withoutChannelsProof(*value.proof)) {
			return errors.New("invalid channels document")
		}
		for _, item := range value.Channels {
			if !validChannelItem(item) {
				return errors.New("invalid channel item")
			}
		}
	default:
		return errors.New("not an identity document")
	}
	return nil
}

func validTeamItem(value TeamItem) bool {
	return safeText(value.ID) && safeText(value.Name) && validNullable(value.DisplayName) && (value.Type == "open" || value.Type == "invite_only")
}
func validUserItem(value UserItem) bool {
	return safeText(value.ID) && safeText(value.Username) && validNullable(value.DisplayName) && validNullable(value.Nickname)
}
func validUsersRetrieval(value UsersRetrieval) bool {
	return safeCount(value.SelectedCount) && value.RequestedLimit >= 1 && safeCount(value.RequestedLimit) && validNullable(value.Query) && validNullable(value.TeamID)
}
func validChannelItem(value ChannelItem) bool {
	if !safeText(value.ID) || !safeText(value.Name) || !safeCount(value.MessageCount) || !validNullable(value.DisplayName) || !validMillisPointer(value.LastPost) {
		return false
	}
	switch value.Type {
	case "dm", "group":
		return value.Team == nil && value.DisplayName == nil
	case "public", "private":
		return value.Team != nil && safeText(value.Team.ID) && safeText(value.Team.Name) && validNullable(value.Team.DisplayName)
	default:
		return false
	}
}

func validIdentity(value Identity) bool {
	if !safeText(value.ID) || !safeText(value.Username) || !validNullable(value.DisplayName) || !validNullable(value.Nickname) || value.Roles == nil {
		return false
	}
	for _, role := range value.Roles {
		if !safeText(role) {
			return false
		}
	}
	return true
}
func safeText(value string) bool       { return strings.TrimSpace(value) != "" && safeDoctorString(value) }
func validNullable(value *string) bool { return value == nil || safeText(*value) }
func rawRequired(value string) bool {
	for _, current := range value {
		if !unicode.IsSpace(current) && !rawControlOrBidi(current) {
			return true
		}
	}
	return false
}
func rawControlOrBidi(current rune) bool {
	// Keep the bidi set aligned with presentation.unsafeControl. Required raw
	// identity values need a meaningful rune beyond anything presentation will
	// make visible as an unsafe directional control.
	return current < 0x20 || (current >= 0x7f && current <= 0x9f) ||
		current == 0x061c || current == 0x200e || current == 0x200f ||
		(current >= 0x202a && current <= 0x202e) || (current >= 0x2066 && current <= 0x2069)
}
func safeCount(value int64) bool { return value >= 0 && value <= MaxSafeMachineInteger }
func validMillisPointer(value *MillisTime) bool {
	if value == nil {
		return true
	}
	_, offset := value.Zone()
	return offset == 0 && value.Year() >= 1 && value.Year() <= 9999 && value.Nanosecond()%int(time.Millisecond) == 0
}
func present(value string, options presentation.Options) string {
	return presentation.SanitizeLabel(presentation.PreprocessWithOptions(value, options).Text)
}
func presentNullable(value string, options presentation.Options) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	result := present(value, options)
	return &result
}
func presentTimestamp(milliseconds int64) *MillisTime {
	if milliseconds <= 0 {
		return nil
	}
	stamp := MillisTime{Time: time.UnixMilli(milliseconds).UTC()}
	if !validMillisPointer(&stamp) {
		return nil
	}
	return &stamp
}
func normalizedLastPostAt(milliseconds int64) int64 {
	if presentTimestamp(milliseconds) == nil {
		return 0
	}
	return milliseconds
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
func cloneBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
func cloneMillis(value *MillisTime) *MillisTime {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
func cloneIdentity(value Identity) Identity {
	value.DisplayName = cloneString(value.DisplayName)
	value.Nickname = cloneString(value.Nickname)
	value.Roles = cloneSlice(value.Roles)
	return value
}
func cloneWhoAmI(value *WhoAmIEnvelope) *WhoAmIEnvelope {
	copy := WhoAmIEnvelope{Schema: value.Schema, Data: cloneIdentity(value.Data)}
	return &copy
}
func withoutWhoProof(value WhoAmIEnvelope) WhoAmIEnvelope { value.proof = nil; return value }
func cloneTeamsEnvelope(value *TeamsEnvelope) *TeamsEnvelope {
	copy := TeamsEnvelope{Schema: value.Schema, Teams: cloneSlice(value.Teams)}
	for i := range copy.Teams {
		copy.Teams[i].DisplayName = cloneString(copy.Teams[i].DisplayName)
	}
	return &copy
}
func withoutTeamsProof(value TeamsEnvelope) TeamsEnvelope { value.proof = nil; return value }
func cloneUsersEnvelope(value *UsersEnvelope) *UsersEnvelope {
	copy := UsersEnvelope{Schema: value.Schema, Users: cloneSlice(value.Users), Retrieval: value.Retrieval}
	for i := range copy.Users {
		copy.Users[i].DisplayName = cloneString(copy.Users[i].DisplayName)
		copy.Users[i].Nickname = cloneString(copy.Users[i].Nickname)
	}
	copy.Retrieval.Query = cloneString(copy.Retrieval.Query)
	copy.Retrieval.TeamID = cloneString(copy.Retrieval.TeamID)
	copy.Retrieval.Truncated = cloneBool(copy.Retrieval.Truncated)
	return &copy
}
func withoutUsersProof(value UsersEnvelope) UsersEnvelope { value.proof = nil; return value }
func cloneChannelsEnvelope(value *ChannelsEnvelope) *ChannelsEnvelope {
	copy := ChannelsEnvelope{Schema: value.Schema, Channels: cloneSlice(value.Channels)}
	for i := range copy.Channels {
		copy.Channels[i].DisplayName = cloneString(copy.Channels[i].DisplayName)
		copy.Channels[i].LastPost = cloneMillis(copy.Channels[i].LastPost)
		if copy.Channels[i].Team != nil {
			team := *copy.Channels[i].Team
			team.DisplayName = cloneString(team.DisplayName)
			copy.Channels[i].Team = &team
		}
	}
	return &copy
}
func withoutChannelsProof(value ChannelsEnvelope) ChannelsEnvelope { value.proof = nil; return value }
