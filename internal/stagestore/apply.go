package stagestore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"time"
)

type RecoveryMode string
type AttemptOutcome string
type StepState string

const (
	RecoveryModeOrdinary RecoveryMode = "ordinary"
	RecoveryModePartial  RecoveryMode = "resume_partial"
	RecoveryModeUnknown  RecoveryMode = "force_unknown"

	OutcomeSucceeded        AttemptOutcome = "succeeded"
	OutcomeAlreadySatisfied AttemptOutcome = "already_satisfied"
	OutcomeRejected         AttemptOutcome = "rejected"
	OutcomePartial          AttemptOutcome = "partial"
	OutcomeUnknown          AttemptOutcome = "unknown"

	StepPending   StepState = "pending"
	StepDispatch  StepState = "dispatch_intent"
	StepValidated StepState = "response_validated"
	StepRejected  StepState = "rejected"
	StepUnknown   StepState = "outcome_unknown"
	StepSkipped   StepState = "skipped"
	StepNotSent   StepState = "not_dispatched"
)

type ApplyClaimInput struct {
	StageID, RequestID string
	Revision           int64
	ExpectedDigest     [32]byte
	RequestDigest      [32]byte
	RecoveryMode       RecoveryMode
}

type ApplyStep struct {
	Ordinal   int             `json:"ordinal"`
	Kind      string          `json:"kind"`
	Condition string          `json:"condition"`
	State     StepState       `json:"state"`
	Result    json.RawMessage `json:"result"`
	StartedAt *time.Time      `json:"startedAt"`
	EndedAt   *time.Time      `json:"endedAt"`
}

type ApplyAttempt struct {
	ID                  string          `json:"id"`
	StageID             string          `json:"stageId"`
	Revision            int64           `json:"revision"`
	SemanticDigest      [32]byte        `json:"semanticDigest"`
	RecoveryMode        RecoveryMode    `json:"recoveryMode"`
	PriorRecovery       Recovery        `json:"priorRecovery"`
	ForcedDuplicateRisk bool            `json:"forcedDuplicateRisk"`
	Plan                json.RawMessage `json:"plan"`
	PendingPostID       string          `json:"pendingPostId"`
	StartedAt           time.Time       `json:"startedAt"`
	EndedAt             *time.Time      `json:"endedAt"`
	Outcome             *AttemptOutcome `json:"outcome"`
	Steps               []ApplyStep     `json:"steps"`
	Replay              bool            `json:"-"`
}

type ApplyReceipt struct {
	Schema              string          `json:"schema"`
	AttemptID           string          `json:"attemptId"`
	StageID             string          `json:"stageId"`
	Revision            int64           `json:"revision"`
	SemanticDigest      string          `json:"semanticDigest"`
	Operation           Operation       `json:"operation"`
	RecoveryMode        RecoveryMode    `json:"recoveryMode"`
	ForcedDuplicateRisk bool            `json:"forcedDuplicateRisk"`
	Destination         json.RawMessage `json:"destination"`
	Outcome             AttemptOutcome  `json:"outcome"`
	Recovery            Recovery        `json:"recovery"`
	StartedAt           time.Time       `json:"startedAt"`
	RecordedAt          time.Time       `json:"recordedAt"`
	Steps               []ApplyStep     `json:"steps"`
	Replay              bool            `json:"-"`
}

type persistedPlan struct {
	Steps []struct {
		Ordinal   int    `json:"ordinal"`
		Type      string `json:"type"`
		Condition string `json:"condition"`
	} `json:"steps"`
}

