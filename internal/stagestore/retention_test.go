//go:build darwin || linux

package stagestore

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestExpireEligibleUsesInclusiveActivityBoundaryAndWritesAuditEvents(t *testing.T) {
	s := openDomainStore(t)
	before, _ := s.Create(context.Background(), createInput("", "before"))
	at, _ := s.Create(context.Background(), createInput("", "at"))
	after, _ := s.Create(context.Background(), createInput("", "after"))
	cutoff := time.Now().UTC().Add(time.Hour).Truncate(time.Millisecond)
	for _, item := range []struct {
		id    string
		stamp time.Time
	}{{before.Stage.ID, cutoff.Add(-time.Millisecond)}, {at.Stage.ID, cutoff}, {after.Stage.ID, cutoff.Add(time.Millisecond)}} {
		if _, err := s.db.Exec(`UPDATE stages SET updated_at=? WHERE id=?`, formatTime(item.stamp), item.id); err != nil {
			t.Fatal(err)
		}
	}
	recorded := cutoff.Add(time.Hour)
	count, err := s.ExpireEligible(context.Background(), cutoff, recorded)
	if err != nil || count != 2 {
		t.Fatalf("expired=%d err=%v", count, err)
	}
	for _, item := range []struct {
		stage StageSummary
		want  Lifecycle
	}{{before.Stage, LifecycleExpired}, {at.Stage, LifecycleExpired}, {after.Stage, LifecycleOpen}} {
		detail, showErr := s.Show(context.Background(), item.stage.ID)
		if showErr != nil || detail.Lifecycle != item.want || detail.Body == nil {
			t.Fatalf("stage=%s lifecycle=%s body=%q err=%v", item.stage.ID, detail.Lifecycle, detail.Body, showErr)
		}
		events, eventErr := s.RetentionEvents(context.Background(), item.stage.ID)
		wantEvents := 0
		if item.want == LifecycleExpired {
			wantEvents = 1
		}
		if eventErr != nil || len(events) != wantEvents {
			t.Fatalf("stage=%s events=%+v err=%v", item.stage.ID, events, eventErr)
		}
		if wantEvents == 1 && (events[0].Event != "expired" || events[0].PolicySeconds == nil || *events[0].PolicySeconds != 3600) {
			t.Fatalf("expiry event=%+v", events[0])
		}
	}
}

