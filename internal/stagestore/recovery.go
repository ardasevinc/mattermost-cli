package stagestore

import (
	"context"
	"time"
)

type RecoveryReport struct {
	Released, Finalized, ForcedUnknown, Partial int
}

// recoverInterruptedApplies classifies every claim left by a process that no
// longer owns the store lock. It never infers non-dispatch from elapsed time.
func (s *Store) recoverInterruptedApplies(ctx context.Context) (RecoveryReport, error) {
	if ctx == nil {
		return RecoveryReport{}, ErrInvalid
	}
	rows, err := s.db.QueryContext(ctx, `SELECT a.id FROM apply_attempts a JOIN stages s ON s.claim_attempt_id=a.id WHERE a.outcome IS NULL AND s.lifecycle='applying' ORDER BY a.started_at,a.id`)
	if err != nil {
		return RecoveryReport{}, localError(err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return RecoveryReport{}, localError(err)
		}
		ids = append(ids, id)
	}
	if err = rows.Close(); err != nil {
		return RecoveryReport{}, localError(err)
	}
	var report RecoveryReport
	for _, id := range ids {
		attempt, scanErr := scanApplyAttempt(ctx, s.db, id)
		if scanErr != nil {
			return report, scanErr
		}
		dispatched, stopped, effects, terminal := false, false, false, true
		for _, step := range attempt.Steps {
			switch step.State {
			case StepDispatch:
				dispatched = true
				terminal = false
			case StepPending:
				terminal = false
			case StepValidated, StepSkipped:
				effects = true
			case StepRejected, StepUnknown:
				stopped = true
			}
		}
		if dispatched {
			for _, step := range attempt.Steps {
				if step.State == StepDispatch {
					if err = s.MarkStepUnknown(ctx, id, step.Ordinal); err != nil {
						return report, err
					}
				}
			}
			if _, err = s.FinalizeApply(ctx, id); err != nil {
				return report, err
			}
			report.Finalized++
			report.ForcedUnknown++
			continue
		}
		if stopped {
			receipt, finalizeErr := s.FinalizeApply(ctx, id)
			if finalizeErr != nil {
				return report, finalizeErr
			}
			report.Finalized++
			if receipt.Outcome == OutcomeUnknown {
				report.ForcedUnknown++
			} else if receipt.Outcome == OutcomePartial {
				report.Partial++
			}
			continue
		}
		if !effects && !terminal {
			if err = s.AbandonApplyBeforeDispatch(ctx, id); err != nil {
				return report, err
			}
			report.Released++
			continue
		}
		if effects && !terminal {
			if err = s.sealInterruptedPending(ctx, id); err != nil {
				return report, err
			}
			report.Partial++
		}
		if _, err = s.FinalizeApply(ctx, id); err != nil {
			return report, err
		}
		report.Finalized++
	}
	return report, nil
}

func (s *Store) sealInterruptedPending(ctx context.Context, attemptID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return localError(err)
	}
	defer tx.Rollback()
	attempt, err := scanApplyAttempt(ctx, tx, attemptID)
	if err != nil {
		return err
	}
	for _, step := range attempt.Steps {
		if step.State == StepDispatch {
			return ErrNotEligible
		}
	}
	stamp := formatTime(time.Now().UTC())
	if err = sealPendingSteps(ctx, tx, &attempt, stamp, "recovered_partial"); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return localError(err)
	}
	runCommitHook()
	return nil
}

func applyJournalAvailable() bool {
	return len(migrations) >= 6 && migrations[5].version == 6 && migrations[5].name == "durable-apply-journal"
}
