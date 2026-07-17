// Package stagecursor encodes and decodes opaque stage-list cursors.
package stagecursor

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"time"
	"unicode/utf8"
)

const (
	maxEncodedLength = 1024
	maxDecodedLength = 768
	maxStageIDLength = 128
)

var (
	// ErrInvalidCursor is returned for every invalid cursor without reflecting
	// attacker-controlled input.
	ErrInvalidCursor = errors.New("invalid stage cursor")
	safeStageID      = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
)

// Boundary is the final stage in a deterministic stage-list page.
type Boundary struct {
	UpdatedAt time.Time
	StageID   string
}

type wireCursor struct {
	Version  int          `json:"v"`
	Scope    string       `json:"scope"`
	Boundary wireBoundary `json:"boundary"`
}

type wireBoundary struct {
	UpdatedAt string `json:"updatedAt"`
	StageID   string `json:"stageId"`
}

// Encode returns canonical, unpadded base64url JSON for boundary.
func Encode(boundary Boundary) (string, error) {
	if !validBoundary(boundary) {
		return "", ErrInvalidCursor
	}
	wire := wireCursor{1, "stages", wireBoundary{
		UpdatedAt: boundary.UpdatedAt.Format(time.RFC3339Nano),
		StageID:   boundary.StageID,
	}}
	data, err := json.Marshal(wire)
	if err != nil || len(data) == 0 || len(data) > maxDecodedLength {
		return "", ErrInvalidCursor
	}
	encoded := base64.RawURLEncoding.EncodeToString(data)
	if len(encoded) > maxEncodedLength {
		return "", ErrInvalidCursor
	}
	return encoded, nil
}

// Decode validates an opaque cursor and returns its stage-list boundary.
func Decode(encoded string) (Boundary, error) {
	if len(encoded) == 0 || len(encoded) > maxEncodedLength || !safeStageID.MatchString(encoded) {
		return Boundary{}, ErrInvalidCursor
	}
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(data) == 0 || len(data) > maxDecodedLength || !utf8.Valid(data) ||
		base64.RawURLEncoding.EncodeToString(data) != encoded {
		return Boundary{}, ErrInvalidCursor
	}

	wire, ok := decodeWire(data)
	if !ok || wire.Version != 1 || wire.Scope != "stages" || !validStageID(wire.Boundary.StageID) {
		return Boundary{}, ErrInvalidCursor
	}
	updatedAt, err := time.Parse(time.RFC3339Nano, wire.Boundary.UpdatedAt)
	if err != nil || updatedAt.IsZero() || wire.Boundary.UpdatedAt != updatedAt.UTC().Format(time.RFC3339Nano) {
		return Boundary{}, ErrInvalidCursor
	}
	boundary := Boundary{UpdatedAt: updatedAt, StageID: wire.Boundary.StageID}
	canonical, err := Encode(boundary)
	if err != nil || canonical != encoded {
		return Boundary{}, ErrInvalidCursor
	}
	return boundary, nil
}

func validBoundary(boundary Boundary) bool {
	if boundary.UpdatedAt.IsZero() || !validStageID(boundary.StageID) {
		return false
	}
	_, offset := boundary.UpdatedAt.Zone()
	return offset == 0 && boundary.UpdatedAt.Format(time.RFC3339Nano) == boundary.UpdatedAt.UTC().Format(time.RFC3339Nano)
}

func validStageID(id string) bool {
	return len(id) >= 1 && len(id) <= maxStageIDLength && safeStageID.MatchString(id)
}

func decodeWire(data []byte) (wireCursor, bool) {
	var outer map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(data))
	if decoder.Decode(&outer) != nil || !onlyKeys(outer, "v", "scope", "boundary") {
		return wireCursor{}, false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return wireCursor{}, false
	}
	var inner map[string]json.RawMessage
	if json.Unmarshal(outer["boundary"], &inner) != nil || !onlyKeys(inner, "updatedAt", "stageId") {
		return wireCursor{}, false
	}
	var wire wireCursor
	typed := json.NewDecoder(bytes.NewReader(data))
	typed.DisallowUnknownFields()
	if typed.Decode(&wire) != nil {
		return wireCursor{}, false
	}
	if err := typed.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return wireCursor{}, false
	}
	return wire, true
}

func onlyKeys(value map[string]json.RawMessage, keys ...string) bool {
	if value == nil || len(value) != len(keys) {
		return false
	}
	for _, key := range keys {
		if _, exists := value[key]; !exists {
			return false
		}
	}
	return true
}
