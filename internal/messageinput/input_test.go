package messageinput

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestReadPreservesExactMarkdown(t *testing.T) {
	short := []byte(shortMarkdown)
	long := []byte(longMarkdown())
	if got := fmt.Sprintf("%x", sha256.Sum256(short)); got != "f723745ae7f1c3c716d20ea724a383fc9777a34ffc73ffd1d5999977bd8fadf9" {
		t.Fatalf("short fixture drifted: %s", got)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(long)); got != "b81aca78af1a5273db3da59748a1d06f6bcbaac885badf56fb7edf9fa809aa30" {
		t.Fatalf("long fixture drifted: %s", got)
	}
	if utf8.RuneCount(long) != 15_666 || len(long) != 15_841 {
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

const shortMarkdown = `# Release ready ✅

**Status:** shipped with [runbook](https://example.test/runbook).

- [x] API healthy
- [x] migrations applied

> Verify the canary before broad rollout.

` + "```ts\nconst ready = true\n```\n"

func longMarkdown() string {
	message := "# Extended deployment report 🌍\n\nThis intentionally large fixture exercises structured Markdown without relying on generated prose.\n"
	for section := 1; utf8.RuneCountInString(message) < 15_500; section++ {
		message += fmt.Sprintf(`
## Service %d

| Check | Result | Detail |
| --- | --- | --- |
| health | ✅ | [probe](https://example.test/health/%d) |
| queue | ✅ | **drained** |

- [x] deploy completed
- [x] metrics reviewed
- [ ] observe for 30 minutes

> Service %d remained inside its latency budget.

`+"```json\n"+`{"service":%d,"status":"healthy","regions":["eu","us"],"rollback":false}
`+"```\n", section, section, section, section)
	}
	return message + "\n---\n\nEnd of report. _Keep this final newline._\n"
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
		"oversized whitespace":  {[]byte(strings.Repeat(" ", MaxCharacters+1)), ErrEmpty},
	} {
		t.Run(name, func(t *testing.T) {
			if err := Validate(test.value); !errors.Is(err, test.want) {
				t.Fatalf("error=%v want=%v", err, test.want)
			}
		})
	}
}

func TestUnicodeBoundariesAndNegativeWhitespace(t *testing.T) {
	maximum := []byte(strings.Repeat("🌍", MaxCharacters))
	if len(maximum) != 65_532 {
		t.Fatalf("maximum non-BMP fixture bytes=%d", len(maximum))
	}
	if err := Validate(maximum); err != nil {
		t.Fatal(err)
	}
	if err := Validate([]byte(strings.Repeat("🌍", MaxCharacters+1))); !errors.Is(err, ErrTooManyBytes) {
		t.Fatalf("overflow error=%v", err)
	}
	for _, value := range []string{"\u0085", "\u180e", "\u200b"} {
		if err := Validate([]byte(value)); err != nil {
			t.Fatalf("non-ECMAScript whitespace %U rejected: %v", []rune(value)[0], err)
		}
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

func TestReadPrefersConfirmedOverflowToSameReadFailure(t *testing.T) {
	physical := errors.New("late read failure")
	reader := dataErrorReader{data: bytes.Repeat([]byte{'x'}, MaxBytes+1), err: physical}
	if _, err := Read(&reader); !errors.Is(err, ErrTooManyBytes) || errors.Is(err, physical) {
		t.Fatalf("error=%v", err)
	}
}

func FuzzValidate(f *testing.F) {
	for _, seed := range [][]byte{nil, []byte("hello 🌍\n"), []byte{0xc3, 0x28}, bytes.Repeat([]byte{'x'}, MaxBytes+1)} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, value []byte) {
		err := Validate(value)
		if err == nil && (len(value) > MaxBytes || !utf8.Valid(value) || utf8.RuneCount(value) > MaxCharacters || whitespaceOnly(value)) {
			t.Fatalf("accepted invalid input: bytes=%d", len(value))
		}
	})
}

type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

type dataErrorReader struct {
	data []byte
	err  error
}

func (r *dataErrorReader) Read(destination []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, r.err
	}
	n := copy(destination, r.data)
	r.data = r.data[n:]
	if len(r.data) == 0 {
		return n, r.err
	}
	return n, nil
}
