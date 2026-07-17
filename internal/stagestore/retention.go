package stagestore

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type RecoveryMaterial string

const (
	MaterialNone    RecoveryMaterial = "none"
	MaterialPartial RecoveryMaterial = "resume_partial"
	MaterialUnknown RecoveryMaterial = "force_unknown"
)

type PruneInput struct {
	StageID, RequestID string
	ExpectedRevision   int64
	ExpectedDigest     [32]byte
	AbandonRecovery    bool
}

type BulkPruneResult struct {
	Schema      string    `json:"schema"`
	Action      string    `json:"action"`
	Cutoff      time.Time `json:"cutoff"`
	PrunedCount int64     `json:"prunedCount"`
	RecordedAt  time.Time `json:"recordedAt"`
}

type RetentionEvent struct {
	Sequence         int64
	StageID          string
	Revision         int64
	SemanticDigest   [32]byte
	Event            string
	FromLifecycle    Lifecycle
	FromRecovery     Recovery
	RecoveryMaterial RecoveryMaterial
	PolicySeconds    *int64
	RequestID        string
	RecordedAt       time.Time
}

func recoveryMaterialFor(lifecycle Lifecycle, recovery Recovery) RecoveryMaterial {
	if lifecycle == LifecycleCompleted || recovery == RecoveryForbidden {
		return MaterialNone
	}
	switch recovery {
	case RecoveryPartial:
		return MaterialPartial
	case RecoveryUnknown:
		return MaterialUnknown
	default:
		return MaterialNone
	}
}

// ExpireEligible durably revokes only inactive ordinary-apply stages at or
// before cutoff. The caller supplies one recorded time for the whole sweep.
func (s *Store) ExpireEligible(ctx context.Context, cutoff, recordedAt time.Time) (int64, error) {
	if ctx == nil || cutoff.IsZero() || recordedAt.IsZero() || !cutoff.Before(recordedAt) || !retentionLifecycleAvailable() {
		return 0, ErrInvalid
	}
	seconds := int64(recordedAt.Sub(cutoff) / time.Second)
	if seconds < 1 {
		return 0, ErrInvalid
	}
	cutoffStamp, stamp := formatTime(cutoff.UTC()), formatTime(recordedAt.UTC())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, localError(err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `INSERT INTO stage_retention_events(stage_id,revision,semantic_digest,event,from_lifecycle,from_recovery,recovery_material,policy_seconds,request_id,recorded_at)
		SELECT s.id,s.current_revision,r.semantic_digest,'expired',s.lifecycle,s.recovery,s.recovery_material,?,NULL,?
		FROM stages s JOIN stage_revisions r ON r.stage_id=s.id AND r.revision=s.current_revision
		WHERE s.lifecycle='open' AND s.recovery='none' AND s.recovery_material='none' AND s.claim_attempt_id IS NULL AND s.updated_at<=?`, seconds, stamp, cutoffStamp)
	if err != nil {
		return 0, localError(err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, localError(err)
	}
	result, err = tx.ExecContext(ctx, `UPDATE stages SET lifecycle='expired',recovery='forbidden',updated_at=?
		WHERE lifecycle='open' AND recovery='none' AND recovery_material='none' AND claim_attempt_id IS NULL AND updated_at<=?`, stamp, cutoffStamp)
	if err != nil {
		return 0, localError(err)
	}
	updated, err := result.RowsAffected()
	if err != nil || updated != count {
		return 0, localError(errors.New("expiry selection changed"))
	}
	if err = tx.Commit(); err != nil {
		return 0, localError(err)
	}
	runCommitHook()
	return count, nil
}

// PruneEligible atomically erases sensitive content for all eligible terminal
// stages at or before cutoff without returning an unbounded stage list.
func (s *Store) PruneEligible(ctx context.Context, cutoff, recordedAt time.Time) (BulkPruneResult, error) {
	result := BulkPruneResult{"mm/v2/stage-prune-result", "pruned", cutoff.UTC(), 0, recordedAt.UTC()}
	if ctx == nil || cutoff.IsZero() || recordedAt.IsZero() || !cutoff.Before(recordedAt) || !retentionLifecycleAvailable() {
		return BulkPruneResult{}, ErrInvalid
	}
	seconds := int64(recordedAt.Sub(cutoff) / time.Second)
	if seconds < 1 {
		return BulkPruneResult{}, ErrInvalid
	}
	cutoffStamp, stamp := formatTime(cutoff.UTC()), formatTime(recordedAt.UTC())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return BulkPruneResult{}, localError(err)
	}
	defer tx.Rollback()
	inserted, err := tx.ExecContext(ctx, `INSERT INTO stage_retention_events(stage_id,revision,semantic_digest,event,from_lifecycle,from_recovery,recovery_material,policy_seconds,request_id,recorded_at)
		SELECT s.id,s.current_revision,r.semantic_digest,'pruned',s.lifecycle,s.recovery,s.recovery_material,?,NULL,?
		FROM stages s JOIN stage_revisions r ON r.stage_id=s.id AND r.revision=s.current_revision
		WHERE s.lifecycle IN ('completed','canceled','expired') AND s.recovery='forbidden' AND s.recovery_material='none'
		AND s.claim_attempt_id IS NULL AND s.updated_at<=?`, seconds, stamp, cutoffStamp)
	if err != nil {
		return BulkPruneResult{}, localError(err)
	}
	result.PrunedCount, err = inserted.RowsAffected()
	if err != nil {
		return BulkPruneResult{}, localError(err)
	}
	updated, err := tx.ExecContext(ctx, `UPDATE stages SET lifecycle='pruned',recovery='forbidden',recovery_material='none',updated_at=?
		WHERE lifecycle IN ('completed','canceled','expired') AND recovery='forbidden' AND recovery_material='none'
		AND claim_attempt_id IS NULL AND updated_at<=?`, stamp, cutoffStamp)
	if err != nil {
		return BulkPruneResult{}, localError(err)
	}
	count, err := updated.RowsAffected()
	if err != nil || count != result.PrunedCount {
		return BulkPruneResult{}, localError(errors.New("prune selection changed"))
	}
	if err = erasePrunedContent(ctx, tx, stamp); err != nil {
		return BulkPruneResult{}, err
	}
	if err = tx.Commit(); err != nil {
		return BulkPruneResult{}, localError(err)
	}
	runCommitHook()
	return result, nil
}

