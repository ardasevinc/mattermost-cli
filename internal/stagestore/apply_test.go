//go:build darwin || linux

package stagestore

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
)

func createApplyStage(t *testing.T, s *Store, plan string) CreateRecord {
	t.Helper()
	attachments := make([]Attachment, strings.Count(plan, `"type":"upload_attachment"`))
	for i := range attachments {
		attachments[i] = attachment(string(rune('a'+i)) + ".txt")
	}
	created, err := s.Create(context.Background(), CreateInput{
		RequestDigest: sha256.Sum256([]byte("apply-stage")), Operation: CreatePost, ServerURL: "https://mattermost.example/api/v4", ServerID: "server-1", UserID: "user-1",
		Content: RevisionContent{Body: []byte("**reviewed**\n"), Destination: json.RawMessage(`{"kind":"conversation","channelId":"channel-1"}`), Plan: json.RawMessage(plan), Attachments: attachments},
	})
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func createConversationStage(t *testing.T, s *Store) CreateRecord {
	t.Helper()
	created, err := s.Create(context.Background(), CreateInput{
		RequestDigest: sha256.Sum256([]byte("conversation-stage")), Operation: ResolveDM, ServerURL: "https://mattermost.example/api/v4", ServerID: "server-1", UserID: "user-1",
		Content: RevisionContent{Destination: json.RawMessage(`{"kind":"conversation","channelId":null,"participantIds":["peer-1"]}`), Plan: json.RawMessage(`{"steps":[{"ordinal":1,"type":"resolve_conversation","condition":"if_missing"}]}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func claimInput(stage StageSummary, request string, mode RecoveryMode) ApplyClaimInput {
	return ApplyClaimInput{StageID: stage.ID, RequestID: request, Revision: stage.Revision, ExpectedDigest: stage.SemanticDigest,
		RequestDigest: sha256.Sum256([]byte("apply\x00" + request)), RecoveryMode: mode}
}

func createPostResult(t *testing.T, attempt ApplyAttempt, postID string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(struct {
		PostID        string `json:"postId"`
		CreateAt      int64  `json:"createAt"`
		ChannelID     string `json:"channelId"`
		UserID        string `json:"userId"`
		PendingPostID string `json:"pendingPostId"`
	}{postID, 1784250000000, "channel-1", "user-1", attempt.PendingPostID})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestApplyClaimBindsExactRevisionAndReplaysCallerRequest(t *testing.T) {
	s := openDomainStore(t)
	created := createApplyStage(t, s, `{"steps":[{"ordinal":1,"type":"create_post","condition":"always"}]}`)
	in := claimInput(created.Stage, "apply-request-1", RecoveryModeOrdinary)
	claimed, err := s.ClaimApply(context.Background(), in)
	if err != nil || claimed.StageID != created.Stage.ID || claimed.Revision != 1 || claimed.RecoveryMode != RecoveryModeOrdinary || claimed.ForcedDuplicateRisk || len(claimed.Steps) != 1 || claimed.Steps[0].State != StepPending {
		t.Fatalf("claim=%+v err=%v", claimed, err)
	}
	replayed, err := s.ClaimApply(context.Background(), in)
	if err != nil || !replayed.Replay || replayed.ID != claimed.ID || replayed.PendingPostID != claimed.PendingPostID {
		t.Fatalf("replay=%+v err=%v", replayed, err)
	}
	conflict := in
	conflict.RequestDigest[0] ^= 0xff
	if _, err = s.ClaimApply(context.Background(), conflict); !errors.Is(err, ErrConflict) {
		t.Fatalf("request conflict=%v", err)
	}
	stale := in
	stale.RequestID = ""
	stale.RequestDigest = [32]byte{}
	stale.ExpectedDigest[0] ^= 0xff
	if _, err = s.ClaimApply(context.Background(), stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("revision conflict=%v", err)
	}
}

func TestApplyClaimAcceptsConversationResolutionPlan(t *testing.T) {
	s := openDomainStore(t)
	created := createConversationStage(t, s)
	claim, err := s.ClaimApply(context.Background(), claimInput(created.Stage, "", RecoveryModeOrdinary))
	if err != nil || claim.Steps[0].Kind != "resolve_conversation" {
		t.Fatalf("claim=%+v err=%v", claim, err)
	}
}

func TestApplySuccessJournalClearsSensitiveCompositionAtomically(t *testing.T) {
	s := openDomainStore(t)
	created := createApplyStage(t, s, `{"steps":[{"ordinal":1,"type":"create_post","condition":"always"}]}`)
	claimed, err := s.ClaimApply(context.Background(), claimInput(created.Stage, "", RecoveryModeOrdinary))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.BeginDispatch(context.Background(), claimed.ID, 1); err != nil {
		t.Fatal(err)
	}
	result := createPostResult(t, claimed, "post-1")
	if err = s.MarkStepValidated(context.Background(), claimed.ID, 1, result); err != nil {
		t.Fatal(err)
	}
	receipt, err := s.FinalizeApply(context.Background(), claimed.ID)
	if err != nil || receipt.Outcome != OutcomeSucceeded || receipt.Recovery != RecoveryForbidden || receipt.Steps[0].State != StepValidated || string(receipt.Steps[0].Result) != `{"postId":"post-1","createAt":1784250000000,"channelId":"channel-1","userId":"user-1","pendingPostId":"`+claimed.PendingPostID+`"}` {
		t.Fatalf("receipt=%+v err=%v", receipt, err)
	}
	detail, err := s.Show(context.Background(), created.Stage.ID)
	if err != nil || detail.Lifecycle != LifecycleCompleted || detail.Recovery != RecoveryForbidden || detail.Body != nil || len(detail.Attachments) != 0 {
		t.Fatalf("detail=%+v err=%v", detail, err)
	}
	replay, err := s.FinalizeApply(context.Background(), claimed.ID)
	if err != nil || !replay.Replay || replay.AttemptID != receipt.AttemptID || replay.RecordedAt != receipt.RecordedAt {
		t.Fatalf("receipt replay=%+v err=%v", replay, err)
	}
	if _, err = s.db.Exec(`UPDATE apply_attempts SET outcome='unknown' WHERE id=?`, claimed.ID); err == nil {
		t.Fatal("terminal attempt outcome mutation succeeded")
	}
	if _, err = s.db.Exec(`UPDATE stage_revisions SET destination_json='{}' WHERE stage_id=?`, created.Stage.ID); err == nil {
		t.Fatal("completed destination mutation succeeded")
	}
	if _, err = s.db.Exec(`UPDATE stage_revisions SET state='superseded' WHERE stage_id=? AND revision=1`, created.Stage.ID); err == nil {
		t.Fatal("completed revision replacement transition succeeded")
	}
	if _, err = s.db.Exec(`INSERT OR REPLACE INTO stage_revisions(stage_id,revision,state,created_at,semantic_digest,body,destination_json,plan_json)
		SELECT stage_id,revision,state,created_at,semantic_digest,body,destination_json,plan_json FROM stage_revisions WHERE stage_id=? AND revision=1`, created.Stage.ID); err == nil {
		t.Fatal("replace bypass of completed revision succeeded")
	}
}

func TestConfirmedApplyPreservesCreateAndReviseRequestReplayAfterContentErasure(t *testing.T) {
	s := openDomainStore(t)
	plan := json.RawMessage(`{"steps":[{"ordinal":1,"type":"create_post","condition":"always"}]}`)
	createInput := CreateInput{
		RequestID: "create-after-apply", RequestDigest: sha256.Sum256([]byte("create caller intent")), Operation: CreatePost,
		ServerURL: "https://mattermost.example/api/v4", ServerID: "server-1", UserID: "user-1",
		Content: RevisionContent{Body: []byte("original"), Destination: json.RawMessage(`{"kind":"conversation","channelId":"channel-1"}`), Plan: plan},
	}
	created, err := s.Create(context.Background(), createInput)
	if err != nil {
		t.Fatal(err)
	}
	reviseInput := ReviseInput{
		StageID: created.Stage.ID, RequestID: "revise-after-apply", ExpectedRevision: created.Stage.Revision, ExpectedDigest: created.Stage.SemanticDigest,
		RequestDigest: sha256.Sum256([]byte("revise caller intent")), Composition: Composition{Body: []byte("revised"), Plan: plan},
	}
	revised, err := s.Revise(context.Background(), reviseInput)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := s.ClaimApply(context.Background(), claimInput(revised.Stage, "", RecoveryModeOrdinary))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.BeginDispatch(context.Background(), claim.ID, 1); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`UPDATE apply_steps SET state='response_validated',result_json='{"message":"secret"}',ended_at='2026-01-01T00:00:01.000000Z' WHERE attempt_id=? AND ordinal=1`, claim.ID); err == nil {
		t.Fatal("arbitrary result transition succeeded")
	}
	mismatched := `{"postId":"post-1","createAt":1784250000000,"channelId":"channel-1","userId":"user-1","pendingPostId":"other"}`
	if _, err = s.db.Exec(`UPDATE apply_steps SET state='response_validated',result_json=?,ended_at='2026-01-01T00:00:01.000000Z' WHERE attempt_id=? AND ordinal=1`, mismatched, claim.ID); err == nil {
		t.Fatal("mismatched result transition succeeded")
	}
	if _, err = s.db.Exec(`UPDATE stage_revisions SET state='superseded' WHERE stage_id=? AND revision=1`, created.Stage.ID); err == nil {
		t.Fatal("applying revision replacement transition succeeded")
	}
	if err = s.MarkStepValidated(context.Background(), claim.ID, 1, createPostResult(t, claim, "post-replay")); err != nil {
		t.Fatal(err)
	}
	if _, err = s.FinalizeApply(context.Background(), claim.ID); err != nil {
		t.Fatal(err)
	}
	createReplay, err := s.Create(context.Background(), createInput)
	if err != nil || !createReplay.Replay || createReplay.Stage.ID != created.Stage.ID {
		t.Fatalf("create replay=%+v err=%v", createReplay, err)
	}
	reviseReplay, err := s.Revise(context.Background(), reviseInput)
	if err != nil || !reviseReplay.Replay || reviseReplay.Stage.ID != revised.Stage.ID || reviseReplay.Stage.Revision != revised.Stage.Revision {
		t.Fatalf("revise replay=%+v err=%v", reviseReplay, err)
	}
	if _, err = s.db.Exec(`UPDATE stage_revisions SET semantic_digest=zeroblob(32) WHERE stage_id=? AND revision=1`, created.Stage.ID); err == nil {
		t.Fatal("retained semantic digest mutation succeeded")
	}
	if _, err = s.db.Exec(`DROP TRIGGER stage_revision_semantics_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`UPDATE stage_revisions SET semantic_digest=zeroblob(32) WHERE stage_id=? AND revision=1`, created.Stage.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.FindCreate(context.Background(), created.Stage.ServerURL, created.Stage.UserID, createInput.RequestID); err == nil || errors.Is(err, ErrConflict) {
		t.Fatalf("corrupt retained digest=%v", err)
	}
}

func TestApplySuccessClearsSupersededRevisionPlaintextAndPaths(t *testing.T) {
	s := openDomainStore(t)
	plan := json.RawMessage(`{"steps":[{"ordinal":1,"type":"upload_attachment","condition":"always"},{"ordinal":2,"type":"create_post","condition":"always"}]}`)
	created := createApplyStage(t, s, string(plan))
	revised, err := s.Revise(context.Background(), ReviseInput{StageID: created.Stage.ID, ExpectedRevision: 1, ExpectedDigest: created.Stage.SemanticDigest,
		Composition: Composition{Body: []byte("revised secret"), Plan: plan, Attachments: []Attachment{attachment("revised.txt")}}})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := s.ClaimApply(context.Background(), claimInput(revised.Stage, "", RecoveryModeOrdinary))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.BeginDispatch(context.Background(), claim.ID, 1); err != nil {
		t.Fatal(err)
	}
	if err = s.MarkStepValidated(context.Background(), claim.ID, 1, json.RawMessage(`{"fileId":"file-1"}`)); err != nil {
		t.Fatal(err)
	}
	if err = s.BeginDispatch(context.Background(), claim.ID, 2); err != nil {
		t.Fatal(err)
	}
	if err = s.MarkStepValidated(context.Background(), claim.ID, 2, createPostResult(t, claim, "post-1")); err != nil {
		t.Fatal(err)
	}
	if _, err = s.FinalizeApply(context.Background(), claim.ID); err != nil {
		t.Fatal(err)
	}
	var bodies, paths int
	if err = s.db.QueryRow(`SELECT count(*) FROM stage_revisions WHERE stage_id=? AND body IS NOT NULL`, created.Stage.ID).Scan(&bodies); err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRow(`SELECT count(*) FROM stage_attachments WHERE stage_id=?`, created.Stage.ID).Scan(&paths); err != nil {
		t.Fatal(err)
	}
	if bodies != 0 || paths != 0 {
		t.Fatalf("retained bodies/paths=%d/%d", bodies, paths)
	}
}

func TestApplyReceiptRejectsBroadResultsAndUnconditionalSkip(t *testing.T) {
	s := openDomainStore(t)
	created := createApplyStage(t, s, `{"steps":[{"ordinal":1,"type":"create_post","condition":"always"}]}`)
	claim, _ := s.ClaimApply(context.Background(), claimInput(created.Stage, "", RecoveryModeOrdinary))
	if err := s.MarkStepSkipped(context.Background(), claim.ID, 1, json.RawMessage(`{"reason":"already_satisfied"}`)); !errors.Is(err, ErrNotEligible) {
		t.Fatalf("unconditional skip=%v", err)
	}
	if err := s.BeginDispatch(context.Background(), claim.ID, 1); err != nil {
		t.Fatal(err)
	}
	for _, broad := range []json.RawMessage{
		json.RawMessage(`{"postId":"post-1","createAt":1784250000000,"message":"secret"}`),
		json.RawMessage(`{"postId":"post-1","createAt":1784250000000,"raw":{"token":"secret"}}`),
	} {
		if err := s.MarkStepValidated(context.Background(), claim.ID, 1, broad); !errors.Is(err, ErrInvalid) {
			t.Fatalf("broad result %s = %v", broad, err)
		}
	}
	edit, err := s.Create(context.Background(), CreateInput{
		RequestDigest: sha256.Sum256([]byte("edit stage")),
		Operation:     EditPost, ServerURL: "https://mattermost.example/api/v4", ServerID: "server-1", UserID: "user-1",
		Content: RevisionContent{Body: []byte("edited"), Destination: json.RawMessage(`{"kind":"post","postId":"target-post"}`), Plan: json.RawMessage(`{"steps":[{"ordinal":1,"type":"edit_post","condition":"always"}]}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	editClaim, err := s.ClaimApply(context.Background(), claimInput(edit.Stage, "", RecoveryModeOrdinary))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.MarkStepSkipped(context.Background(), editClaim.ID, 1, json.RawMessage(`{"reason":"already_satisfied"}`)); !errors.Is(err, ErrNotEligible) {
		t.Fatalf("unconditional edit skip=%v", err)
	}
}

func TestApplyValidatedResultsBindTheClaimedRemoteEffect(t *testing.T) {
	s := openDomainStore(t)
	created := createApplyStage(t, s, `{"steps":[{"ordinal":1,"type":"create_post","condition":"always"}]}`)
	claim, err := s.ClaimApply(context.Background(), claimInput(created.Stage, "", RecoveryModeOrdinary))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.BeginDispatch(context.Background(), claim.ID, 1); err != nil {
		t.Fatal(err)
	}
	wrong := createPostResult(t, claim, "post-1")
	var decoded map[string]any
	if err = json.Unmarshal(wrong, &decoded); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{"channelId": "other-channel", "userId": "other-user", "pendingPostId": "other-pending"} {
		copy := make(map[string]any, len(decoded))
		for key, original := range decoded {
			copy[key] = original
		}
		copy[name] = value
		raw, marshalErr := json.Marshal(copy)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if err = s.MarkStepValidated(context.Background(), claim.ID, 1, raw); !errors.Is(err, ErrInvalid) {
			t.Fatalf("mismatched %s=%v", name, err)
		}
	}
	for _, tc := range []struct {
		name        string
		operation   Operation
		body        []byte
		destination string
		plan        string
		result      string
	}{
		{"edit", EditPost, []byte("edited"), `{"kind":"post","postId":"target-post"}`, `{"steps":[{"ordinal":1,"type":"edit_post","condition":"always"}]}`, `{"postId":"other-post","updateAt":1784250000000}`},
		{"delete", DeletePost, nil, `{"kind":"post","postId":"target-post"}`, `{"steps":[{"ordinal":1,"type":"delete_post","condition":"always"}]}`, `{"postId":"other-post","deleteAt":1784250000000}`},
		{"reaction", React, nil, `{"kind":"reaction","postId":"target-post"}`, `{"steps":[{"ordinal":1,"type":"add_reaction","condition":"if_missing"}]}`, `{"postId":"other-post"}`},
		{"conversation", ResolveDM, nil, `{"kind":"conversation","channelId":null,"participantIds":["peer-1"]}`, `{"steps":[{"ordinal":1,"type":"resolve_conversation","condition":"if_missing"}]}`, `{"channelId":"channel-1","participantIds":["other-peer"]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stage, createErr := s.Create(context.Background(), CreateInput{RequestDigest: sha256.Sum256([]byte(tc.name)), Operation: tc.operation,
				ServerURL: "https://mattermost.example/api/v4", ServerID: "server-1", UserID: "user-1",
				Content: RevisionContent{Body: tc.body, Destination: json.RawMessage(tc.destination), Plan: json.RawMessage(tc.plan)}})
			if createErr != nil {
				t.Fatal(createErr)
			}
			attempt, claimErr := s.ClaimApply(context.Background(), claimInput(stage.Stage, "", RecoveryModeOrdinary))
			if claimErr != nil {
				t.Fatal(claimErr)
			}
			if dispatchErr := s.BeginDispatch(context.Background(), attempt.ID, 1); dispatchErr != nil {
				t.Fatal(dispatchErr)
			}
			if validateErr := s.MarkStepValidated(context.Background(), attempt.ID, 1, json.RawMessage(tc.result)); !errors.Is(validateErr, ErrInvalid) {
				t.Fatalf("mismatched result=%v", validateErr)
			}
		})
	}
}

func TestHistoricalUnknownReceiptReplaysAfterForcedSuccess(t *testing.T) {
	s := openDomainStore(t)
	created := createApplyStage(t, s, `{"steps":[{"ordinal":1,"type":"create_post","condition":"always"}]}`)
	firstInput := claimInput(created.Stage, "unknown-attempt", RecoveryModeOrdinary)
	first, err := s.ClaimApply(context.Background(), firstInput)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.BeginDispatch(context.Background(), first.ID, 1); err != nil {
		t.Fatal(err)
	}
	if err = s.MarkStepUnknown(context.Background(), first.ID, 1); err != nil {
		t.Fatal(err)
	}
	unknown, err := s.FinalizeApply(context.Background(), first.ID)
	if err != nil || unknown.Outcome != OutcomeUnknown {
		t.Fatalf("unknown=%+v err=%v", unknown, err)
	}
	if _, err = s.db.Exec(`UPDATE stages SET recovery='none' WHERE id=?`, created.Stage.ID); err == nil {
		t.Fatal("unknown recovery downgrade succeeded")
	}
	if _, err = s.db.Exec(`UPDATE stages SET lifecycle='expired',recovery='forbidden' WHERE id=?`, created.Stage.ID); err == nil {
		t.Fatal("unknown recovery expiry bypass succeeded")
	}
	detail, err := s.Show(context.Background(), created.Stage.ID)
	if err != nil {
		t.Fatal(err)
	}
	forced, err := s.ClaimApply(context.Background(), claimInput(detail.StageSummary, "", RecoveryModeUnknown))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.BeginDispatch(context.Background(), forced.ID, 1); err != nil {
		t.Fatal(err)
	}
	if err = s.MarkStepValidated(context.Background(), forced.ID, 1, createPostResult(t, forced, "post-forced")); err != nil {
		t.Fatal(err)
	}
	if _, err = s.FinalizeApply(context.Background(), forced.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`UPDATE stages SET lifecycle='open',recovery='none' WHERE id=?`, created.Stage.ID); err == nil {
		t.Fatal("completed stage reopen succeeded")
	}
	storedAttempt, scanErr := scanApplyAttempt(context.Background(), s.db, first.ID)
	storedReceipt, receiptErr := loadApplyReceipt(context.Background(), s.db, first.ID)
	if scanErr != nil || receiptErr != nil || !validReceiptForAttempt(storedReceipt, storedAttempt) {
		t.Fatalf("stored historical binding attempt=%+v receipt=%+v scan=%v load=%v valid=%v", storedAttempt, storedReceipt, scanErr, receiptErr, validReceiptForAttempt(storedReceipt, storedAttempt))
	}
	replayed, err := s.FinalizeApply(context.Background(), first.ID)
	if err != nil || !replayed.Replay || replayed.Outcome != OutcomeUnknown || replayed.Recovery != RecoveryUnknown {
		t.Fatalf("historical replay=%+v err=%v", replayed, err)
	}
}

func TestApplyPartialAndUnknownRecoveryAreMonotonic(t *testing.T) {
	t.Run("validated residue then rejection", func(t *testing.T) {
		s := openDomainStore(t)
		created := createApplyStage(t, s, `{"steps":[{"ordinal":1,"type":"upload_attachment","condition":"always"},{"ordinal":2,"type":"create_post","condition":"always"}]}`)
		claim, _ := s.ClaimApply(context.Background(), claimInput(created.Stage, "", RecoveryModeOrdinary))
		_ = s.BeginDispatch(context.Background(), claim.ID, 1)
		_ = s.MarkStepValidated(context.Background(), claim.ID, 1, json.RawMessage(`{"fileId":"file-1"}`))
		_ = s.BeginDispatch(context.Background(), claim.ID, 2)
		_ = s.MarkStepRejected(context.Background(), claim.ID, 2, json.RawMessage(`{"status":400}`))
		receipt, err := s.FinalizeApply(context.Background(), claim.ID)
		if err != nil || receipt.Outcome != OutcomePartial || receipt.Recovery != RecoveryPartial {
			t.Fatalf("receipt=%+v err=%v", receipt, err)
		}
	})

	t.Run("later rejection cannot erase uncertainty", func(t *testing.T) {
		s := openDomainStore(t)
		created := createApplyStage(t, s, `{"steps":[{"ordinal":1,"type":"create_post","condition":"always"}]}`)
		first, _ := s.ClaimApply(context.Background(), claimInput(created.Stage, "", RecoveryModeOrdinary))
		_ = s.BeginDispatch(context.Background(), first.ID, 1)
		_ = s.MarkStepUnknown(context.Background(), first.ID, 1)
		unknown, err := s.FinalizeApply(context.Background(), first.ID)
		if err != nil || unknown.Outcome != OutcomeUnknown || unknown.Recovery != RecoveryUnknown {
			t.Fatalf("unknown=%+v err=%v", unknown, err)
		}
		detail, _ := s.Show(context.Background(), created.Stage.ID)
		forced, err := s.ClaimApply(context.Background(), claimInput(detail.StageSummary, "", RecoveryModeUnknown))
		if err != nil || !forced.ForcedDuplicateRisk {
			t.Fatalf("forced=%+v err=%v", forced, err)
		}
		_ = s.BeginDispatch(context.Background(), forced.ID, 1)
		_ = s.MarkStepRejected(context.Background(), forced.ID, 1, json.RawMessage(`{"status":403}`))
		rejected, err := s.FinalizeApply(context.Background(), forced.ID)
		if err != nil || rejected.Outcome != OutcomeRejected || rejected.Recovery != RecoveryUnknown {
			t.Fatalf("rejected=%+v err=%v", rejected, err)
		}
	})
}

func TestApplyPartialResumeIsRefusedUntilReuseProofIsBound(t *testing.T) {
	s := openDomainStore(t)
	created := createApplyStage(t, s, `{"steps":[{"ordinal":1,"type":"upload_attachment","condition":"always"},{"ordinal":2,"type":"create_post","condition":"always"}]}`)
	first, _ := s.ClaimApply(context.Background(), claimInput(created.Stage, "", RecoveryModeOrdinary))
	_ = s.BeginDispatch(context.Background(), first.ID, 1)
	_ = s.MarkStepValidated(context.Background(), first.ID, 1, json.RawMessage(`{"fileId":"file-1"}`))
	_ = s.BeginDispatch(context.Background(), first.ID, 2)
	_ = s.MarkStepRejected(context.Background(), first.ID, 2, json.RawMessage(`{"status":400}`))
	_, _ = s.FinalizeApply(context.Background(), first.ID)
	detail, _ := s.Show(context.Background(), created.Stage.ID)
	if _, err := s.ClaimApply(context.Background(), claimInput(detail.StageSummary, "", RecoveryModePartial)); !errors.Is(err, ErrNotEligible) {
		t.Fatalf("unsafe partial resume=%v", err)
	}
}

func TestApplyCanBeReleasedOnlyBeforeDispatch(t *testing.T) {
	s := openDomainStore(t)
	created := createApplyStage(t, s, `{"steps":[{"ordinal":1,"type":"create_post","condition":"always"}]}`)
	claim, _ := s.ClaimApply(context.Background(), claimInput(created.Stage, "release-1", RecoveryModeOrdinary))
	if err := s.AbandonApplyBeforeDispatch(context.Background(), claim.ID); err != nil {
		t.Fatal(err)
	}
	detail, err := s.Show(context.Background(), created.Stage.ID)
	if err != nil || detail.Lifecycle != LifecycleOpen || detail.Recovery != RecoveryNone {
		t.Fatalf("detail=%+v err=%v", detail, err)
	}
	if _, found, err := s.FindApply(context.Background(), created.Stage.ServerURL, created.Stage.UserID, "release-1", claimInput(created.Stage, "release-1", RecoveryModeOrdinary).RequestDigest); err != nil || found {
		t.Fatalf("released request found=%v err=%v", found, err)
	}
	second, _ := s.ClaimApply(context.Background(), claimInput(created.Stage, "", RecoveryModeOrdinary))
	_ = s.BeginDispatch(context.Background(), second.ID, 1)
	if err := s.AbandonApplyBeforeDispatch(context.Background(), second.ID); !errors.Is(err, ErrNotEligible) {
		t.Fatalf("post-dispatch release=%v", err)
	}
}

func TestApplyAuditHistoryCannotBeDeletedAfterDispatch(t *testing.T) {
	s := openDomainStore(t)
	created := createApplyStage(t, s, `{"steps":[{"ordinal":1,"type":"create_post","condition":"always"}]}`)
	claim, err := s.ClaimApply(context.Background(), claimInput(created.Stage, "audit-1", RecoveryModeOrdinary))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.BeginDispatch(context.Background(), claim.ID, 1); err != nil {
		t.Fatal(err)
	}
	var recursive int
	if err = s.db.QueryRow(`PRAGMA recursive_triggers`).Scan(&recursive); err != nil || recursive != 1 {
		t.Fatalf("recursive_triggers=%d err=%v", recursive, err)
	}
	for _, statement := range []string{
		`DELETE FROM apply_steps WHERE attempt_id=?`,
		`DELETE FROM apply_events WHERE attempt_id=?`,
		`DELETE FROM apply_requests WHERE attempt_id=?`,
		`DELETE FROM apply_attempts WHERE id=?`,
	} {
		if _, err = s.db.Exec(statement, claim.ID); err == nil {
			t.Fatalf("audit deletion succeeded: %s", statement)
		}
	}
	if _, err = s.db.Exec(`UPDATE apply_steps SET state='pending',started_at=NULL WHERE attempt_id=? AND ordinal=1`, claim.ID); err == nil {
		t.Fatal("dispatched step state regression succeeded")
	}
	if _, err = s.db.Exec(`UPDATE apply_steps SET started_at='2026-01-01T00:00:00.000000Z' WHERE attempt_id=? AND ordinal=1`, claim.ID); err == nil {
		t.Fatal("dispatched step timestamp mutation succeeded")
	}
	if _, err = s.db.Exec(`INSERT OR REPLACE INTO apply_steps(attempt_id,ordinal,kind,condition,state,result_json,started_at,ended_at)
		SELECT attempt_id,ordinal,kind,condition,state,result_json,started_at,ended_at FROM apply_steps WHERE attempt_id=? AND ordinal=1`, claim.ID); err == nil {
		t.Fatal("replace bypass of dispatched step succeeded")
	}
	if _, err = s.db.Exec(`UPDATE stages SET lifecycle='open',claim_attempt_id=NULL WHERE id=?`, created.Stage.ID); err == nil {
		t.Fatal("dispatched stage claim release succeeded")
	}
}

func TestApplyReplayBindsStageRevisionDigestAndMode(t *testing.T) {
	s := openDomainStore(t)
	first := createApplyStage(t, s, `{"steps":[{"ordinal":1,"type":"create_post","condition":"always"}]}`)
	second := createApplyStage(t, s, `{"steps":[{"ordinal":1,"type":"create_post","condition":"always"}]}`)
	in := claimInput(first.Stage, "bound-request", RecoveryModeOrdinary)
	if _, err := s.ClaimApply(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	wrong := in
	wrong.StageID, wrong.Revision, wrong.ExpectedDigest = second.Stage.ID, second.Stage.Revision, second.Stage.SemanticDigest
	if _, err := s.ClaimApply(context.Background(), wrong); !errors.Is(err, ErrConflict) {
		t.Fatalf("cross-stage replay=%v", err)
	}
}

func TestConcurrentApplyHasOneClaimWinner(t *testing.T) {
	s := openDomainStore(t)
	created := createApplyStage(t, s, `{"steps":[{"ordinal":1,"type":"create_post","condition":"always"}]}`)
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := s.ClaimApply(context.Background(), claimInput(created.Stage, "", RecoveryModeOrdinary))
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	success, ineligible := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrNotEligible) || errors.Is(err, ErrConflict) {
			ineligible++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || ineligible != 1 {
		t.Fatalf("success/ineligible=%d/%d", success, ineligible)
	}
}

func TestApplyJournalForbidsOutOfOrderDispatch(t *testing.T) {
	s := openDomainStore(t)
	created := createApplyStage(t, s, `{"steps":[{"ordinal":1,"type":"upload_attachment","condition":"always"},{"ordinal":2,"type":"create_post","condition":"always"}]}`)
	claim, err := s.ClaimApply(context.Background(), claimInput(created.Stage, "", RecoveryModeOrdinary))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.BeginDispatch(context.Background(), claim.ID, 2); !errors.Is(err, ErrNotEligible) {
		t.Fatalf("out-of-order dispatch=%v", err)
	}
	if err = s.BeginDispatch(context.Background(), claim.ID, 1); err != nil {
		t.Fatal(err)
	}
	if err = s.BeginDispatch(context.Background(), claim.ID, 2); !errors.Is(err, ErrNotEligible) {
		t.Fatalf("parallel dispatch=%v", err)
	}
}

func TestWritableOpenRecoversInterruptedApplyFromJournalEvidence(t *testing.T) {
	for _, tc := range []struct {
		name         string
		prepare      func(*testing.T, *Store, ApplyAttempt)
		wantRecovery Recovery
		wantOutcome  *AttemptOutcome
		wantFound    bool
		wantBody     bool
	}{
		{name: "no dispatch safely releases", wantRecovery: RecoveryNone, wantFound: false, wantBody: true},
		{name: "unmatched dispatch becomes unknown", prepare: func(t *testing.T, s *Store, a ApplyAttempt) {
			t.Helper()
			if err := s.BeginDispatch(context.Background(), a.ID, 1); err != nil {
				t.Fatal(err)
			}
		}, wantRecovery: RecoveryUnknown, wantOutcome: outcomePointer(OutcomeUnknown), wantFound: true, wantBody: true},
		{name: "journaled unknown seals pending suffix", prepare: func(t *testing.T, s *Store, a ApplyAttempt) {
			t.Helper()
			if err := s.BeginDispatch(context.Background(), a.ID, 1); err != nil {
				t.Fatal(err)
			}
			if err := s.MarkStepUnknown(context.Background(), a.ID, 1); err != nil {
				t.Fatal(err)
			}
		}, wantRecovery: RecoveryUnknown, wantOutcome: outcomePointer(OutcomeUnknown), wantFound: true, wantBody: true},
		{name: "validated prefix becomes partial", prepare: func(t *testing.T, s *Store, a ApplyAttempt) {
			t.Helper()
			if err := s.BeginDispatch(context.Background(), a.ID, 1); err != nil {
				t.Fatal(err)
			}
			if err := s.MarkStepValidated(context.Background(), a.ID, 1, json.RawMessage(`{"fileId":"file-1"}`)); err != nil {
				t.Fatal(err)
			}
		}, wantRecovery: RecoveryPartial, wantOutcome: outcomePointer(OutcomePartial), wantFound: true, wantBody: true},
		{name: "fully validated attempt completes", prepare: func(t *testing.T, s *Store, a ApplyAttempt) {
			t.Helper()
			for _, step := range a.Steps {
				if err := s.BeginDispatch(context.Background(), a.ID, step.Ordinal); err != nil {
					t.Fatal(err)
				}
				result := createPostResult(t, a, "post-1")
				if step.Kind == "upload_attachment" {
					result = json.RawMessage(`{"fileId":"file-1"}`)
				}
				if err := s.MarkStepValidated(context.Background(), a.ID, step.Ordinal, result); err != nil {
					t.Fatal(err)
				}
			}
		}, wantRecovery: RecoveryForbidden, wantOutcome: outcomePointer(OutcomeSucceeded), wantFound: true, wantBody: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := testPath(t)
			s, err := Open(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			plan := `{"steps":[{"ordinal":1,"type":"create_post","condition":"always"}]}`
			if tc.name == "validated prefix becomes partial" || tc.name == "journaled unknown seals pending suffix" {
				plan = `{"steps":[{"ordinal":1,"type":"upload_attachment","condition":"always"},{"ordinal":2,"type":"create_post","condition":"always"}]}`
			}
			created := createApplyStage(t, s, plan)
			input := claimInput(created.Stage, "recover-request", RecoveryModeOrdinary)
			claim, err := s.ClaimApply(context.Background(), input)
			if err != nil {
				t.Fatal(err)
			}
			if tc.prepare != nil {
				tc.prepare(t, s, claim)
			}
			if err = s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = Open(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			detail, err := s.Show(context.Background(), created.Stage.ID)
			if err != nil || detail.Recovery != tc.wantRecovery || (detail.Body != nil) != tc.wantBody || detail.Lifecycle == LifecycleApplying {
				t.Fatalf("detail=%+v err=%v", detail, err)
			}
			recovered, found, err := s.FindApply(context.Background(), created.Stage.ServerURL, created.Stage.UserID, "recover-request", input.RequestDigest)
			if err != nil || found != tc.wantFound {
				t.Fatalf("found=%v attempt=%+v err=%v", found, recovered, err)
			}
			if tc.wantOutcome != nil && (recovered.Outcome == nil || *recovered.Outcome != *tc.wantOutcome) {
				t.Fatalf("outcome=%v want=%v", recovered.Outcome, *tc.wantOutcome)
			}
			if tc.wantOutcome != nil && *tc.wantOutcome == OutcomePartial && recovered.Steps[1].State != StepNotSent {
				t.Fatalf("partial suffix=%+v", recovered.Steps)
			}
		})
	}
}

func outcomePointer(value AttemptOutcome) *AttemptOutcome { return &value }
