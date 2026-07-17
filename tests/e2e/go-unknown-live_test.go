//go:build e2e

package e2e_test

import "testing"

func TestGoApplyCrashAfterServerAcceptanceRequiresExplicitForce(t *testing.T) {
	h := newLiveHarness(t)
	self := h.user("me")
	alice := h.user("username/alice")
	channel := h.createChannel("direct", []string{self.ID, alice.ID})
	message := "# accepted before crash\n\nthis may already exist exactly once\n"
	before := h.posts(channel.ID)

	writesBeforeStage := h.mutationSnapshot()
	var staged stageReceipt
	decodeCLI(t, h.cli(message, "--json", "stage", "send", "dm", "alice", "--request-id", "crash-stage"), &staged)
	h.assertMutationDelta(writesBeforeStage, nil)
	if staged.Schema != "mm/v2/stage-receipt" || staged.Stage.StageRef == "" {
		t.Fatalf("invalid stage receipt: %+v", staged)
	}

	writesBeforeCrash := h.mutationSnapshot()
	stdout, stderr := h.cliKilledAfterResponse("", "POST /api/v4/posts", "--json", "apply", staged.Stage.StageRef, "--request-id", "crash-apply")
	h.assertMutationDelta(writesBeforeCrash, map[string]int{"POST /api/v4/posts": 1})
	if len(stdout) != 0 || len(stderr) != 0 {
		t.Fatalf("killed CLI emitted output: stdout=%q stderr=%q", stdout, stderr)
	}
	afterCrash := h.posts(channel.ID)
	if countMessage(afterCrash, message) != 1 || len(afterCrash.Order) != len(before.Order)+1 {
		t.Fatalf("accepted crash mutation mismatch: before=%d after=%d matches=%d", len(before.Order), len(afterCrash.Order), countMessage(afterCrash, message))
	}

	writesBeforeReplay := h.mutationSnapshot()
	replayRaw, replayStderr, replayCode := h.cliResult("", "--json", "apply", staged.Stage.StageRef, "--request-id", "crash-apply")
	h.assertMutationDelta(writesBeforeReplay, nil)
	var replay applyReceipt
	decodeCLI(t, replayRaw, &replay)
	if replayCode != 5 || len(replayStderr) != 0 || replay.Schema != "mm/v2/apply-receipt" || replay.AttemptID == "" || replay.Outcome != "unknown" || replay.Recovery != "force_unknown" || replay.RecoveryMode != "ordinary" || replay.ForcedDuplicateRisk ||
		len(replay.Steps) != 1 || replay.Steps[0].Kind != "create_post" || replay.Steps[0].State != "outcome_unknown" || string(replay.Steps[0].Result) != "null" {
		t.Fatalf("invalid recovered replay: code=%d stderr=%q receipt=%+v", replayCode, replayStderr, replay)
	}
	if afterReplay := h.posts(channel.ID); countMessage(afterReplay, message) != 1 || len(afterReplay.Order) != len(afterCrash.Order) {
		t.Fatalf("unknown replay dispatched again: after-crash=%d after-replay=%d matches=%d", len(afterCrash.Order), len(afterReplay.Order), countMessage(afterReplay, message))
	}

	writesBeforeForce := h.mutationSnapshot()
	forcedRaw, forcedStderr, forcedCode := h.cliResult("", "--json", "apply", staged.Stage.StageRef, "--force-unknown", "--request-id", "crash-force")
	h.assertMutationDelta(writesBeforeForce, map[string]int{"POST /api/v4/posts": 1})
	var forced applyReceipt
	decodeCLI(t, forcedRaw, &forced)
	const forceWarning = "warning: forcing an unknown stage may duplicate a real Mattermost side effect; inspect the destination first\n"
	if forcedCode != 0 || string(forcedStderr) != forceWarning || forced.Schema != "mm/v2/apply-receipt" || forced.AttemptID == "" || forced.AttemptID == replay.AttemptID || forced.Outcome != "succeeded" || forced.Recovery != "forbidden" || forced.RecoveryMode != "force_unknown" || !forced.ForcedDuplicateRisk ||
		len(forced.Steps) != 1 || forced.Steps[0].Kind != "create_post" || forced.Steps[0].State != "response_validated" {
		t.Fatalf("invalid forced apply: code=%d stderr=%q receipt=%+v", forcedCode, forcedStderr, forced)
	}
	forcedPostID := createPostID(t, forced)
	afterForce := h.posts(channel.ID)
	if _, existed := afterCrash.Posts[forcedPostID]; existed || afterForce.Posts[forcedPostID].Message != message || countMessage(afterForce, message) != 2 || len(afterForce.Order) != len(before.Order)+2 {
		t.Fatalf("explicit force did not expose duplicate risk: before=%d after=%d matches=%d", len(before.Order), len(afterForce.Order), countMessage(afterForce, message))
	}
}
