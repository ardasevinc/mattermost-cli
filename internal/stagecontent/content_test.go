package stagecontent

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ardasevinc/mattermost-cli/internal/messageinput"
)

func TestAcquireExplicitMessagePreservesBytes(t *testing.T) {
	want := "first\nsecond\n"
	run := false
	got, err := Acquire(context.Background(), Request{Stdin: strings.NewReader("ignored tty"), Message: want, MessageSet: true}, Runtime{
		IsTTY:     func(io.Reader) bool { return true },
		RunEditor: func(context.Context, EditorInvocation) error { run = true; return nil },
	})
	if err != nil || string(got) != want || run {
		t.Fatalf("content=%q editorRun=%v err=%v", got, run, err)
	}
}

func TestAcquirePipedStdinPreservesFinalNewline(t *testing.T) {
	want := []byte("markdown **exactly**\n")
	got, err := Acquire(context.Background(), Request{Stdin: bytes.NewReader(want)}, Runtime{
		IsTTY: func(io.Reader) bool { return false },
	})
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("content=%q err=%v", got, err)
	}
}

func TestAcquireRejectsConflictingMessageAndPipe(t *testing.T) {
	got, err := Acquire(context.Background(), Request{Stdin: strings.NewReader("pipe"), Message: "flag", MessageSet: true}, Runtime{
		IsTTY: func(io.Reader) bool { return false },
	})
	if got != nil || !errors.Is(err, ErrConflictingSources) {
		t.Fatalf("content=%q err=%v", got, err)
	}
}

func TestAcquireMachineTTYRequiresExplicitContent(t *testing.T) {
	lookedUp, ran := false, false
	got, err := Acquire(context.Background(), Request{Stdin: strings.NewReader("tty"), Machine: true}, Runtime{
		IsTTY:     func(io.Reader) bool { return true },
		LookupEnv: func(string) (string, bool) { lookedUp = true; return "editor", true },
		RunEditor: func(context.Context, EditorInvocation) error { ran = true; return nil },
	})
	if got != nil || !errors.Is(err, ErrContentRequired) || lookedUp || ran {
		t.Fatalf("content=%q lookup=%v run=%v err=%v", got, lookedUp, ran, err)
	}
}

func TestAcquireMachineAcceptsPipeAndMessage(t *testing.T) {
	for name, request := range map[string]Request{
		"pipe":    {Stdin: strings.NewReader("pipe"), Machine: true},
		"message": {Stdin: strings.NewReader("tty"), Message: "flag", MessageSet: true, Machine: true},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := Acquire(context.Background(), request, Runtime{IsTTY: func(io.Reader) bool { return name == "message" }})
			if err != nil || string(got) != nameValue(name) {
				t.Fatalf("content=%q err=%v", got, err)
			}
		})
	}
}

func nameValue(name string) string {
	if name == "message" {
		return "flag"
	}
	return "pipe"
}

func TestAcquireEditorVisualPrecedenceAndSafeArgv(t *testing.T) {
	environment := map[string]string{"VISUAL": `code --wait --reuse-window "two words"`, "EDITOR": "fallback"}
	var invocation EditorInvocation
	got, err := Acquire(context.Background(), Request{Stdin: strings.NewReader("tty")}, Runtime{
		IsTTY:     func(io.Reader) bool { return true },
		LookupEnv: func(name string) (string, bool) { value, ok := environment[name]; return value, ok },
		RunEditor: func(_ context.Context, value EditorInvocation) error {
			invocation = value
			return os.WriteFile(value.Path, []byte("edited\n"), 0o600)
		},
	})
	if err != nil || string(got) != "edited\n" {
		t.Fatalf("content=%q err=%v", got, err)
	}
	if invocation.Command != "code" || !reflect.DeepEqual(invocation.Args, []string{"--wait", "--reuse-window", "two words"}) {
		t.Fatalf("invocation=%+v", invocation)
	}
}

func TestAcquireEditorFallsBackToEditor(t *testing.T) {
	var command string
	_, err := Acquire(context.Background(), Request{Stdin: strings.NewReader("tty")}, Runtime{
		IsTTY: func(io.Reader) bool { return true },
		LookupEnv: func(name string) (string, bool) {
			if name == "VISUAL" {
				return "  ", true
			}
			return `vim -f`, true
		},
		RunEditor: func(_ context.Context, value EditorInvocation) error {
			command = value.Command
			return os.WriteFile(value.Path, []byte("ok"), 0o600)
		},
	})
	if err != nil || command != "vim" {
		t.Fatalf("command=%q err=%v", command, err)
	}
}