func (s *Store) ClaimApply(ctx context.Context, in ApplyClaimInput) (ApplyAttempt, error) {
	if ctx == nil || !bounded(in.StageID, maxIdentityBytes) || in.Revision < 1 || !validRecoveryMode(in.RecoveryMode) || !validRequestID(in.RequestID) || in.ExpectedDigest == ([32]byte{}) || in.RequestID != "" && in.RequestDigest == ([32]byte{}) {
		return ApplyAttempt{}, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return ApplyAttempt{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ApplyAttempt{}, localError(err)
	}
	defer tx.Rollback()
	base, err := scanCurrent(ctx, tx, in.StageID)
	if err != nil {
		return ApplyAttempt{}, err
	}
	if in.RequestID != "" {
		attempt, found, findErr := findApplyRequest(ctx, tx, base.ServerURL, base.UserID, in.RequestID, in.RequestDigest)
		if findErr != nil {
			return ApplyAttempt{}, findErr
		}
		if found {
			if attempt.StageID != in.StageID || attempt.Revision != in.Revision || attempt.SemanticDigest != in.ExpectedDigest || attempt.RecoveryMode != in.RecoveryMode {
				return ApplyAttempt{}, ErrConflict
			}
			attempt.Replay = true
			return attempt, nil
		}
	}
	if base.Revision != in.Revision || base.SemanticDigest != in.ExpectedDigest {
		return ApplyAttempt{}, ErrConflict
	}
	if base.Lifecycle != LifecycleOpen || !modeMatchesRecovery(in.RecoveryMode, base.Recovery) {
		return ApplyAttempt{}, ErrNotEligible
	}
	// Partial resume needs step input digests plus explicit remote revalidation.
	// Refuse it until that proof is part of the claim instead of redispatching a
	// previously confirmed effect from ordinal coincidence alone.
	if in.RecoveryMode == RecoveryModePartial {
		return ApplyAttempt{}, ErrNotEligible
	}
	plan, steps, err := decodePersistedPlan(base.Plan)
	if err != nil {
		return ApplyAttempt{}, localError(err)
	}
	attachments, err := readAttachments(ctx, tx, base.ID, base.Revision)
	if err != nil {
		return ApplyAttempt{}, err
	}
	if !validPlanForOperation(base.Operation, steps, len(attachments)) {
		return ApplyAttempt{}, localError(errors.New("stored apply plan"))
	}
	attemptID, err := newIdentity("att_")
	if err != nil {
		return ApplyAttempt{}, errors.New("stage store: random identity unavailable")
	}
	pendingID, err := newIdentity("pending_")
	if err != nil {
		return ApplyAttempt{}, errors.New("stage store: random identity unavailable")
	}
	now := time.Now().UTC()
	stamp := formatTime(now)
	forced := in.RecoveryMode == RecoveryModeUnknown
	if _, err = tx.ExecContext(ctx, `INSERT INTO apply_attempts(id,stage_id,revision,semantic_digest,recovery_mode,prior_recovery,forced_duplicate_risk,plan_json,pending_post_id,started_at) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		attemptID, base.ID, base.Revision, base.SemanticDigest[:], in.RecoveryMode, base.Recovery, boolInt(forced), string(plan), pendingID, stamp); err != nil {
		return ApplyAttempt{}, localError(err)
	}
	for _, step := range steps {
		if _, err = tx.ExecContext(ctx, `INSERT INTO apply_steps(attempt_id,ordinal,kind,condition,state) VALUES(?,?,?,?,'pending')`, attemptID, step.Ordinal, step.Kind, step.Condition); err != nil {
			return ApplyAttempt{}, localError(err)
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO apply_events(attempt_id,event,recorded_at) VALUES(?,'claimed',?)`, attemptID, stamp); err != nil {
		return ApplyAttempt{}, localError(err)
	}
	if in.RequestID != "" {
		if _, err = tx.ExecContext(ctx, `INSERT INTO apply_requests(server_url,user_id,request_id,request_digest,attempt_id,created_at) VALUES(?,?,?,?,?,?)`, base.ServerURL, base.UserID, in.RequestID, in.RequestDigest[:], attemptID, stamp); err != nil {
			return ApplyAttempt{}, localError(err)
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE stages SET lifecycle='applying',claim_attempt_id=?,updated_at=? WHERE id=? AND current_revision=? AND lifecycle='open' AND recovery=? AND claim_attempt_id IS NULL`, attemptID, stamp, base.ID, base.Revision, base.Recovery)
	if err != nil {
		return ApplyAttempt{}, localError(err)
	}
	if !oneRow(result) {
		return ApplyAttempt{}, ErrConflict
	}
	if err = tx.Commit(); err != nil {
		return ApplyAttempt{}, localError(err)
	}
	runCommitHook()
	return ApplyAttempt{ID: attemptID, StageID: base.ID, Revision: base.Revision, SemanticDigest: base.SemanticDigest, RecoveryMode: in.RecoveryMode, PriorRecovery: base.Recovery,
		ForcedDuplicateRisk: forced, Plan: plan, PendingPostID: pendingID, StartedAt: now, Steps: steps}, nil
}

func (s *Store) BeginDispatch(ctx context.Context, attemptID string, ordinal int) error {
	return s.transitionStep(ctx, attemptID, ordinal, StepPending, StepDispatch, nil)
}

func (s *Store) MarkStepValidated(ctx context.Context, attemptID string, ordinal int, result json.RawMessage) error {
	return s.transitionStep(ctx, attemptID, ordinal, StepDispatch, StepValidated, result)
}

func (s *Store) MarkStepRejected(ctx context.Context, attemptID string, ordinal int, result json.RawMessage) error {
	return s.transitionStep(ctx, attemptID, ordinal, StepDispatch, StepRejected, result)
}

func (s *Store) MarkStepUnknown(ctx context.Context, attemptID string, ordinal int) error {
	return s.transitionStep(ctx, attemptID, ordinal, StepDispatch, StepUnknown, nil)
}

func (s *Store) MarkStepSkipped(ctx context.Context, attemptID string, ordinal int, result json.RawMessage) error {
	return s.transitionStep(ctx, attemptID, ordinal, StepPending, StepSkipped, result)
}

func (s *Store) transitionStep(ctx context.Context, attemptID string, ordinal int, from, to StepState, result json.RawMessage) error {
	if ctx == nil || !bounded(attemptID, maxIdentityBytes) || ordinal < 1 || !validStepTransition(from, to) {
		return ErrInvalid
	}
	if result != nil {
		canonical, err := canonicalStepResult(ctx, s.db, attemptID, ordinal, to, result)
		if err != nil {
			return ErrInvalid
		}
		result = canonical
	}
	if (to == StepValidated || to == StepRejected || to == StepSkipped) != (result != nil) {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return localError(err)
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	stamp := formatTime(now)
	var started, ended any
	if to == StepDispatch {
		started = stamp
	}
	if to != StepDispatch {
		ended = stamp
	}
	if from == StepPending {
		var blocked int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM apply_steps current WHERE current.attempt_id=? AND current.ordinal=? AND (
EXISTS(SELECT 1 FROM apply_steps prior WHERE prior.attempt_id=current.attempt_id AND prior.ordinal<current.ordinal AND prior.state NOT IN ('response_validated','skipped'))
OR EXISTS(SELECT 1 FROM apply_steps later WHERE later.attempt_id=current.attempt_id AND later.ordinal>current.ordinal AND later.state!='pending'))`, attemptID, ordinal).Scan(&blocked); err != nil {
			return localError(err)
		}
		if blocked != 0 {
			return ErrNotEligible
		}
		if to == StepSkipped {
			var kind, condition string
			if err = tx.QueryRowContext(ctx, `SELECT kind,condition FROM apply_steps WHERE attempt_id=? AND ordinal=?`, attemptID, ordinal).Scan(&kind, &condition); err != nil {
				return localError(err)
			}
			if condition != "if_missing" && !(kind == "edit_post" && condition == "always") {
				return ErrNotEligible
			}
		}
	}
	res, err := tx.ExecContext(ctx, `UPDATE apply_steps SET state=?,result_json=?,started_at=coalesce(started_at,?),ended_at=? WHERE attempt_id=? AND ordinal=? AND state=? AND EXISTS(
SELECT 1 FROM stages s JOIN apply_attempts a ON a.id=? WHERE s.id=a.stage_id AND s.lifecycle='applying' AND s.claim_attempt_id=a.id AND a.outcome IS NULL)`,
		to, nullableRaw(result), started, ended, attemptID, ordinal, from, attemptID)
	if err != nil {
		return localError(err)
	}
	if !oneRow(res) {
		return ErrConflict
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO apply_events(attempt_id,ordinal,event,recorded_at) VALUES(?,?,?,?)`, attemptID, ordinal, to, stamp); err != nil {
		return localError(err)
	}
	if err = tx.Commit(); err != nil {
		return localError(err)
	}
	runCommitHook()
	return nil
}

func (s *Store) FinalizeApply(ctx context.Context, attemptID string) (ApplyReceipt, error) {
	if ctx == nil || !bounded(attemptID, maxIdentityBytes) {
		return ApplyReceipt{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ApplyReceipt{}, localError(err)
	}
	defer tx.Rollback()
	attempt, err := scanApplyAttempt(ctx, tx, attemptID)
	if err != nil {
		return ApplyReceipt{}, err
	}
	if attempt.Outcome != nil {
		receipt, receiptErr := loadApplyReceipt(ctx, tx, attemptID)
		if receiptErr != nil {
			return ApplyReceipt{}, receiptErr
		}
		var operation Operation
		var destination string
		if receiptErr = tx.QueryRowContext(ctx, `SELECT s.operation,r.destination_json FROM stages s JOIN stage_revisions r ON r.stage_id=s.id AND r.revision=? WHERE s.id=?`, attempt.Revision, attempt.StageID).Scan(&operation, &destination); receiptErr != nil {
			return ApplyReceipt{}, localError(receiptErr)
		}
		if !validReceiptForAttempt(receipt, attempt) || receipt.Operation != operation || !bytes.Equal(receipt.Destination, []byte(destination)) {
			return ApplyReceipt{}, localError(errors.New("apply receipt binding"))
		}
		receipt.Replay = true
		return receipt, nil
	}
	if hasStoppedStep(attempt.Steps) {
		if err = sealPendingSteps(ctx, tx, &attempt, formatTime(time.Now().UTC()), "not_dispatched"); err != nil {
			return ApplyReceipt{}, err
		}
	}
	outcome, recovery, lifecycle, err := deriveAttemptResult(attempt)
	if err != nil {
		return ApplyReceipt{}, err
	}
	var operation Operation
	var destination string
	var currentRevision int64
	var claimed sql.NullString
	if err = tx.QueryRowContext(ctx, `SELECT s.operation,r.destination_json,s.current_revision,s.claim_attempt_id FROM stages s JOIN stage_revisions r ON r.stage_id=s.id AND r.revision=a.revision JOIN apply_attempts a ON a.stage_id=s.id WHERE a.id=?`, attemptID).Scan(&operation, &destination, &currentRevision, &claimed); err != nil {
		return ApplyReceipt{}, localError(err)
	}
	if currentRevision != attempt.Revision || !claimed.Valid || claimed.String != attempt.ID {
		return ApplyReceipt{}, ErrConflict
	}
	now := time.Now().UTC()
	stamp := formatTime(now)
	if _, err = tx.ExecContext(ctx, `UPDATE apply_attempts SET outcome=?,ended_at=? WHERE id=? AND outcome IS NULL`, outcome, stamp, attemptID); err != nil {
		return ApplyReceipt{}, localError(err)
	}
	res, err := tx.ExecContext(ctx, `UPDATE stages SET lifecycle=?,recovery=?,claim_attempt_id=NULL,updated_at=? WHERE id=? AND lifecycle='applying' AND claim_attempt_id=?`, lifecycle, recovery, stamp, attempt.StageID, attemptID)
	if err != nil {
		return ApplyReceipt{}, localError(err)
	}
	if !oneRow(res) {
		return ApplyReceipt{}, ErrConflict
	}
	if lifecycle == LifecycleCompleted {
		if _, err = tx.ExecContext(ctx, `UPDATE stage_revisions SET body=NULL WHERE stage_id=?`, attempt.StageID); err != nil {
			return ApplyReceipt{}, localError(err)
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM stage_attachments WHERE stage_id=?`, attempt.StageID); err != nil {
			return ApplyReceipt{}, localError(err)
		}
	}
	attempt.Outcome, attempt.EndedAt = &outcome, &now
	receipt := ApplyReceipt{"mm/v2/apply-receipt", attempt.ID, attempt.StageID, attempt.Revision, hex.EncodeToString(attempt.SemanticDigest[:]), operation, attempt.RecoveryMode, attempt.ForcedDuplicateRisk, json.RawMessage(destination), outcome, recovery, attempt.StartedAt, now, attempt.Steps, false}
	raw, err := marshalCanonical(receipt)
	if err != nil {
		return ApplyReceipt{}, localError(err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO apply_receipts(attempt_id,receipt_json,recorded_at) VALUES(?,?,?)`, attemptID, string(raw), stamp); err != nil {
		return ApplyReceipt{}, localError(err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO apply_events(attempt_id,event,recorded_at) VALUES(?,'completed',?)`, attemptID, stamp); err != nil {
		return ApplyReceipt{}, localError(err)
	}
	if err = tx.Commit(); err != nil {
		return ApplyReceipt{}, localError(err)
	}
	runCommitHook()
	return receipt, nil
}

func (s *Store) AbandonApplyBeforeDispatch(ctx context.Context, attemptID string) error {
	if ctx == nil || !bounded(attemptID, maxIdentityBytes) {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return localError(err)
	}
	defer tx.Rollback()
	var stageID string
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT a.stage_id,count(CASE WHEN p.state!='pending' THEN 1 END) FROM apply_attempts a JOIN apply_steps p ON p.attempt_id=a.id WHERE a.id=? AND a.outcome IS NULL GROUP BY a.stage_id`, attemptID).Scan(&stageID, &count); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return localError(err)
	}
	if count != 0 {
		return ErrNotEligible
	}
	stamp := formatTime(time.Now().UTC())
	res, err := tx.ExecContext(ctx, `UPDATE stages SET lifecycle='open',claim_attempt_id=NULL,updated_at=? WHERE id=? AND lifecycle='applying' AND claim_attempt_id=?`, stamp, stageID, attemptID)
	if err != nil {
		return localError(err)
	}
	if !oneRow(res) {
		return ErrConflict
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM apply_steps WHERE attempt_id=?`, attemptID); err != nil {
		return localError(err)
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM apply_events WHERE attempt_id=?`, attemptID); err != nil {
		return localError(err)
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM apply_requests WHERE attempt_id=?`, attemptID); err != nil {
		return localError(err)
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM apply_attempts WHERE id=?`, attemptID); err != nil {
		return localError(err)
	}
	if err = tx.Commit(); err != nil {
		return localError(err)
	}
	runCommitHook()
	return nil
}

func (s *Store) FindApply(ctx context.Context, server, user, requestID string, requestDigest [32]byte) (ApplyAttempt, bool, error) {
	if ctx == nil || !canonicalServerURL(server) || !bounded(user, maxIdentityBytes) || !validRequestID(requestID) || requestID == "" || requestDigest == ([32]byte{}) {
		return ApplyAttempt{}, false, ErrInvalid
	}
	attempt, found, err := findApplyRequest(ctx, s.db, server, user, requestID, requestDigest)
	if found {
		attempt.Replay = true
	}
	return attempt, found, err
}

func findApplyRequest(ctx context.Context, q queryer, server, user, requestID string, requestDigest [32]byte) (ApplyAttempt, bool, error) {
	var storedDigest []byte
	var attemptID string
	err := q.QueryRowContext(ctx, `SELECT request_digest,attempt_id FROM apply_requests WHERE server_url=? AND user_id=? AND request_id=?`, server, user, requestID).Scan(&storedDigest, &attemptID)
	if errors.Is(err, sql.ErrNoRows) {
		return ApplyAttempt{}, false, nil
	}
	if err != nil {
		return ApplyAttempt{}, false, localError(err)
	}
	if len(storedDigest) != sha256.Size || !bytes.Equal(storedDigest, requestDigest[:]) {
		return ApplyAttempt{}, false, ErrConflict
	}
	attempt, err := scanApplyAttempt(ctx, q, attemptID)
	return attempt, err == nil, err
}

func scanApplyAttempt(ctx context.Context, q queryer, attemptID string) (ApplyAttempt, error) {
	var a ApplyAttempt
	var plan, started string
	var ended, outcome sql.NullString
	var forced int
	var semantic []byte
	err := q.QueryRowContext(ctx, `SELECT id,stage_id,revision,semantic_digest,recovery_mode,prior_recovery,forced_duplicate_risk,plan_json,pending_post_id,started_at,ended_at,outcome FROM apply_attempts WHERE id=?`, attemptID).
		Scan(&a.ID, &a.StageID, &a.Revision, &semantic, &a.RecoveryMode, &a.PriorRecovery, &forced, &plan, &a.PendingPostID, &started, &ended, &outcome)
	if errors.Is(err, sql.ErrNoRows) {
		return ApplyAttempt{}, ErrNotFound
	}
	if err != nil {
		return ApplyAttempt{}, localError(err)
	}
	a.ForcedDuplicateRisk = forced == 1
	if len(semantic) != sha256.Size {
		return ApplyAttempt{}, localError(errors.New("attempt semantic digest"))
	}
	copy(a.SemanticDigest[:], semantic)
	a.Plan = json.RawMessage(plan)
	if a.StartedAt, err = parseTime(started); err != nil {
		return ApplyAttempt{}, err
	}
	if ended.Valid {
		value, parseErr := parseTime(ended.String)
		if parseErr != nil {
			return ApplyAttempt{}, parseErr
		}
		a.EndedAt = &value
	}
	if outcome.Valid {
		value := AttemptOutcome(outcome.String)
		if !validOutcome(value) {
			return ApplyAttempt{}, localError(errors.New("attempt outcome"))
		}
		a.Outcome = &value
	}
	rows, err := q.QueryContext(ctx, `SELECT ordinal,kind,condition,state,result_json,started_at,ended_at FROM apply_steps WHERE attempt_id=? ORDER BY ordinal`, attemptID)
	if err != nil {
		return ApplyAttempt{}, localError(err)
	}
	for rows.Next() {
		var step ApplyStep
		var result, stepStarted, stepEnded sql.NullString
		if err = rows.Scan(&step.Ordinal, &step.Kind, &step.Condition, &step.State, &result, &stepStarted, &stepEnded); err != nil {
			return ApplyAttempt{}, localError(err)
		}
		if result.Valid {
			step.Result = json.RawMessage(result.String)
		}
		if step.StartedAt, err = parseOptionalTime(stepStarted); err != nil {
			return ApplyAttempt{}, err
		}
		if step.EndedAt, err = parseOptionalTime(stepEnded); err != nil {
			return ApplyAttempt{}, err
		}
		a.Steps = append(a.Steps, step)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return ApplyAttempt{}, localError(errors.New("apply attempt projection"))
	}
	if err = rows.Close(); err != nil || !validAttempt(a) {
		return ApplyAttempt{}, localError(errors.New("apply attempt projection"))
	}
	for _, step := range a.Steps {
		if step.State != StepValidated && step.State != StepRejected && step.State != StepSkipped {
			continue
		}
		canonical, validationErr := canonicalStepResult(ctx, q, a.ID, step.Ordinal, step.State, step.Result)
		if validationErr != nil || !bytes.Equal(canonical, step.Result) {
			return ApplyAttempt{}, localError(errors.New("apply step result binding"))
		}
	}
	return a, nil
}

func loadApplyReceipt(ctx context.Context, q queryer, attemptID string) (ApplyReceipt, error) {
	var raw, recorded string
	if err := q.QueryRowContext(ctx, `SELECT receipt_json,recorded_at FROM apply_receipts WHERE attempt_id=?`, attemptID).Scan(&raw, &recorded); err != nil {
		return ApplyReceipt{}, localError(err)
	}
	var receipt ApplyReceipt
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&receipt) != nil || decoder.Decode(new(any)) != io.EOF || receipt.Schema != "mm/v2/apply-receipt" || receipt.AttemptID != attemptID {
		return ApplyReceipt{}, localError(errors.New("apply receipt projection"))
	}
	for i := range receipt.Steps {
		if bytes.Equal(receipt.Steps[i].Result, []byte("null")) {
			receipt.Steps[i].Result = nil
		}
	}
	want, err := parseTime(recorded)
	if err != nil || !receipt.RecordedAt.Equal(want) {
		return ApplyReceipt{}, localError(errors.New("apply receipt timestamp"))
	}
	return receipt, nil
}

func decodePersistedPlan(raw json.RawMessage) (json.RawMessage, []ApplyStep, error) {
	canonical, err := canonicalObject(raw)
	if err != nil {
		return nil, nil, err
	}
	var plan persistedPlan
	decoder := json.NewDecoder(bytes.NewReader(canonical))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&plan) != nil || decoder.Decode(new(any)) != io.EOF || len(plan.Steps) == 0 || len(plan.Steps) > maxAttachments+2 {
		return nil, nil, ErrInvalid
	}
	steps := make([]ApplyStep, len(plan.Steps))
	for i, input := range plan.Steps {
		if input.Ordinal != i+1 || !validStepKind(input.Type) || input.Condition != "always" && input.Condition != "if_missing" {
			return nil, nil, ErrInvalid
		}
		steps[i] = ApplyStep{Ordinal: input.Ordinal, Kind: input.Type, Condition: input.Condition, State: StepPending}
	}
	return canonical, steps, nil
}

func canonicalStepResult(ctx context.Context, q queryer, attemptID string, ordinal int, state StepState, raw json.RawMessage) (json.RawMessage, error) {
	canonical, err := canonicalObject(raw)
	if err != nil {
		return nil, err
	}
	var kind, pendingPostID, userID, destinationRaw string
	if err = q.QueryRowContext(ctx, `SELECT p.kind,a.pending_post_id,s.user_id,r.destination_json
		FROM apply_steps p JOIN apply_attempts a ON a.id=p.attempt_id JOIN stages s ON s.id=a.stage_id
		JOIN stage_revisions r ON r.stage_id=a.stage_id AND r.revision=a.revision
		WHERE p.attempt_id=? AND p.ordinal=?`, attemptID, ordinal).Scan(&kind, &pendingPostID, &userID, &destinationRaw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrInvalid
		}
		return nil, localError(err)
	}
	if state == StepRejected {
		var result struct {
			Status int `json:"status"`
		}
		if decodeNarrow(canonical, &result) != nil || result.Status < 400 || result.Status > 499 {
			return nil, ErrInvalid
		}
		return marshalCanonical(result)
	}
	if state == StepSkipped {
		var result struct {
			Reason string `json:"reason"`
		}
		if decodeNarrow(canonical, &result) != nil || result.Reason != "already_satisfied" {
			return nil, ErrInvalid
		}
		return marshalCanonical(result)
	}
	if state != StepValidated {
		return nil, ErrInvalid
	}
	var destination struct {
		ChannelID      *string  `json:"channelId"`
		PostID         *string  `json:"postId"`
		ParticipantIDs []string `json:"participantIds"`
	}
	if json.Unmarshal([]byte(destinationRaw), &destination) != nil {
		return nil, ErrInvalid
	}
	switch kind {
	case "upload_attachment":
		var result struct {
			FileID string `json:"fileId"`
		}
		if decodeNarrow(canonical, &result) != nil || !validReceiptID(result.FileID) {
			return nil, ErrInvalid
		}
		return marshalCanonical(result)
	case "create_post":
		var result struct {
			PostID        string `json:"postId"`
			CreateAt      int64  `json:"createAt"`
			ChannelID     string `json:"channelId"`
			UserID        string `json:"userId"`
			PendingPostID string `json:"pendingPostId"`
		}
		if decodeNarrow(canonical, &result) != nil || !validReceiptID(result.PostID) || !validRemoteTimestamp(result.CreateAt) || destination.ChannelID == nil || result.ChannelID != *destination.ChannelID || result.UserID != userID || result.PendingPostID != pendingPostID {
			return nil, ErrInvalid
		}
		return marshalCanonical(result)
	case "edit_post":
		var result struct {
			PostID   string `json:"postId"`
			UpdateAt int64  `json:"updateAt"`
		}
		if decodeNarrow(canonical, &result) != nil || !validReceiptID(result.PostID) || !validRemoteTimestamp(result.UpdateAt) || destination.PostID == nil || result.PostID != *destination.PostID {
			return nil, ErrInvalid
		}
		return marshalCanonical(result)
	case "delete_post":
		var result struct {
			PostID string `json:"postId"`
		}
		if decodeNarrow(canonical, &result) != nil || !validReceiptID(result.PostID) || destination.PostID == nil || result.PostID != *destination.PostID {
			return nil, ErrInvalid
		}
		return marshalCanonical(result)
	case "add_reaction", "remove_reaction":
		var result struct {
			PostID string `json:"postId"`
		}
		if decodeNarrow(canonical, &result) != nil || !validReceiptID(result.PostID) || destination.PostID == nil || result.PostID != *destination.PostID {
			return nil, ErrInvalid
		}
		return marshalCanonical(result)
	case "resolve_conversation":
		var result struct {
			ChannelID      string   `json:"channelId"`
			ParticipantIDs []string `json:"participantIds"`
		}
		if decodeNarrow(canonical, &result) != nil || !validReceiptID(result.ChannelID) || destination.ChannelID != nil && result.ChannelID != *destination.ChannelID || !slices.Equal(result.ParticipantIDs, destination.ParticipantIDs) {
			return nil, ErrInvalid
		}
		return marshalCanonical(result)
	default:
		return nil, ErrInvalid
	}
}

func decodeNarrow(raw []byte, out any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return ErrInvalid
	}
	return nil
}

func validReceiptID(value string) bool {
	return boundedMetadata(value, maxIdentityBytes)
}

func validRemoteTimestamp(value int64) bool {
	return value > 0 && value <= 9_007_199_254_740_991
}

func deriveAttemptResult(a ApplyAttempt) (AttemptOutcome, Recovery, Lifecycle, error) {
	validated, skipped, notSent, rejected, unknown, dispatching := 0, 0, 0, 0, 0, 0
	for _, step := range a.Steps {
		switch step.State {
		case StepValidated:
			validated++
		case StepSkipped:
			skipped++
		case StepRejected:
			rejected++
		case StepUnknown:
			unknown++
		case StepNotSent:
			notSent++
		case StepDispatch:
			dispatching++
		case StepPending:
		default:
			return "", "", "", ErrInvalid
		}
	}
	if dispatching > 0 {
		return "", "", "", ErrNotEligible
	}
	if unknown > 0 {
		return OutcomeUnknown, RecoveryUnknown, LifecycleOpen, nil
	}
	if rejected > 0 {
		if rejected != 1 {
			return "", "", "", ErrInvalid
		}
		if validated > 0 {
			return OutcomePartial, maxRecovery(a.PriorRecovery, RecoveryPartial), LifecycleOpen, nil
		}
		return OutcomeRejected, a.PriorRecovery, LifecycleOpen, nil
	}
	if notSent > 0 {
		if validated+skipped == 0 {
			return OutcomeRejected, a.PriorRecovery, LifecycleOpen, nil
		}
		return OutcomePartial, maxRecovery(a.PriorRecovery, RecoveryPartial), LifecycleOpen, nil
	}
	if validated+skipped != len(a.Steps) {
		return "", "", "", ErrNotEligible
	}
	if validated == 0 {
		return OutcomeAlreadySatisfied, RecoveryForbidden, LifecycleCompleted, nil
	}
	return OutcomeSucceeded, RecoveryForbidden, LifecycleCompleted, nil
}

func validAttempt(a ApplyAttempt) bool {
	if !bounded(a.ID, maxIdentityBytes) || !bounded(a.StageID, maxIdentityBytes) || a.Revision < 1 || a.SemanticDigest == ([32]byte{}) || !validRecoveryMode(a.RecoveryMode) || !validRecovery(a.PriorRecovery) || a.PriorRecovery == RecoveryForbidden ||
		len(a.Steps) == 0 || a.StartedAt.IsZero() || !bounded(a.PendingPostID, maxIdentityBytes) || a.ForcedDuplicateRisk != (a.RecoveryMode == RecoveryModeUnknown) || !modeMatchesRecovery(a.RecoveryMode, a.PriorRecovery) ||
		(a.Outcome == nil) != (a.EndedAt == nil) || a.EndedAt != nil && a.EndedAt.Before(a.StartedAt) {
		return false
	}
	plan, planned, err := decodePersistedPlan(a.Plan)
	if err != nil || !bytes.Equal(plan, a.Plan) || len(planned) != len(a.Steps) {
		return false
	}
	for i, step := range a.Steps {
		if step.Ordinal != i+1 || step.Kind != planned[i].Kind || step.Condition != planned[i].Condition || !validApplyStep(step) {
			return false
		}
	}
	return true
}

func validApplyStep(step ApplyStep) bool {
	if !validStepKind(step.Kind) || step.Condition != "always" && step.Condition != "if_missing" || !validStepState(step.State) {
		return false
	}
	switch step.State {
	case StepPending:
		return step.StartedAt == nil && step.EndedAt == nil && step.Result == nil
	case StepDispatch:
		return step.StartedAt != nil && step.EndedAt == nil && step.Result == nil
	case StepValidated, StepRejected:
		return step.StartedAt != nil && step.EndedAt != nil && step.Result != nil && !step.EndedAt.Before(*step.StartedAt)
	case StepSkipped:
		return (step.Condition == "if_missing" || step.Kind == "edit_post" && step.Condition == "always") && step.StartedAt == nil && step.EndedAt != nil && step.Result != nil
	case StepUnknown:
		return step.StartedAt != nil && step.EndedAt != nil && step.Result == nil && !step.EndedAt.Before(*step.StartedAt)
	case StepNotSent:
		return step.StartedAt == nil && step.EndedAt != nil && step.Result == nil
	}
	return false
}

func validReceiptForAttempt(receipt ApplyReceipt, attempt ApplyAttempt) bool {
	if receipt.Schema != "mm/v2/apply-receipt" || receipt.AttemptID != attempt.ID || receipt.StageID != attempt.StageID || receipt.Revision != attempt.Revision || receipt.SemanticDigest != hex.EncodeToString(attempt.SemanticDigest[:]) || receipt.RecoveryMode != attempt.RecoveryMode || receipt.ForcedDuplicateRisk != attempt.ForcedDuplicateRisk ||
		attempt.Outcome == nil || receipt.Outcome != *attempt.Outcome || attempt.EndedAt == nil || !receipt.StartedAt.Equal(attempt.StartedAt) || !receipt.RecordedAt.Equal(*attempt.EndedAt) ||
		!validOperation(receipt.Operation) || !validRecovery(receipt.Recovery) || len(receipt.Steps) != len(attempt.Steps) {
		return false
	}
	if _, err := canonicalObject(receipt.Destination); err != nil {
		return false
	}
	derivedOutcome, derivedRecovery, _, err := deriveAttemptResult(attempt)
	if err != nil || derivedOutcome != receipt.Outcome || derivedRecovery != receipt.Recovery {
		return false
	}
	for i := range receipt.Steps {
		if receipt.Steps[i].Ordinal != attempt.Steps[i].Ordinal || receipt.Steps[i].Kind != attempt.Steps[i].Kind || receipt.Steps[i].Condition != attempt.Steps[i].Condition || receipt.Steps[i].State != attempt.Steps[i].State ||
			!bytes.Equal(receipt.Steps[i].Result, attempt.Steps[i].Result) || !timePointerEqual(receipt.Steps[i].StartedAt, attempt.Steps[i].StartedAt) || !timePointerEqual(receipt.Steps[i].EndedAt, attempt.Steps[i].EndedAt) {
			return false
		}
	}
	return true
}

func timePointerEqual(a, b *time.Time) bool {
	return a == nil && b == nil || a != nil && b != nil && a.Equal(*b)
}

func validRecoveryMode(mode RecoveryMode) bool {
	return mode == RecoveryModeOrdinary || mode == RecoveryModePartial || mode == RecoveryModeUnknown
}

func modeMatchesRecovery(mode RecoveryMode, recovery Recovery) bool {
	return mode == RecoveryModeOrdinary && recovery == RecoveryNone || mode == RecoveryModePartial && recovery == RecoveryPartial || mode == RecoveryModeUnknown && recovery == RecoveryUnknown
}

func validStepKind(kind string) bool {
	switch kind {
	case "upload_attachment", "create_post", "edit_post", "delete_post", "add_reaction", "remove_reaction", "resolve_conversation":
		return true
	}
	return false
}

func validPlanForOperation(operation Operation, steps []ApplyStep, attachmentCount int) bool {
	single := func(kind, condition string) bool {
		return len(steps) == 1 && steps[0].Kind == kind && steps[0].Condition == condition
	}
	switch operation {
	case CreatePost, Reply:
		if len(steps) != attachmentCount+1 || steps[len(steps)-1].Kind != "create_post" || steps[len(steps)-1].Condition != "always" {
			return false
		}
		for _, step := range steps[:len(steps)-1] {
			if step.Kind != "upload_attachment" || step.Condition != "always" {
				return false
			}
		}
		return true
	case EditPost:
		return attachmentCount == 0 && single("edit_post", "always")
	case DeletePost:
		return attachmentCount == 0 && single("delete_post", "always")
	case React:
		return attachmentCount == 0 && single("add_reaction", "if_missing")
	case Unreact:
		return attachmentCount == 0 && single("remove_reaction", "if_missing")
	case ResolveDM, ResolveGroupDM:
		return attachmentCount == 0 && single("resolve_conversation", "if_missing")
	}
	return false
}

func validStepState(state StepState) bool {
	switch state {
	case StepPending, StepDispatch, StepValidated, StepRejected, StepUnknown, StepSkipped, StepNotSent:
		return true
	}
	return false
}

func validStepTransition(from, to StepState) bool {
	return from == StepPending && (to == StepDispatch || to == StepSkipped) || from == StepDispatch && (to == StepValidated || to == StepRejected || to == StepUnknown)
}

func validOutcome(outcome AttemptOutcome) bool {
	return outcome == OutcomeSucceeded || outcome == OutcomeAlreadySatisfied || outcome == OutcomeRejected || outcome == OutcomePartial || outcome == OutcomeUnknown
}

func maxRecovery(a, b Recovery) Recovery {
	weight := map[Recovery]int{RecoveryNone: 0, RecoveryPartial: 1, RecoveryUnknown: 2, RecoveryForbidden: 3}
	if weight[a] >= weight[b] {
		return a
	}
	return b
}

func parseOptionalTime(raw sql.NullString) (*time.Time, error) {
	if !raw.Valid {
		return nil, nil
	}
	value, err := parseTime(raw.String)
	if err != nil {
		return nil, err
	}
	return &value, nil
}

func nullableRaw(raw json.RawMessage) any {
	if raw == nil {
		return nil
	}
	return string(raw)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func hasStoppedStep(steps []ApplyStep) bool {
	for _, step := range steps {
		if step.State == StepRejected || step.State == StepUnknown {
			return true
		}
	}
	return false
}

func sealPendingSteps(ctx context.Context, tx *sql.Tx, attempt *ApplyAttempt, stamp, event string) error {
	for i := range attempt.Steps {
		if attempt.Steps[i].State != StepPending {
			continue
		}
		result, err := tx.ExecContext(ctx, `UPDATE apply_steps SET state='not_dispatched',ended_at=? WHERE attempt_id=? AND ordinal=? AND state='pending'`, stamp, attempt.ID, attempt.Steps[i].Ordinal)
		if err != nil {
			return localError(err)
		}
		if !oneRow(result) {
			return ErrConflict
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO apply_events(attempt_id,ordinal,event,recorded_at) VALUES(?,?,?,?)`, attempt.ID, attempt.Steps[i].Ordinal, event, stamp); err != nil {
			return localError(err)
		}
		ended, parseErr := parseTime(stamp)
		if parseErr != nil {
			return parseErr
		}
		attempt.Steps[i].State, attempt.Steps[i].EndedAt = StepNotSent, &ended
	}
	return nil
}
