// Package cursor encodes and decodes opaque channel-history cursors.
package cursor

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math"
	"regexp"
	"unicode/utf8"
)

const (
	maxEncodedLength = 2048
	maxDecodedLength = 1536
	maxDateMillis    = int64(8_640_000_000_000_000)
	maxIDLength      = 128
)

var (
	// ErrInvalidCursor is returned for every invalid cursor without exposing its input.
	ErrInvalidCursor = errors.New("invalid cursor")
	safeIDPattern    = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
)

// ChannelHistory identifies a deterministic boundary in one channel's history.
type ChannelHistory struct {
	Version          int      `json:"v"`
	Scope            string   `json:"scope"`
	ChannelID        string   `json:"channelId"`
	Boundary         Boundary `json:"boundary"`
	Since            *int64   `json:"since"`
	SafeBeforePostID string   `json:"safeBeforePostId,omitempty"`
}

// Boundary is the newest post included in a channel-history page.
type Boundary struct {
	CreateAt int64  `json:"createAt"`
	ID       string `json:"id"`
}

// EncodeChannelHistory returns canonical, unpadded base64url JSON.
func EncodeChannelHistory(value ChannelHistory) (string, error) {
	if !valid(value) {
		return "", ErrInvalidCursor
	}

	data, err := json.Marshal(value)
	if err != nil {
		return "", ErrInvalidCursor
	}
	encoded := base64.RawURLEncoding.EncodeToString(data)
	if len(encoded) > maxEncodedLength {
		return "", ErrInvalidCursor
	}
	return encoded, nil
}

// DecodeChannelHistory validates and decodes an opaque channel-history cursor.
func DecodeChannelHistory(encoded string) (ChannelHistory, error) {
	if len(encoded) == 0 || len(encoded) > maxEncodedLength || !safeIDPattern.MatchString(encoded) {
		return ChannelHistory{}, ErrInvalidCursor
	}

	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(data) == 0 || len(data) > maxDecodedLength || !utf8.Valid(data) ||
		base64.RawURLEncoding.EncodeToString(data) != encoded {
		return ChannelHistory{}, ErrInvalidCursor
	}

	value, ok := decodeJSON(data)
	if !ok || !valid(value) {
		return ChannelHistory{}, ErrInvalidCursor
	}
	return value, nil
}

// ComparePostIDs applies the bytewise ASCII ordering used for equal timestamps.
func ComparePostIDs(a, b string) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

func valid(value ChannelHistory) bool {
	return value.Version == 1 && value.Scope == "channel" &&
		isSafeID(value.ChannelID) && isSafeID(value.Boundary.ID) &&
		value.Boundary.CreateAt > 0 && value.Boundary.CreateAt <= maxDateMillis &&
		(value.Since == nil || (*value.Since >= 0 && *value.Since <= value.Boundary.CreateAt)) &&
		(value.SafeBeforePostID == "" || isSafeID(value.SafeBeforePostID))
}

func isSafeID(value string) bool {
	return len(value) >= 1 && len(value) <= maxIDLength && safeIDPattern.MatchString(value)
}

func decodeJSON(data []byte) (ChannelHistory, bool) {
	var outer map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&outer); err != nil || !onlyKeys(outer,
		[]string{"v", "scope", "channelId", "boundary", "since"}, []string{"safeBeforePostId"}) {
		return ChannelHistory{}, false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ChannelHistory{}, false
	}

	var boundary map[string]json.RawMessage
	if err := json.Unmarshal(outer["boundary"], &boundary); err != nil ||
		!onlyKeys(boundary, []string{"createAt", "id"}, nil) {
		return ChannelHistory{}, false
	}

	version, ok := integer(outer["v"])
	if !ok || version != 1 {
		return ChannelHistory{}, false
	}
	createAt, ok := integer(boundary["createAt"])
	if !ok {
		return ChannelHistory{}, false
	}

	value := ChannelHistory{Version: int(version), Boundary: Boundary{CreateAt: createAt}}
	if json.Unmarshal(outer["scope"], &value.Scope) != nil ||
		json.Unmarshal(outer["channelId"], &value.ChannelID) != nil ||
		json.Unmarshal(boundary["id"], &value.Boundary.ID) != nil {
		return ChannelHistory{}, false
	}
	if !bytes.Equal(outer["since"], []byte("null")) {
		since, valid := integer(outer["since"])
		if !valid {
			return ChannelHistory{}, false
		}
		value.Since = &since
	}
	if raw, exists := outer["safeBeforePostId"]; exists {
		if json.Unmarshal(raw, &value.SafeBeforePostID) != nil || value.SafeBeforePostID == "" {
			return ChannelHistory{}, false
		}
	}
	return value, true
}

func integer(raw json.RawMessage) (int64, bool) {
	var number float64
	if json.Unmarshal(raw, &number) != nil || math.IsNaN(number) || math.IsInf(number, 0) ||
		math.Trunc(number) != number || number < -9_007_199_254_740_991 || number > 9_007_199_254_740_991 {
		return 0, false
	}
	return int64(number), true
}

func onlyKeys(value map[string]json.RawMessage, required, optional []string) bool {
	if value == nil || len(value) < len(required) || len(value) > len(required)+len(optional) {
		return false
	}
	allowed := make(map[string]struct{}, len(required)+len(optional))
	for _, key := range append(required, optional...) {
		allowed[key] = struct{}{}
	}
	for _, key := range required {
		if _, exists := value[key]; !exists {
			return false
		}
	}
	for key := range value {
		if _, exists := allowed[key]; !exists {
			return false
		}
	}
	return true
}