func TestAcquireEditorPrivatePermissionsAndCleanup(t *testing.T) {
	var path, directory string
	_, err := Acquire(context.Background(), Request{Stdin: strings.NewReader("tty")}, Runtime{
		IsTTY:     func(io.Reader) bool { return true },
		LookupEnv: func(string) (string, bool) { return "editor", true },
		RunEditor: func(_ context.Context, value EditorInvocation) error {
			path, directory = value.Path, filepath.Dir(value.Path)
			dirInfo, statErr := os.Stat(directory)
			if statErr != nil {
				return statErr
			}
			fileInfo, statErr := os.Stat(path)
			if statErr != nil {
				return statErr
			}
			if dirInfo.Mode().Perm() != 0o700 || fileInfo.Mode().Perm() != 0o600 {
				return errors.New("insecure permissions")
			}
			return os.WriteFile(path, []byte("ok"), 0o600)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary file remains: %v", err)
	}
	if _, err := os.Stat(directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary directory remains: %v", err)
	}
}

func TestAcquireEditorFailureIsNarrowAndCleansUp(t *testing.T) {
	physical := errors.New("secret command and path detail")
	var path string
	got, err := Acquire(context.Background(), Request{Stdin: strings.NewReader("tty")}, Runtime{
		IsTTY:     func(io.Reader) bool { return true },
		LookupEnv: func(string) (string, bool) { return "editor", true },
		RunEditor: func(_ context.Context, value EditorInvocation) error { path = value.Path; return physical },
	})
	if got != nil || !errors.Is(err, ErrEditorFailed) || errors.Is(err, physical) || strings.Contains(err.Error(), physical.Error()) || strings.Contains(err.Error(), path) {
		t.Fatalf("content=%q err=%v", got, err)
	}
	if _, statErr := os.Stat(filepath.Dir(path)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("temporary directory remains: %v", statErr)
	}
}

func TestAcquireAllSourcesUseMessageValidation(t *testing.T) {
	for name, run := range map[string]func() ([]byte, error){
		"empty message": func() ([]byte, error) {
			return Acquire(context.Background(), Request{MessageSet: true}, Runtime{})
		},
		"invalid stdin": func() ([]byte, error) {
			return Acquire(context.Background(), Request{Stdin: bytes.NewReader([]byte{0xc3, 0x28})}, Runtime{IsTTY: func(io.Reader) bool { return false }})
		},
		"empty editor": func() ([]byte, error) {
			return editorResult(nil)
		},
		"invalid editor": func() ([]byte, error) {
			return editorResult([]byte{0xc3, 0x28})
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := run()
			want := messageinput.ErrEmpty
			if strings.Contains(name, "invalid") {
				want = messageinput.ErrInvalidUTF8
			}
			if got != nil || !errors.Is(err, want) {
				t.Fatalf("content=%q err=%v want=%v", got, err, want)
			}
		})
	}
}

func editorResult(content []byte) ([]byte, error) {
	return Acquire(context.Background(), Request{Stdin: strings.NewReader("tty")}, Runtime{
		IsTTY:     func(io.Reader) bool { return true },
		LookupEnv: func(string) (string, bool) { return "editor", true },
		RunEditor: func(_ context.Context, value EditorInvocation) error { return os.WriteFile(value.Path, content, 0o600) },
	})
}

func TestAcquireEditorConfigurationErrors(t *testing.T) {
	base := Request{Stdin: strings.NewReader("tty")}
	tty := func(io.Reader) bool { return true }
	if _, err := Acquire(context.Background(), base, Runtime{IsTTY: tty, LookupEnv: func(string) (string, bool) { return "", false }}); !errors.Is(err, ErrEditorNotConfigured) {
		t.Fatalf("missing editor error=%v", err)
	}
	for _, value := range []string{`editor "unterminated`, "editor\nother", `editor trailing\`} {
		if _, err := Acquire(context.Background(), base, Runtime{IsTTY: tty, LookupEnv: func(string) (string, bool) { return value, true }}); !errors.Is(err, ErrInvalidEditor) {
			t.Fatalf("command=%q error=%v", value, err)
		}
	}
}

func TestSplitCommandDoesNotInterpretShellSyntax(t *testing.T) {
	got, err := splitCommand(`editor ';' '$(touch nope)' "" escaped\ value`)
	want := []string{"editor", ";", "$(touch nope)", "", "escaped value"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("argv=%q err=%v", got, err)
	}
}
