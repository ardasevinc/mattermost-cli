//go:build darwin || linux

package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	mmSchema "github.com/ardasevinc/mattermost-cli/internal/schema"
	"github.com/ardasevinc/mattermost-cli/internal/stagestore"
)

func stageInspectCommand(t *testing.T, home, stateRoot string, jsonOutput bool, stdout, stderr *bytes.Buffer) (*rootState, *cobraCommandShim) {
	t.Helper()
	state := &rootState{streams: streams{in: strings.NewReader(""), out: stdout, err: stderr}, deps: defaultDependencies(stdout)}
	state.deps.homeDir = func() (string, error) { return home, nil }
	state.deps.lookupEnv = func(key string) (string, bool) {
		if key == "XDG_STATE_HOME" {
			return stateRoot, true
		}
		return "", false
	}
	state.flags.json = jsonOutput
	command := newStageCommand(state)
	command.SilenceUsage = true
	command.SilenceErrors = true
	command.SetOut(stdout)
	command.SetErr(stderr)
	return state, &cobraCommandShim{command}
}

// The shim keeps test call sites small without changing production command APIs.
type cobraCommandShim struct {
	command interface {
		SetArgs([]string)
		ExecuteContext(context.Context) error
	}
}

func (s *cobraCommandShim) execute(ctx context.Context, args ...string) error {
	s.command.SetArgs(args)
	return s.command.ExecuteContext(ctx)
}

func TestStageListAbsentIsOfflineReadOnlyAndSchemaValid(t *testing.T) {
	home, stateRoot := t.TempDir(), filepath.Join(t.TempDir(), "state")
	var stdout, stderr bytes.Buffer
	_, command := stageInspectCommand(t, home, stateRoot, true, &stdout, &stderr)
	if err := command.execute(t.Context(), "list"); err != nil {
		t.Fatal(err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr=%q", stderr.String())
	}
	registry, err := mmSchema.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Validate("mm/v2/stages", bytes.NewReader(stdout.Bytes())); err != nil {
		t.Fatalf("schema: %v\n%s", err, stdout.String())
	}
	if stdout.String() != "{\"schema\":\"mm/v2/stages\",\"stages\":[],\"nextCursor\":null}\n" {
		t.Fatalf("stdout=%q", stdout.String())
	}
	if _, err := os.Stat(stateRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stage list created state: %v", err)
	}
}

func TestStageCommandsRejectConfigWritableByOtherUsers(t *testing.T) {
	home, stateRoot := t.TempDir(), filepath.Join(t.TempDir(), "state")
	path := filepath.Join(home, ".config", "mattermost-cli", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("stage_ttl_seconds = 1\n"), 0o620); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o620); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	_, command := stageInspectCommand(t, home, stateRoot, false, &stdout, &stderr)
	err := command.execute(t.Context(), "list")
	if err == nil || exitCode(err) != 3 || !strings.Contains(err.Error(), "must not be writable by other users") || stdout.Len() != 0 {
		t.Fatalf("err=%v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	if _, statErr := os.Stat(stateRoot); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("unsafe policy touched store: %v", statErr)
	}
}

func TestStageListValidatesBoundsAndCursorBeforeStoreAccess(t *testing.T) {
	for _, args := range [][]string{{"list", "--limit", "0"}, {"list", "--limit", "101"}, {"list", "--cursor="}, {"list", "--cursor", "not-a-cursor"}} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			stateRoot := filepath.Join(t.TempDir(), "absent")
			var stdout, stderr bytes.Buffer
			_, command := stageInspectCommand(t, t.TempDir(), stateRoot, false, &stdout, &stderr)
			err := command.execute(t.Context(), args...)
			if err == nil || exitCode(err) != 2 || stdout.Len() != 0 {
				t.Fatalf("err=%v stdout=%q", err, stdout.String())
			}
			if _, statErr := os.Stat(stateRoot); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("validation touched store: %v", statErr)
			}
		})
	}
}

func createInspectionStage(t *testing.T, home, stateRoot, body string) string {
	t.Helper()
	paths, err := stagestore.ResolvePaths(home, func(key string) (string, bool) { return stateRoot, key == "XDG_STATE_HOME" })
	if err != nil {
		t.Fatal(err)
	}
	store, err := stagestore.Open(t.Context(), paths.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	destination := json.RawMessage(`{"kind":"conversation","channelId":"channel-1","channelType":"public","teamId":"team-1","postId":null,"rootPostId":null,"participantIds":[],"emoji":null,"postState":null,"reactionPresent":null}`)
	plan := json.RawMessage(`{"steps":[{"ordinal":1,"type":"create_post","condition":"always"}]}`)
	result, err := store.Create(t.Context(), stagestore.CreateInput{
		RequestID: "inspection-stage", RequestDigest: sha256.Sum256([]byte("request")), Operation: stagestore.CreatePost,
		ServerURL: "https://mattermost.example/api/v4", UserID: "user-1",
		Content: stagestore.RevisionContent{Body: []byte(body), Destination: destination, Plan: plan},
	})
	if closeErr := store.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	return result.Stage.ID
}

func TestStageListOmitsContentAndShowExplicitlyRevealsProjectedContent(t *testing.T) {
	home, stateRoot := t.TempDir(), filepath.Join(t.TempDir(), "state")
	stageID := createInspectionStage(t, home, stateRoot, "review me")

	var listOut, listErr bytes.Buffer
	_, list := stageInspectCommand(t, home, stateRoot, true, &listOut, &listErr)
	if err := list.execute(t.Context(), "list", "--limit", "1"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(listOut.String(), "review me") || strings.Contains(listOut.String(), `"body"`) || strings.Contains(listOut.String(), `"path"`) {
		t.Fatalf("list leaked content: %s", listOut.String())
	}
	registry, _ := mmSchema.Load()
	if err := registry.Validate("mm/v2/stages", bytes.NewReader(listOut.Bytes())); err != nil {
		t.Fatalf("list schema: %v", err)
	}

	var showOut, showErr bytes.Buffer
	_, show := stageInspectCommand(t, home, stateRoot, true, &showOut, &showErr)
	if err := show.execute(t.Context(), "show", stageID); err != nil {
		t.Fatal(err)
	}
	if showErr.Len() != 0 || !strings.Contains(showOut.String(), `"body":"review me"`) {
		t.Fatalf("stdout=%q stderr=%q", showOut.String(), showErr.String())
	}
	if err := registry.Validate("mm/v2/stage", bytes.NewReader(showOut.Bytes())); err != nil {
		t.Fatalf("show schema: %v\n%s", err, showOut.String())
	}

	showOut.Reset()
	showErr.Reset()
	_, human := stageInspectCommand(t, home, stateRoot, false, &showOut, &showErr)
	if err := human.execute(t.Context(), "show", stageID); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(showErr.String(), "warning:") || !strings.Contains(showOut.String(), "body:\nreview me") || !strings.Contains(showOut.String(), "apply: mm apply "+stageID+"@1") {
		t.Fatalf("stdout=%q stderr=%q", showOut.String(), showErr.String())
	}
}

func TestStageShowHumanPreservesMarkdownLinesAndShowsOrderedPlan(t *testing.T) {
	home, stateRoot := t.TempDir(), filepath.Join(t.TempDir(), "state")
	stageID := createInspectionStage(t, home, stateRoot, "# heading\n\n- one\n- two")
	var stdout, stderr bytes.Buffer
	_, command := stageInspectCommand(t, home, stateRoot, false, &stdout, &stderr)
	if err := command.execute(t.Context(), "show", stageID); err != nil {
		t.Fatal(err)
	}
	want := "body:\n# heading\n\n- one\n- two\n"
	if !strings.Contains(stdout.String(), want) || !strings.Contains(stdout.String(), "plan:\n  1. create_post (always)\n") {
		t.Fatalf("stdout=%q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "apply: mm apply "+stageID+"@1") || !strings.HasPrefix(stderr.String(), "warning:") {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestStageShowAbsentIsLocalStateFailure(t *testing.T) {
	var stdout, stderr bytes.Buffer
	_, command := stageInspectCommand(t, t.TempDir(), filepath.Join(t.TempDir(), "absent"), true, &stdout, &stderr)
	err := command.execute(t.Context(), "show", "stg_0123456789abcdefghijklmnopqrstuv")
	var local localStateFailure
	if !errors.As(err, &local) || stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("err=%v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
}

func TestStageInspectionIsWiredThroughRootWithStableMachineErrors(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("MM_URL", "")
	t.Setenv("MM_TOKEN", "")

	var stdout, stderr bytes.Buffer
	if code := Execute(t.Context(), []string{"--json", "stage", "list"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("list exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if stdout.String() != "{\"schema\":\"mm/v2/stages\",\"stages\":[],\"nextCursor\":null}\n" || stderr.Len() != 0 {
		t.Fatalf("list stdout=%q stderr=%q", stdout.String(), stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	stageID := "stg_0123456789abcdefghijklmnopqrstuv"
	if code := Execute(t.Context(), []string{"--json", "stage", "show", stageID}, strings.NewReader(""), &stdout, &stderr); code != 6 {
		t.Fatalf("show exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), `"code":"state_conflict"`) || !strings.Contains(stderr.String(), `"exitCode":6`) {
		t.Fatalf("show stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestConcurrentStageListReadOnly(t *testing.T) {
	home, stateRoot := t.TempDir(), filepath.Join(t.TempDir(), "state")
	createInspectionStage(t, home, stateRoot, "concurrent")
	const workers = 8
	var wait sync.WaitGroup
	errs := make(chan error, workers)
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			var stdout, stderr bytes.Buffer
			_, command := stageInspectCommand(t, home, stateRoot, true, &stdout, &stderr)
			if err := command.execute(context.Background(), "list"); err != nil {
				errs <- err
			} else if !strings.Contains(stdout.String(), `"schema":"mm/v2/stages"`) || stderr.Len() != 0 {
				errs <- errors.New("invalid concurrent output")
			}
		}()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