func (s *Store) Prune(ctx context.Context, in PruneInput) (MutationResult, error) {
	if ctx == nil || !bounded(in.StageID, maxIdentityBytes) || in.ExpectedRevision < 1 || !validRequestID(in.RequestID) || in.ExpectedDigest == ([32]byte{}) || !retentionLifecycleAvailable() {
		return MutationResult{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MutationResult{}, localError(err)
	}
	defer tx.Rollback()
	base, err := scanCurrent(ctx, tx, in.StageID)
	if err != nil {
		return MutationResult{}, err
	}
	digest := pruneRequestDigest(in)
	if in.RequestID != "" {
		if replay, found, replayErr := loadReplay(ctx, tx, base.ServerURL, base.UserID, in.RequestID, "mm/v2/stage-prune-request", digest); replayErr != nil {
			return MutationResult{}, replayErr
		} else if found {
			return replay, nil
		}
	}
	if base.Revision != in.ExpectedRevision || base.SemanticDigest != in.ExpectedDigest {
		return MutationResult{}, ErrConflict
	}
	var material RecoveryMaterial
	var claimed sql.NullString
	if err = tx.QueryRowContext(ctx, `SELECT recovery_material,claim_attempt_id FROM stages WHERE id=?`, in.StageID).Scan(&material, &claimed); err != nil {
		return MutationResult{}, localError(err)
	}
	if claimed.Valid || base.Lifecycle == LifecycleApplying || base.Lifecycle == LifecyclePruned {
		return MutationResult{}, ErrNotEligible
	}
	event := "pruned"
	if in.AbandonRecovery {
		if material != MaterialPartial && material != MaterialUnknown {
			return MutationResult{}, ErrNotEligible
		}
		event = "abandoned_recovery"
	} else if material != MaterialNone || base.Lifecycle != LifecycleCompleted && base.Lifecycle != LifecycleCanceled && base.Lifecycle != LifecycleExpired {
		return MutationResult{}, ErrNotEligible
	}
	now := time.Now().UTC()
	stamp := formatTime(now)
	if _, err = tx.ExecContext(ctx, `INSERT INTO stage_retention_events(stage_id,revision,semantic_digest,event,from_lifecycle,from_recovery,recovery_material,policy_seconds,request_id,recorded_at)
		VALUES(?,?,?,?,?,?,?,NULL,?,?)`, base.ID, base.Revision, base.SemanticDigest[:], event, base.Lifecycle, base.Recovery, material, nullable(in.RequestID), stamp); err != nil {
		return MutationResult{}, localError(err)
	}
	updated, err := tx.ExecContext(ctx, `UPDATE stages SET lifecycle='pruned',recovery='forbidden',recovery_material='none',updated_at=?
		WHERE id=? AND current_revision=? AND lifecycle=? AND recovery=? AND recovery_material=? AND claim_attempt_id IS NULL
		AND EXISTS(SELECT 1 FROM stage_revisions WHERE stage_id=? AND revision=? AND semantic_digest=?)`, stamp, base.ID, base.Revision, base.Lifecycle, base.Recovery, material, base.ID, base.Revision, base.SemanticDigest[:])
	if err != nil {
		return MutationResult{}, localError(err)
	}
	if !oneRow(updated) {
		return MutationResult{}, ErrConflict
	}
	if _, err = tx.ExecContext(ctx, `UPDATE stage_revisions SET body=NULL WHERE stage_id=?`, base.ID); err != nil {
		return MutationResult{}, localError(err)
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM stage_attachments WHERE stage_id=?`, base.ID); err != nil {
		return MutationResult{}, localError(err)
	}
	summary := base.StageSummary
	summary.Lifecycle, summary.Recovery, summary.UpdatedAt = LifecyclePruned, RecoveryForbidden, now
	result := MutationResult{"mm/v2/stage-mutation-receipt", "prune", summary, false, now, false}
	if err = persistReplay(ctx, tx, base.ServerURL, base.UserID, in.RequestID, "mm/v2/stage-prune-request", digest, result, stamp); err != nil {
		return MutationResult{}, err
	}
	if err = tx.Commit(); err != nil {
		return MutationResult{}, localError(err)
	}
	runCommitHook()
	return result, nil
}

func pruneRequestDigest(in PruneInput) [32]byte {
	return digestValue(struct {
		Domain           string
		StageID          string
		ExpectedRevision int64
		ExpectedDigest   [32]byte
		AbandonRecovery  bool
	}{"mm/v2/stage-prune-request/caller-intent/v1", in.StageID, in.ExpectedRevision, in.ExpectedDigest, in.AbandonRecovery})
}

func erasePrunedContent(ctx context.Context, tx *sql.Tx, stamp string) error {
	selection := `SELECT stage_id FROM stage_retention_events WHERE recorded_at=? AND event='pruned'`
	if _, err := tx.ExecContext(ctx, `UPDATE stage_revisions SET body=NULL WHERE stage_id IN (`+selection+`)`, stamp); err != nil {
		return localError(err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM stage_attachments WHERE stage_id IN (`+selection+`)`, stamp); err != nil {
		return localError(err)
	}
	return nil
}

func (s *Store) RetentionEvents(ctx context.Context, stageID string) ([]RetentionEvent, error) {
	if ctx == nil || !bounded(stageID, maxIdentityBytes) {
		return nil, ErrInvalid
	}
	rows, err := s.db.QueryContext(ctx, `SELECT sequence,stage_id,revision,semantic_digest,event,from_lifecycle,from_recovery,recovery_material,policy_seconds,coalesce(request_id,''),recorded_at
		FROM stage_retention_events WHERE stage_id=? ORDER BY sequence`, stageID)
	if err != nil {
		return nil, localError(err)
	}
	defer rows.Close()
	var events []RetentionEvent
	for rows.Next() {
		var event RetentionEvent
		var digest []byte
		var seconds sql.NullInt64
		var stamp string
		if err = rows.Scan(&event.Sequence, &event.StageID, &event.Revision, &digest, &event.Event, &event.FromLifecycle, &event.FromRecovery, &event.RecoveryMaterial, &seconds, &event.RequestID, &stamp); err != nil {
			return nil, localError(err)
		}
		if len(digest) != 32 {
			return nil, localError(errors.New("retention event digest"))
		}
		copy(event.SemanticDigest[:], digest)
		if seconds.Valid {
			value := seconds.Int64
			event.PolicySeconds = &value
		}
		event.RecordedAt, err = parseTime(stamp)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	if err = rows.Err(); err != nil {
		return nil, localError(err)
	}
	return events, nil
}
