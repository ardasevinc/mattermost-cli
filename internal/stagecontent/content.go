// Package stagecontent acquires human stage message content without depending
// on CLI flag wiring. Dry-run paths can therefore skip acquisition entirely.
package stagecontent

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ardasevinc/mattermost-cli/internal/messageinput"
)

var (
	ErrConflictingSources  = errors.New("stage content: multiple content sources")
	ErrContentRequired     = errors.New("stage content: content source required")
	ErrEditorNotConfigured = errors.New("stage content: editor not configured")
	ErrInvalidEditor       = errors.New("stage content: invalid editor command")
	ErrEditorFailed        = errors.New("stage content: editor failed")
	ErrEditorOutput        = errors.New("stage content: could not read editor output")
)

// Request describes the content-related facts already resolved by a caller.
// MessageSet distinguishes an explicit empty --message value from an absent
// flag. Machine mode may use an explicit message or piped stdin, but never an
// editor.
type Request struct {
	Stdin      io.Reader
	Message    string
	MessageSet bool
	Machine    bool
	Initial    []byte
}

// EditorInvocation is an argv-safe editor execution request. Command and Args
// are already parsed; implementations must execute them directly, never via a
// shell. Path names the private 0600 file to edit.
type EditorInvocation struct {
	Command string
	Args    []string
	Path    string
}

// Runtime holds injectable host behavior for deterministic tests. Nil
// functions select the production implementations.
type Runtime struct {
	IsTTY     func(io.Reader) bool
	LookupEnv func(string) (string, bool)
	RunEditor func(context.Context, EditorInvocation) error
}

// Acquire selects exactly one source. Non-TTY stdin is an explicit source and
// conflicts with --message. With TTY stdin, only human mode may fall back to
// VISUAL and then EDITOR. Returned bytes preserve the selected source exactly.
func Acquire(ctx context.Context, request Request, runtime Runtime) ([]byte, error) {
	if ctx == nil {
		return nil, ErrContentRequired
	}
	isTTY := runtime.IsTTY
	if isTTY == nil {
		isTTY = defaultIsTTY
	}
	piped := request.Stdin != nil && !isTTY(request.Stdin)
	if request.MessageSet && piped {
		return nil, ErrConflictingSources
	}
	if request.MessageSet {
		return messageinput.Read(strings.NewReader(request.Message))
	}
	if piped {
		return messageinput.Read(request.Stdin)
	}
	if request.Machine {
		return nil, ErrContentRequired
	}
	return acquireEditor(ctx, runtime, request.Initial)
}

func acquireEditor(ctx context.Context, runtime Runtime, initial []byte) ([]byte, error) {
	lookup := runtime.LookupEnv
	if lookup == nil {
		lookup = os.LookupEnv
	}
	command := ""
	for _, name := range []string{"VISUAL", "EDITOR"} {
		if value, ok := lookup(name); ok && strings.TrimSpace(value) != "" {
			command = value
			break
		}
	}
	if command == "" {
		return nil, ErrEditorNotConfigured
	}
	argv, err := splitCommand(command)
	if err != nil || len(argv) == 0 || argv[0] == "" {
		return nil, ErrInvalidEditor
	}

	directory, err := os.MkdirTemp("", "mm-stage-content-")
	if err != nil {
		return nil, ErrEditorOutput
	}
	defer os.RemoveAll(directory)
	if err := os.Chmod(directory, 0o700); err != nil {
		return nil, ErrEditorOutput
	}
	path := filepath.Join(directory, "message")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, ErrEditorOutput
	}
	if len(initial) != 0 {
		validated, validateErr := messageinput.Read(bytes.NewReader(initial))
		if validateErr != nil || !bytes.Equal(validated, initial) {
			_ = file.Close()
			return nil, ErrEditorOutput
		}
		if _, err = file.Write(validated); err != nil {
			_ = file.Close()
			return nil, ErrEditorOutput
		}
	}
	if err := file.Close(); err != nil {
		return nil, ErrEditorOutput
	}

	run := runtime.RunEditor
	if run == nil {
		run = defaultRunEditor
	}
	invocation := EditorInvocation{Command: argv[0], Args: append([]string(nil), argv[1:]...), Path: path}
	if err := run(ctx, invocation); err != nil {
		return nil, ErrEditorFailed
	}
	file, err = os.Open(path)
	if err != nil {
		return nil, ErrEditorOutput
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, ErrEditorOutput
	}
	return messageinput.Read(file)
}

func defaultIsTTY(input io.Reader) bool {
	file, ok := input.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func defaultRunEditor(ctx context.Context, invocation EditorInvocation) error {
	args := append(append([]string(nil), invocation.Args...), invocation.Path)
	command := exec.CommandContext(ctx, invocation.Command, args...)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	return command.Run()
}

// splitCommand accepts the quoting needed by common editor settings while
// deliberately omitting shell expansion, substitution, operators, and globbing.
func splitCommand(value string) ([]string, error) {
	var result []string
	var current strings.Builder
	inSingle, inDouble, escaped, started := false, false, false, false
	flush := func() {
		if started {
			result = append(result, current.String())
			current.Reset()
			started = false
		}
	}
	for _, r := range value {
		if r == 0 || r == '\n' || r == '\r' {
			return nil, ErrInvalidEditor
		}
		if escaped {
			current.WriteRune(r)
			started, escaped = true, false
			continue
		}
		switch {
		case inSingle:
			if r == '\'' {
				inSingle = false
			} else {
				current.WriteRune(r)
			}
		case inDouble:
			switch r {
			case '"':
				inDouble = false
			case '\\':
				escaped = true
			default:
				current.WriteRune(r)
			}
		case r == '\\':
			escaped, started = true, true
		case r == '\'':
			inSingle, started = true, true
		case r == '"':
			inDouble, started = true, true
		case r == ' ' || r == '\t':
			flush()
		default:
			current.WriteRune(r)
			started = true
		}
	}
	if escaped || inSingle || inDouble {
		return nil, ErrInvalidEditor
	}
	flush()
	return result, nil
}
