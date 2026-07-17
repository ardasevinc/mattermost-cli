//go:build e2e

package e2e_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGoConcurrentApplyProcessesDispatchExactlyOnce(t *testing.T) {
	h := newLiveHarness(t)
	self := h.user("me")
	alice := h.user("username/alice")
	channel := h.createChannel("direct", []string{self.ID, alice.ID})
	message := "# concurrent apply\n\nonly one process may dispatch\n"
	before := h.posts(channel.ID)

	writesBeforeStage := h.mutationSnapshot()
	var staged stageReceipt
	decodeCLI(t, h.cli(message, "--json", "stage", "send", "dm", "alice", "--request-id", "concurrent-stage"), &staged)
	h.assertMutationDelta(writesBeforeStage, nil)
	if staged.Schema != "mm/v2/stage-receipt" || staged.Stage.StageRef == "" {
		t.Fatalf("invalid stage receipt: %+v", staged)
	}

	arrived, release := h.armRequestGate("POST /api/v4/posts")
	first, firstStdout, firstStderr := h.cliCommand("", "--json", "apply", staged.Stage.StageRef, "--request-id", "concurrent-apply")
	contentionMarker := filepath.Join(h.home, "second-apply-contended")
	second, secondStdout, secondStderr := h.cliCommandWithEnv("", []string{"MM_E2E_LOCK_CONTENTION_MARKER=" + contentionMarker}, "--json", "apply", staged.Stage.StageRef, "--request-id", "concurrent-apply")
	writesBeforeApply := h.mutationSnapshot()
	if err := first.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Process.Kill() })
	select {
	case <-arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("first apply did not reach the gated mutation dispatch")
	}
	if err := second.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Process.Kill() })
	secondDone := make(chan error, 1)
	go func() { secondDone <- second.Wait() }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(contentionMarker); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if !time.Now().Before(deadline) {
			t.Fatal("second apply never contended on the winner's live store lock")
		}
		select {
		case err := <-secondDone:
			t.Fatalf("second apply exited before lock contention: %v", err)
		case <-time.After(10 * time.Millisecond):
		}
	}
	close(release)
	firstErr, secondErr := first.Wait(), <-secondDone
	if firstErr != nil || secondErr != nil || firstStderr.Len() != 0 || secondStderr.Len() != 0 {
		t.Fatalf("concurrent apply failed: first=%v/%q second=%v/%q", firstErr, firstStderr.String(), secondErr, secondStderr.String())
	}
	h.assertMutationDelta(writesBeforeApply, map[string]int{"POST /api/v4/posts": 1})

	var firstReceipt, secondReceipt applyReceipt
	decodeCLI(t, firstStdout.Bytes(), &firstReceipt)
	decodeCLI(t, secondStdout.Bytes(), &secondReceipt)
	if firstStdout.String() != secondStdout.String() || firstReceipt.Schema != "mm/v2/apply-receipt" || firstReceipt.AttemptID == "" || firstReceipt.AttemptID != secondReceipt.AttemptID || secondReceipt.Schema != firstReceipt.Schema ||
		firstReceipt.Outcome != "succeeded" || secondReceipt.Outcome != "succeeded" || firstReceipt.RecoveryMode != "ordinary" || secondReceipt.RecoveryMode != "ordinary" || firstReceipt.ForcedDuplicateRisk || secondReceipt.ForcedDuplicateRisk ||
		firstReceipt.Recovery != "forbidden" || secondReceipt.Recovery != "forbidden" || len(firstReceipt.Steps) != 1 || len(secondReceipt.Steps) != 1 || firstReceipt.Steps[0].Kind != "create_post" || secondReceipt.Steps[0].Kind != "create_post" || firstReceipt.Steps[0].State != "response_validated" || secondReceipt.Steps[0].State != "response_validated" {
		t.Fatalf("concurrent receipts diverged: first=%+v second=%+v", firstReceipt, secondReceipt)
	}
	firstPostID, secondPostID := createPostID(t, firstReceipt), createPostID(t, secondReceipt)
	if firstPostID != secondPostID {
		t.Fatalf("concurrent receipts bind different posts: %q != %q", firstPostID, secondPostID)
	}
	after := h.posts(channel.ID)
	if after.Posts[firstPostID].Message != message || countMessage(after, message) != 1 || len(after.Order) != len(before.Order)+1 {
		t.Fatalf("concurrent apply server state mismatch: before=%d after=%d matches=%d", len(before.Order), len(after.Order), countMessage(after, message))
	}
}
