package messageinput

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestReadPreservesExactMarkdown(t *testing.T) {
	short := []byte("# release notes\n\n- **bold** `code`\n- [link](https://example.com/?a=1&b=2)\n\n> final 🌍\n")
	long := []byte(strings.Repeat("## section 🌍\n\n- **bold** `code`\n\n", 470))
	if utf8.RuneCount(long) < 15_500 || utf8.RuneCount(long) >= MaxCharacters || len(long) >= MaxBytes {
		t.Fatalf("invalid long fixture: bytes=%d characters=%d", len(long), utf8.RuneCount(long))
	}
	for name, value := range map[string][]byte{"short": short, "long": long} {
		t.Run(name, func(t *testing.T) {
			chunks := io.MultiReader(
				bytes.NewReader(value[:min(17, len(value))]),
				bytes.NewReader(value[min(17, len(value)):min(4099, len(value))]),
				bytes.NewReader(value[min(4099, len(value)):]),
			)
			got, err := Read(chunks)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, value) {
				t.Fatal("message bytes changed")
			}
		})
	}
}

func TestValidateMessageBounds(t *testing.T) {
	if err := Validate([]byte(strings.Repeat("a", MaxCharacters))); err != nil {
		t.Fatal(err)
	}
	for name, test := range map[string]struct {
		value []byte
		want  error
	}{
		"empty":                 {nil, ErrEmpty},
		"ascii whitespace":      {[]byte(" \t\r\n\v\f"), ErrEmpty},
		"ecmascript whitespace": {[]byte("\u00a0\u1680\u2028\u2029\ufeff"), ErrEmpty},
		"invalid utf8":          {[]byte{0xc3, 0x28}, ErrInvalidUTF8},
		"characters":            {[]byte(strings.Repeat("a", MaxCharacters+1)), ErrTooManyRunes},
		"bytes":                 {[]byte(strings.Repeat("x", MaxBytes+1)), ErrTooManyBytes},
	} {
		t.Run(name, func(t *testing.T) {
			if err := Validate(test.value); !errors.Is(err, test.want) {
				t.Fatalf("error=%v want=%v", err, test.want)
			}
		})
	}
}

func TestReadFailsAtStreamingByteBoundary(t *testing.T) {
	input := io.MultiReader(bytes.NewReader(bytes.Repeat([]byte{'x'}, MaxBytes)), strings.NewReader("x"))
	if _, err := Read(input); !errors.Is(err, ErrTooManyBytes) {
		t.Fatalf("error=%v", err)
	}
}

func TestReadHidesPhysicalInputFailure(t *testing.T) {
	physical := errors.New("hostile reader detail")
	if _, err := Read(failingReader{err: physical}); !errors.Is(err, ErrRead) || errors.Is(err, physical) || strings.Contains(err.Error(), physical.Error()) {
		t.Fatalf("error=%v", err)
	}
}

type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }
