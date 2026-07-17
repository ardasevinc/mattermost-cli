//go:build darwin || linux

package cli

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mmSchema "github.com/ardasevinc/mattermost-cli/v2/internal/schema"
	"github.com/ardasevinc/mattermost-cli/v2/internal/stagestore"
)

func setOfflineStageEnvironment(t *testing.T, stateRoot string) {
	t.Helper()
	setTestHome(t, t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_STATE_HOME", stateRoot)
	t.Setenv("MM_URL", "")
	t.Setenv("MM_TOKEN", "")
}

func loadStoredStage(t *testing.T, stateRoot, stageID string) stagestore.StageDetail {
	t.Helper()
	paths, err := stagestore.ResolvePaths(t.TempDir(), func(key string) (string, bool) { return stateRoot, key == "XDG_STATE_HOME" })
	if err != nil {
		t.Fatal(err)
	}
	store, err := stagestore.OpenReadOnly(t.Context(), paths.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := store.Show(t.Context(), stageID)
	closeErr := store.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("show=%v close=%v", err, closeErr)
	}
	return detail
}

func TestStructuredReviseAndCancelAreOfflineCASBoundAndReplayable(t *testing.T) {
	home, stateRoot := t.TempDir(), filepath.Join(t.TempDir(), "state")
	stageID := createInspectionStage(t, home, stateRoot, "# original\n")
	setOfflineStageEnvironment(t, stateRoot)
	original := loadStoredStage(t, stateRoot, stageID)
	replacement := "# revised\n\n" + strings.Repeat("- durable\n", 100)
	reviseRequest := map[string]any{
		"schema": "mm/v2/stage-revise-request", "requestId": "revision-1", "stageId": stageID,
		"expectedRevision": original.Revision, "expectedDigest": hex.EncodeToString(original.SemanticDigest[:]),
		"revive": false, "body": replacement, "attachments": nil,
	}
	reviseJSON, err := json.Marshal(reviseRequest)
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := Execute(t.Context(), []string{"stage", "revise", "--from-json"}, bytes.NewReader(reviseJSON), &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("revise exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	registry, err := mmSchema.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Validate("mm/v2/stage-receipt", bytes.NewReader(stdout.Bytes())); err != nil {
		t.Fatalf("revision receipt schema: %v\n%s", err, stdout.String())
	}
	if strings.Contains(stdout.String(), replacement) || !strings.Contains(stdout.String(), `"action":"revised"`) || !strings.Contains(stdout.String(), `"revision":2`) {
		t.Fatalf("revision receipt=%s", stdout.String())
	}
	revised := loadStoredStage(t, stateRoot, stageID)
	if revised.Revision != 2 || string(revised.Body) != replacement {
		t.Fatalf("revision=%d body=%q", revised.Revision, revised.Body)
	}

	cancelRequest := map[string]any{
		"schema": "mm/v2/stage-cancel-request", "requestId": "cancel-1", "stageId": stageID,
		"expectedRevision": revised.Revision, "expectedDigest": hex.EncodeToString(revised.SemanticDigest[:]),
	}
	cancelJSON, err := json.Marshal(cancelRequest)
	if err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	code = Execute(t.Context(), []string{"stage", "cancel", "--from-json"}, bytes.NewReader(cancelJSON), &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), `"action":"canceled"`) || !strings.Contains(stdout.String(), `"replayed":false`) {
		t.Fatalf("cancel exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	code = Execute(t.Context(), []string{"stage", "cancel", "--from-json"}, bytes.NewReader(cancelJSON), &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), `"replayed":true`) {
		t.Fatalf("cancel replay exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	canceled := loadStoredStage(t, stateRoot, stageID)
	if canceled.Lifecycle != stagestore.LifecycleCanceled || canceled.Recovery != stagestore.RecoveryForbidden || string(canceled.Body) != replacement {
		t.Fatalf("canceled=%+v body=%q", canceled.StageSummary, canceled.Body)
	}
}

func TestHumanReviseUsesCurrentCASAndHumanCancelClosesStage(t *testing.T) {
	home, stateRoot := t.TempDir(), filepath.Join(t.TempDir(), "state")
	stageID := createInspectionStage(t, home, stateRoot, "old")
	setOfflineStageEnvironment(t, stateRoot)
	var stdout, stderr bytes.Buffer
	code := Execute(t.Context(), []string{"stage", "revise", stageID, "--message", "new **markdown**"}, nil, &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), "revised: "+stageID+"@2") {
		t.Fatalf("revise exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if got := loadStoredStage(t, stateRoot, stageID); string(got.Body) != "new **markdown**" || got.Revision != 2 {
		t.Fatalf("detail revision=%d body=%q", got.Revision, got.Body)
	}
	stdout.Reset()
	stderr.Reset()
	code = Execute(t.Context(), []string{"stage", "cancel", stageID}, nil, &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 || stdout.String() != "canceled: "+stageID+"@2\n" {
		t.Fatalf("cancel exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestStageMutationFromJSONRejectsHumanFlagMixingAsMachineError(t *testing.T) {
	setOfflineStageEnvironment(t, filepath.Join(t.TempDir(), "state"))
	for _, args := range [][]string{
		{"stage", "revise", "--from-json", "--message", "nope"},
		{"stage", "cancel", "--from-json", "--request-id", "nope"},
		{"stage", "prune", "--from-json", "--older-than", "720h"},
	} {
		var stdout, stderr bytes.Buffer
		code := Execute(t.Context(), args, strings.NewReader(`{}`), &stdout, &stderr)
		if code != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), `"schema":"mm/v2/error"`) || !strings.Contains(stderr.String(), `"code":"invalid_input"`) {
			t.Fatalf("args=%v exit=%d stdout=%q stderr=%q", args, code, stdout.String(), stderr.String())
		}
	}
}

func TestStructuredExactPruneIsReplayableAndErasesRetainedContent(t *testing.T) {
	home, stateRoot := t.TempDir(), filepath.Join(t.TempDir(), "state")
	stageID := createInspectionStage(t, home, stateRoot, "# retained\n\n**markdown**")
	setOfflineStageEnvironment(t, stateRoot)
	var stdout, stderr bytes.Buffer
	if code := Execute(t.Context(), []string{"stage", "cancel", stageID}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("cancel exit=%d stderr=%q", code, stderr.String())
	}
	canceled := loadStoredStage(t, stateRoot, stageID)
	request := map[string]any{
		"schema": "mm/v2/stage-prune-request", "requestId": "prune-structured-1", "stageId": stageID,
		"expectedRevision": canceled.Revision, "expectedDigest": hex.EncodeToString(canceled.SemanticDigest[:]), "abandonRecovery": false,
	}
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		stdout.Reset()
		stderr.Reset()
		code := Execute(t.Context(), []string{"stage", "prune", "--from-json"}, bytes.NewReader(raw), &stdout, &stderr)
		if code != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), `"action":"pruned"`) || !strings.Contains(stdout.String(), `"replayed":`+[]string{"false", "true"}[attempt]) {
			t.Fatalf("attempt=%d exit=%d stdout=%q stderr=%q", attempt, code, stdout.String(), stderr.String())
		}
	}
	pruned := loadStoredStage(t, stateRoot, stageID)
	if pruned.Lifecycle != stagestore.LifecyclePruned || pruned.Body != nil || len(pruned.Attachments) != 0 {
		t.Fatalf("pruned=%+v body=%q attachments=%d", pruned.StageSummary, pruned.Body, len(pruned.Attachments))
	}
}

func TestBulkPruneFailsClosedWithoutAgeAndUsesConfiguredAge(t *testing.T) {
	home, stateRoot, configRoot := t.TempDir(), filepath.Join(t.TempDir(), "state"), filepath.Join(t.TempDir(), "config")
	setTestHome(t, home)
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	t.Setenv("XDG_STATE_HOME", stateRoot)
	t.Setenv("MM_URL", "")
	t.Setenv("MM_TOKEN", "")
	var stdout, stderr bytes.Buffer
	code := Execute(t.Context(), []string{"stage", "prune"}, nil, &stdout, &stderr)
	if code != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "bulk prune requires") {
		t.Fatalf("disabled exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	configPath := filepath.Join(configRoot, "mattermost-cli", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("stage_prune_after_seconds = 3600\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	code = Execute(t.Context(), []string{"--json", "stage", "prune"}, nil, &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), `"schema":"mm/v2/stage-prune-result"`) || !strings.Contains(stdout.String(), `"prunedCount":0`) {
		t.Fatalf("configured exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestConfiguredTTLIsOpportunisticAndReadOnlyInspectionDoesNotMutate(t *testing.T) {
	home, stateRoot, configRoot := t.TempDir(), filepath.Join(t.TempDir(), "state"), filepath.Join(t.TempDir(), "config")
	stageID := createInspectionStage(t, home, stateRoot, "still reviewable")
	setTestHome(t, home)
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	t.Setenv("XDG_STATE_HOME", stateRoot)
	t.Setenv("MM_URL", "")
	t.Setenv("MM_TOKEN", "")
	configPath := filepath.Join(configRoot, "mattermost-cli", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("stage_ttl_seconds = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond)
	var stdout, stderr bytes.Buffer
	if code := Execute(t.Context(), []string{"stage", "list"}, nil, &stdout, &stderr); code != 0 || stderr.Len() != 0 {
		t.Fatalf("list exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if detail := loadStoredStage(t, stateRoot, stageID); detail.Lifecycle != stagestore.LifecycleOpen {
		t.Fatalf("read-only list expired stage: %+v", detail.StageSummary)
	}
	stdout.Reset()
	stderr.Reset()
	code := Execute(t.Context(), []string{"stage", "cancel", stageID}, nil, &stdout, &stderr)
	if code != 6 || stdout.Len() != 0 {
		t.Fatalf("cancel exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if detail := loadStoredStage(t, stateRoot, stageID); detail.Lifecycle != stagestore.LifecycleExpired || detail.Body == nil {
		t.Fatalf("writable operation did not expire safely: %+v body=%q", detail.StageSummary, detail.Body)
	}
}