func TestExactPruneErasesContentAndPreservesRequestReplay(t *testing.T) {
	s := openDomainStore(t)
	in := createInput("create-before-prune", "sensitive markdown")
	created, err := s.Create(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Cancel(context.Background(), CancelInput{created.Stage.ID, "cancel-before-prune", created.Stage.Revision, created.Stage.SemanticDigest}); err != nil {
		t.Fatal(err)
	}
	input := PruneInput{StageID: created.Stage.ID, RequestID: "prune-exact", ExpectedRevision: created.Stage.Revision, ExpectedDigest: created.Stage.SemanticDigest}
	pruned, err := s.Prune(context.Background(), input)
	if err != nil || pruned.Stage.Lifecycle != LifecyclePruned || pruned.Stage.Recovery != RecoveryForbidden {
		t.Fatalf("pruned=%+v err=%v", pruned, err)
	}
	detail, err := s.Show(context.Background(), created.Stage.ID)
	if err != nil || detail.Body != nil || len(detail.Attachments) != 0 || detail.Lifecycle != LifecyclePruned {
		t.Fatalf("detail=%+v err=%v", detail, err)
	}
	events, err := s.RetentionEvents(context.Background(), created.Stage.ID)
	if err != nil || len(events) != 1 || events[0].Event != "pruned" || events[0].RequestID != input.RequestID {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	replay, err := s.Prune(context.Background(), input)
	if err != nil || !replay.Replay || replay.RecordedAt != pruned.RecordedAt {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	conflict := input
	conflict.AbandonRecovery = true
	if _, err = s.Prune(context.Background(), conflict); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting replay=%v", err)
	}
	createdReplay, found, err := s.FindCreate(context.Background(), in.ServerURL, in.UserID, in.RequestID)
	if err != nil || !found || !createdReplay.Replay || createdReplay.Stage.ID != created.Stage.ID {
		t.Fatalf("create replay=%+v found=%v err=%v", createdReplay, found, err)
	}
}

func TestUnknownThenCancelRequiresExplicitRecoveryAbandonment(t *testing.T) {
	s := openDomainStore(t)
	created := createApplyStage(t, s, `{"steps":[{"ordinal":1,"type":"create_post","condition":"always"}]}`)
	attempt, err := s.ClaimApply(context.Background(), claimInput(created.Stage, "unknown-for-prune", RecoveryModeOrdinary))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.BeginDispatch(context.Background(), attempt.ID, 1); err != nil {
		t.Fatal(err)
	}
	if err = s.MarkStepUnknown(context.Background(), attempt.ID, 1); err != nil {
		t.Fatal(err)
	}
	if _, err = s.FinalizeApply(context.Background(), attempt.ID); err != nil {
		t.Fatal(err)
	}
	detail, err := s.Show(context.Background(), created.Stage.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Cancel(context.Background(), CancelInput{detail.ID, "cancel-unknown", detail.Revision, detail.SemanticDigest}); err != nil {
		t.Fatal(err)
	}
	ordinary := PruneInput{StageID: detail.ID, RequestID: "ordinary-unknown", ExpectedRevision: detail.Revision, ExpectedDigest: detail.SemanticDigest}
	if _, err = s.Prune(context.Background(), ordinary); !errors.Is(err, ErrNotEligible) {
		t.Fatalf("ordinary prune=%v", err)
	}
	ordinary.RequestID = "abandon-unknown"
	ordinary.AbandonRecovery = true
	pruned, err := s.Prune(context.Background(), ordinary)
	if err != nil || pruned.Stage.Lifecycle != LifecyclePruned {
		t.Fatalf("abandon=%+v err=%v", pruned, err)
	}
	events, err := s.RetentionEvents(context.Background(), detail.ID)
	if err != nil || len(events) != 1 || events[0].Event != "abandoned_recovery" || events[0].RecoveryMaterial != MaterialUnknown {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	var material RecoveryMaterial
	if err = s.db.QueryRow(`SELECT recovery_material FROM stages WHERE id=?`, detail.ID).Scan(&material); err != nil || material != MaterialNone {
		t.Fatalf("material=%s err=%v", material, err)
	}
}

func TestExactPruneRollsBackEventLifecycleAndErasureTogether(t *testing.T) {
	s := openDomainStore(t)
	created, _ := s.Create(context.Background(), createInput("", "rollback me"))
	if _, err := s.Cancel(context.Background(), CancelInput{created.Stage.ID, "", created.Stage.Revision, created.Stage.SemanticDigest}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER fail_prune_erasure BEFORE UPDATE OF body ON stage_revisions WHEN OLD.stage_id='` + created.Stage.ID + `' BEGIN SELECT RAISE(ABORT,'injected prune failure'); END;`); err != nil {
		t.Fatal(err)
	}
	_, err := s.Prune(context.Background(), PruneInput{StageID: created.Stage.ID, ExpectedRevision: created.Stage.Revision, ExpectedDigest: created.Stage.SemanticDigest})
	if err == nil {
		t.Fatal("injected prune failure succeeded")
	}
	detail, showErr := s.Show(context.Background(), created.Stage.ID)
	events, eventErr := s.RetentionEvents(context.Background(), created.Stage.ID)
	if showErr != nil || eventErr != nil || detail.Lifecycle != LifecycleCanceled || detail.Body == nil || len(detail.Attachments) == 0 || len(events) != 0 {
		t.Fatalf("detail=%+v events=%+v showErr=%v eventErr=%v", detail, events, showErr, eventErr)
	}
}

func TestBulkPruneSkipsCanceledStageWithRetainedUnknownMaterial(t *testing.T) {
	s := openDomainStore(t)
	ordinary, _ := s.Create(context.Background(), createInput("", "ordinary canceled"))
	if _, err := s.Cancel(context.Background(), CancelInput{ordinary.Stage.ID, "", ordinary.Stage.Revision, ordinary.Stage.SemanticDigest}); err != nil {
		t.Fatal(err)
	}
	unknown := createApplyStage(t, s, `{"steps":[{"ordinal":1,"type":"create_post","condition":"always"}]}`)
	attempt, err := s.ClaimApply(context.Background(), claimInput(unknown.Stage, "bulk-unknown", RecoveryModeOrdinary))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.BeginDispatch(context.Background(), attempt.ID, 1); err != nil {
		t.Fatal(err)
	}
	if err = s.MarkStepUnknown(context.Background(), attempt.ID, 1); err != nil {
		t.Fatal(err)
	}
	if _, err = s.FinalizeApply(context.Background(), attempt.ID); err != nil {
		t.Fatal(err)
	}
	unknownDetail, err := s.Show(context.Background(), unknown.Stage.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Cancel(context.Background(), CancelInput{unknownDetail.ID, "", unknownDetail.Revision, unknownDetail.SemanticDigest}); err != nil {
		t.Fatal(err)
	}
	cutoff := time.Now().UTC().Add(time.Hour)
	result, err := s.PruneEligible(context.Background(), cutoff, cutoff.Add(time.Hour))
	if err != nil || result.PrunedCount != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	ordinaryDetail, ordinaryErr := s.Show(context.Background(), ordinary.Stage.ID)
	unknownDetail, unknownErr := s.Show(context.Background(), unknown.Stage.ID)
	if ordinaryErr != nil || ordinaryDetail.Lifecycle != LifecyclePruned || unknownErr != nil || unknownDetail.Lifecycle != LifecycleCanceled || unknownDetail.Body == nil {
		t.Fatalf("ordinary=%+v/%v unknown=%+v/%v", ordinaryDetail, ordinaryErr, unknownDetail, unknownErr)
	}
}

func TestMigrationBackfillsCanceledRecoveryMaterialFromAttemptHistory(t *testing.T) {
	path := testPath(t)
	original := migrations
	migrations = append([]migration(nil), original[:10]...)
	t.Cleanup(func() { migrations = original })
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	unknown := createApplyStage(t, s, `{"steps":[{"ordinal":1,"type":"create_post","condition":"always"}]}`)
	unknownAttempt, err := s.ClaimApply(context.Background(), claimInput(unknown.Stage, "migration-unknown", RecoveryModeOrdinary))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.BeginDispatch(context.Background(), unknownAttempt.ID, 1); err != nil {
		t.Fatal(err)
	}
	if err = s.MarkStepUnknown(context.Background(), unknownAttempt.ID, 1); err != nil {
		t.Fatal(err)
	}
	if _, err = s.FinalizeApply(context.Background(), unknownAttempt.ID); err != nil {
		t.Fatal(err)
	}
	unknownCurrent, _ := s.Show(context.Background(), unknown.Stage.ID)
	if _, err = s.Cancel(context.Background(), CancelInput{unknownCurrent.ID, "", unknownCurrent.Revision, unknownCurrent.SemanticDigest}); err != nil {
		t.Fatal(err)
	}
	partial := createApplyStage(t, s, `{"steps":[{"ordinal":1,"type":"upload_attachment","condition":"always"},{"ordinal":2,"type":"create_post","condition":"always"}]}`)
	partialAttempt, err := s.ClaimApply(context.Background(), claimInput(partial.Stage, "migration-partial", RecoveryModeOrdinary))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.BeginDispatch(context.Background(), partialAttempt.ID, 1); err != nil {
		t.Fatal(err)
	}
	if err = s.MarkStepValidated(context.Background(), partialAttempt.ID, 1, []byte(`{"fileId":"file-1"}`)); err != nil {
		t.Fatal(err)
	}
	if err = s.BeginDispatch(context.Background(), partialAttempt.ID, 2); err != nil {
		t.Fatal(err)
	}
	if err = s.MarkStepRejected(context.Background(), partialAttempt.ID, 2, []byte(`{"status":403}`)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.FinalizeApply(context.Background(), partialAttempt.ID); err != nil {
		t.Fatal(err)
	}
	partialCurrent, _ := s.Show(context.Background(), partial.Stage.ID)
	if _, err = s.Cancel(context.Background(), CancelInput{partialCurrent.ID, "", partialCurrent.Revision, partialCurrent.SemanticDigest}); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	migrations = original
	s, err = Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	for _, want := range []struct {
		id       string
		material RecoveryMaterial
	}{{unknown.Stage.ID, MaterialUnknown}, {partial.Stage.ID, MaterialPartial}} {
		var got RecoveryMaterial
		if err = s.db.QueryRow(`SELECT recovery_material FROM stages WHERE id=?`, want.id).Scan(&got); err != nil || got != want.material {
			t.Fatalf("stage=%s material=%s want=%s err=%v", want.id, got, want.material, err)
		}
	}
}
