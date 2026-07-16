package messageinput

import (
	"errors"
	"io"
	"unicode"
	"unicode/utf8"
)

const (
	MaxBytes      = 65_535
	MaxCharacters = 16_383
)

var (
	ErrEmpty        = errors.New("message cannot be empty")
	ErrInvalidUTF8  = errors.New("message must be valid UTF-8")
	ErrTooManyBytes = errors.New("message exceeds 65535 UTF-8 bytes")
	ErrTooManyRunes = errors.New("message exceeds 16383 Unicode characters")
	ErrRead         = errors.New("could not read message input")
)

// Read consumes at most one byte beyond Mattermost's verified message limit.
// The returned bytes are an exact copy of the caller's valid UTF-8 input.
func Read(input io.Reader) ([]byte, error) {
	if input == nil {
		return nil, ErrRead
	}
	data, err := io.ReadAll(io.LimitReader(input, MaxBytes+1))
	if len(data) > MaxBytes {
		return nil, ErrTooManyBytes
	}
	if err != nil {
		return nil, ErrRead
	}
	if err := Validate(data); err != nil {
		return nil, err
	}
	return data, nil
}

func Validate(message []byte) error {
	if len(message) > MaxBytes {
		return ErrTooManyBytes
	}
	if !utf8.Valid(message) {
		return ErrInvalidUTF8
	}
	if whitespaceOnly(message) {
		return ErrEmpty
	}
	if utf8.RuneCount(message) > MaxCharacters {
		return ErrTooManyRunes
	}
	return nil
}

func whitespaceOnly(message []byte) bool {
	if len(message) == 0 {
		return true
	}
	for _, character := range string(message) {
		if !ecmaScriptWhitespace(character) {
			return false
		}
	}
	return true
}

func ecmaScriptWhitespace(character rune) bool {
	switch character {
	case '\t', '\n', '\v', '\f', '\r', ' ', '\u00a0', '\u2028', '\u2029', '\ufeff':
		return true
	default:
		return unicode.Is(unicode.Zs, character)
	}
}
