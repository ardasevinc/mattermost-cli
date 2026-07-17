package stagestore

import (
	"crypto/sha256"
	"encoding/hex"
)

type migration struct {
	version int
	name    string
	sql     string
}

func (m migration) checksum() string {
	sum := sha256.Sum256([]byte(m.name + "\x00" + m.sql))
	return hex.EncodeToString(sum[:])
}

var migrations = []migration{{version: 1, name: "core-stage-state", sql: `
CREATE TABLE stages (
  id TEXT PRIMARY KEY NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  operation TEXT NOT NULL CHECK (operation IN ('create_post','reply','edit_post','delete_post','react','unreact','resolve_dm','resolve_group_dm')),
  server_url TEXT NOT NULL,
  server_id TEXT,
  user_id TEXT NOT NULL,
  lifecycle TEXT NOT NULL CHECK (lifecycle IN ('open','applying','completed','canceled','expired','pruned')),
  recovery TEXT NOT NULL CHECK (recovery IN ('none','resume_partial','force_unknown','forbidden')),
  current_revision INTEGER NOT NULL CHECK (current_revision > 0),
  current_revision_state TEXT NOT NULL DEFAULT 'current' CHECK (current_revision_state = 'current'),
  FOREIGN KEY (id, current_revision, current_revision_state) REFERENCES stage_revisions(stage_id, revision, state) DEFERRABLE INITIALLY DEFERRED
) STRICT;
CREATE TABLE stage_revisions (
  stage_id TEXT NOT NULL REFERENCES stages(id) ON DELETE CASCADE,
  revision INTEGER NOT NULL CHECK (revision > 0),
  state TEXT NOT NULL CHECK (state IN ('current','superseded')),
  created_at TEXT NOT NULL,
  semantic_digest BLOB NOT NULL CHECK (length(semantic_digest) = 32),
  body BLOB,
  destination_json TEXT NOT NULL CHECK (json_valid(destination_json)),
  plan_json TEXT NOT NULL CHECK (json_valid(plan_json)),
  PRIMARY KEY (stage_id, revision),
  UNIQUE (stage_id, revision, state)
) STRICT;
CREATE UNIQUE INDEX one_current_stage_revision ON stage_revisions(stage_id) WHERE state = 'current';
CREATE TABLE stage_attachments (
  stage_id TEXT NOT NULL,
  revision INTEGER NOT NULL,
  ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
  supplied_path TEXT NOT NULL,
  canonical_path TEXT NOT NULL,
  remote_filename TEXT NOT NULL,
  byte_length INTEGER NOT NULL CHECK (byte_length >= 0),
  media_type TEXT,
  content_digest BLOB NOT NULL CHECK (length(content_digest) = 32),
  PRIMARY KEY (stage_id, revision, ordinal),
  FOREIGN KEY (stage_id, revision) REFERENCES stage_revisions(stage_id, revision) ON DELETE CASCADE
) STRICT;
CREATE TABLE request_replays (
  server_url TEXT NOT NULL,
  user_id TEXT NOT NULL,
  request_id TEXT NOT NULL,
  request_schema TEXT NOT NULL,
  semantic_digest BLOB NOT NULL CHECK (length(semantic_digest) = 32),
  stage_id TEXT NOT NULL REFERENCES stages(id) ON DELETE CASCADE,
  revision INTEGER NOT NULL CHECK (revision > 0),
  created_at TEXT NOT NULL,
  PRIMARY KEY (server_url, user_id, request_id),
  FOREIGN KEY (stage_id, revision) REFERENCES stage_revisions(stage_id, revision)
) STRICT;
`}, {version: 2, name: "immutable-local-request-receipts", sql: `
CREATE TABLE local_requests (
  server_url TEXT NOT NULL,
  user_id TEXT NOT NULL,
  request_id TEXT NOT NULL,
  request_schema TEXT NOT NULL,
  request_digest BLOB NOT NULL CHECK (length(request_digest) = 32),
  result_json TEXT NOT NULL CHECK (json_valid(result_json)),
  created_at TEXT NOT NULL,
  PRIMARY KEY (server_url, user_id, request_id)
) STRICT;
INSERT INTO local_requests(server_url,user_id,request_id,request_schema,request_digest,result_json,created_at)
SELECT server_url,user_id,request_id,'mm/v2/legacy-request-conflict',semantic_digest,'{}',created_at FROM request_replays;
CREATE TRIGGER local_requests_immutable_update BEFORE UPDATE ON local_requests BEGIN SELECT RAISE(ABORT, 'local request receipts are immutable'); END;
CREATE TRIGGER local_requests_immutable_delete BEFORE DELETE ON local_requests BEGIN SELECT RAISE(ABORT, 'local request receipts are immutable'); END;
CREATE TRIGGER stage_revision_binding_immutable BEFORE INSERT ON stage_revisions
WHEN NEW.revision > 1 AND EXISTS(SELECT 1 FROM stage_revisions WHERE stage_id=NEW.stage_id)
 AND (NEW.destination_json != (SELECT destination_json FROM stage_revisions WHERE stage_id=NEW.stage_id ORDER BY revision LIMIT 1)
   OR NEW.plan_json != (SELECT plan_json FROM stage_revisions WHERE stage_id=NEW.stage_id ORDER BY revision LIMIT 1))
BEGIN SELECT RAISE(ABORT, 'stage destination and plan are immutable'); END;
`}, {version: 3, name: "caller-intent-stage-create-replay", sql: `
DROP TRIGGER local_requests_immutable_update;
UPDATE local_requests SET request_schema='mm/v2/legacy-stage-request-conflict' WHERE request_schema='mm/v2/stage-request';
CREATE TRIGGER local_requests_immutable_update BEFORE UPDATE ON local_requests BEGIN SELECT RAISE(ABORT, 'local request receipts are immutable'); END;
`}, {version: 4, name: "caller-intent-stage-revise-replay", sql: `
DROP TRIGGER local_requests_immutable_update;
UPDATE local_requests SET request_schema='mm/v2/legacy-stage-revise-conflict' WHERE request_schema='mm/v2/stage-revise-request';
CREATE TRIGGER local_requests_immutable_update BEFORE UPDATE ON local_requests BEGIN SELECT RAISE(ABORT, 'local request receipts are immutable'); END;
`}, {version: 5, name: "revision-plan-follows-composition", sql: `
DROP TRIGGER stage_revision_binding_immutable;
CREATE TRIGGER stage_revision_binding_immutable BEFORE INSERT ON stage_revisions
WHEN NEW.revision > 1 AND EXISTS(SELECT 1 FROM stage_revisions WHERE stage_id=NEW.stage_id)
 AND NEW.destination_json != (SELECT destination_json FROM stage_revisions WHERE stage_id=NEW.stage_id ORDER BY revision LIMIT 1)
BEGIN SELECT RAISE(ABORT, 'stage destination is immutable'); END;
`}, {version: 6, name: "durable-apply-journal", sql: `
CREATE TABLE apply_attempts (
  id TEXT PRIMARY KEY NOT NULL,
  stage_id TEXT NOT NULL,
  revision INTEGER NOT NULL CHECK (revision > 0),
  semantic_digest BLOB NOT NULL CHECK (length(semantic_digest) = 32),
  recovery_mode TEXT NOT NULL CHECK (recovery_mode IN ('ordinary','resume_partial','force_unknown')),
  prior_recovery TEXT NOT NULL CHECK (prior_recovery IN ('none','resume_partial','force_unknown')),
  forced_duplicate_risk INTEGER NOT NULL CHECK (forced_duplicate_risk IN (0,1)),
  plan_json TEXT NOT NULL CHECK (json_valid(plan_json)),
  pending_post_id TEXT NOT NULL,
  started_at TEXT NOT NULL,
  ended_at TEXT,
  outcome TEXT CHECK (outcome IN ('succeeded','already_satisfied','rejected','partial','unknown')),
  CHECK ((ended_at IS NULL) = (outcome IS NULL)),
  FOREIGN KEY (stage_id, revision) REFERENCES stage_revisions(stage_id, revision)
) STRICT;
ALTER TABLE stages ADD COLUMN claim_attempt_id TEXT REFERENCES apply_attempts(id);
UPDATE stages SET lifecycle='open',recovery='force_unknown' WHERE lifecycle='applying';
CREATE UNIQUE INDEX one_apply_claim_per_stage ON apply_attempts(stage_id) WHERE outcome IS NULL;
CREATE TABLE apply_steps (
  attempt_id TEXT NOT NULL REFERENCES apply_attempts(id) ON DELETE CASCADE,
  ordinal INTEGER NOT NULL CHECK (ordinal > 0),
  kind TEXT NOT NULL,
  condition TEXT NOT NULL CHECK (condition IN ('always','if_missing')),
  state TEXT NOT NULL CHECK (state IN ('pending','dispatch_intent','response_validated','rejected','outcome_unknown','skipped','not_dispatched')),
  result_json TEXT CHECK (result_json IS NULL OR json_valid(result_json)),
  started_at TEXT,
  ended_at TEXT,
  PRIMARY KEY (attempt_id, ordinal),
  CHECK (
    state='pending' AND result_json IS NULL AND started_at IS NULL AND ended_at IS NULL
    OR state='dispatch_intent' AND result_json IS NULL AND started_at IS NOT NULL AND ended_at IS NULL
    OR state IN ('response_validated','rejected') AND result_json IS NOT NULL AND started_at IS NOT NULL AND ended_at IS NOT NULL
    OR state='outcome_unknown' AND result_json IS NULL AND started_at IS NOT NULL AND ended_at IS NOT NULL
    OR state='skipped' AND condition='if_missing' AND result_json IS NOT NULL AND started_at IS NULL AND ended_at IS NOT NULL
    OR state='not_dispatched' AND result_json IS NULL AND started_at IS NULL AND ended_at IS NOT NULL
  )
) STRICT;
CREATE TABLE apply_events (
  sequence INTEGER PRIMARY KEY AUTOINCREMENT,
  attempt_id TEXT NOT NULL REFERENCES apply_attempts(id) ON DELETE CASCADE,
  ordinal INTEGER,
  event TEXT NOT NULL CHECK (event IN ('claimed','dispatch_intent','response_validated','rejected','outcome_unknown','skipped','not_dispatched','completed','released_before_dispatch','recovered_unknown','recovered_partial')),
  recorded_at TEXT NOT NULL,
  CHECK (ordinal IS NULL OR ordinal > 0)
) STRICT;
CREATE TABLE apply_requests (
  server_url TEXT NOT NULL,
  user_id TEXT NOT NULL,
  request_id TEXT NOT NULL,
  request_digest BLOB NOT NULL CHECK (length(request_digest) = 32),
  attempt_id TEXT NOT NULL REFERENCES apply_attempts(id) ON DELETE CASCADE,
  created_at TEXT NOT NULL,
  PRIMARY KEY (server_url, user_id, request_id),
  UNIQUE (attempt_id)
) STRICT;
CREATE TABLE apply_receipts (
  attempt_id TEXT PRIMARY KEY NOT NULL REFERENCES apply_attempts(id) ON DELETE CASCADE,
  receipt_json TEXT NOT NULL CHECK (json_valid(receipt_json)),
  recorded_at TEXT NOT NULL
) STRICT;
CREATE TRIGGER stage_apply_claim_valid BEFORE UPDATE OF lifecycle,claim_attempt_id ON stages
WHEN (NEW.lifecycle='applying' AND (NEW.claim_attempt_id IS NULL OR NOT EXISTS(
  SELECT 1 FROM apply_attempts a WHERE a.id=NEW.claim_attempt_id AND a.stage_id=NEW.id AND a.revision=NEW.current_revision AND a.outcome IS NULL
))) OR (NEW.lifecycle!='applying' AND NEW.claim_attempt_id IS NOT NULL)
BEGIN SELECT RAISE(ABORT, 'invalid stage apply claim'); END;
CREATE TRIGGER stage_apply_claim_release_valid BEFORE UPDATE OF lifecycle,claim_attempt_id ON stages
WHEN OLD.lifecycle='applying' AND NEW.lifecycle!='applying' AND EXISTS(
  SELECT 1 FROM apply_attempts a WHERE a.id=OLD.claim_attempt_id AND a.outcome IS NULL
    AND EXISTS(SELECT 1 FROM apply_steps p WHERE p.attempt_id=a.id AND p.state!='pending')
)
BEGIN SELECT RAISE(ABORT, 'dispatched apply claim cannot be released'); END;
CREATE TRIGGER stage_lifecycle_recovery_transition_valid BEFORE UPDATE OF lifecycle,recovery ON stages
WHEN (NEW.lifecycle IS NOT OLD.lifecycle OR NEW.recovery IS NOT OLD.recovery) AND NOT (
  OLD.lifecycle='open' AND NEW.lifecycle='applying' AND NEW.recovery=OLD.recovery AND NEW.claim_attempt_id IS NOT NULL
    AND EXISTS(SELECT 1 FROM apply_attempts a WHERE a.id=NEW.claim_attempt_id AND a.stage_id=OLD.id AND a.outcome IS NULL)
  OR OLD.lifecycle='applying' AND NEW.lifecycle='open' AND NEW.recovery=OLD.recovery AND NEW.claim_attempt_id IS NULL
    AND EXISTS(SELECT 1 FROM apply_attempts a WHERE a.id=OLD.claim_attempt_id AND a.outcome IS NULL)
    AND NOT EXISTS(SELECT 1 FROM apply_steps p WHERE p.attempt_id=OLD.claim_attempt_id AND p.state!='pending')
  OR OLD.lifecycle='applying' AND NEW.claim_attempt_id IS NULL AND EXISTS(
    SELECT 1 FROM apply_attempts a WHERE a.id=OLD.claim_attempt_id AND a.outcome IS NOT NULL AND (
      a.outcome IN ('succeeded','already_satisfied') AND NEW.lifecycle='completed' AND NEW.recovery='forbidden'
      OR a.outcome='rejected' AND NEW.lifecycle='open' AND NEW.recovery=a.prior_recovery
      OR a.outcome='partial' AND NEW.lifecycle='open' AND NEW.recovery=CASE WHEN a.prior_recovery='force_unknown' THEN 'force_unknown' ELSE 'resume_partial' END
      OR a.outcome='unknown' AND NEW.lifecycle='open' AND NEW.recovery='force_unknown'
    )
  )
  OR OLD.lifecycle='open' AND NEW.lifecycle='canceled' AND NEW.recovery='forbidden' AND OLD.claim_attempt_id IS NULL AND NEW.claim_attempt_id IS NULL
  OR OLD.lifecycle='open' AND OLD.recovery='none' AND NEW.lifecycle='expired' AND NEW.recovery='forbidden' AND OLD.claim_attempt_id IS NULL AND NEW.claim_attempt_id IS NULL
  OR OLD.lifecycle='expired' AND OLD.recovery='forbidden' AND NEW.lifecycle='open' AND NEW.recovery='none'
    AND OLD.claim_attempt_id IS NULL AND NEW.claim_attempt_id IS NULL AND NEW.current_revision=OLD.current_revision+1
    AND EXISTS(SELECT 1 FROM stage_revisions r WHERE r.stage_id=OLD.id AND r.revision=OLD.current_revision AND r.state='superseded')
    AND EXISTS(SELECT 1 FROM stage_revisions r WHERE r.stage_id=OLD.id AND r.revision=NEW.current_revision AND r.state='current')
)
BEGIN SELECT RAISE(ABORT, 'invalid stage lifecycle or recovery transition'); END;
CREATE TRIGGER apply_attempt_identity_immutable BEFORE UPDATE ON apply_attempts
WHEN NEW.id IS NOT OLD.id OR NEW.stage_id IS NOT OLD.stage_id OR NEW.revision IS NOT OLD.revision OR NEW.semantic_digest IS NOT OLD.semantic_digest
 OR NEW.recovery_mode IS NOT OLD.recovery_mode OR NEW.prior_recovery IS NOT OLD.prior_recovery
 OR NEW.forced_duplicate_risk IS NOT OLD.forced_duplicate_risk OR NEW.plan_json IS NOT OLD.plan_json
 OR NEW.pending_post_id IS NOT OLD.pending_post_id OR NEW.started_at IS NOT OLD.started_at
BEGIN SELECT RAISE(ABORT, 'apply attempt identity is immutable'); END;
CREATE TRIGGER apply_attempt_outcome_immutable BEFORE UPDATE OF outcome,ended_at ON apply_attempts
WHEN OLD.outcome IS NOT NULL
BEGIN SELECT RAISE(ABORT, 'apply attempt outcome is immutable'); END;
CREATE TRIGGER apply_attempt_history_immutable_delete BEFORE DELETE ON apply_attempts
WHEN OLD.outcome IS NOT NULL OR EXISTS(SELECT 1 FROM apply_steps WHERE attempt_id=OLD.id AND state!='pending')
BEGIN SELECT RAISE(ABORT, 'dispatched apply history is immutable'); END;
CREATE TRIGGER apply_step_identity_immutable BEFORE UPDATE ON apply_steps
WHEN NEW.attempt_id IS NOT OLD.attempt_id OR NEW.ordinal IS NOT OLD.ordinal
 OR NEW.kind IS NOT OLD.kind OR NEW.condition IS NOT OLD.condition
BEGIN SELECT RAISE(ABORT, 'apply step identity is immutable'); END;
CREATE TRIGGER apply_step_state_transition_valid BEFORE UPDATE OF state ON apply_steps
WHEN NOT (
  OLD.state='pending' AND NEW.state IN ('dispatch_intent','skipped','not_dispatched')
  OR OLD.state='dispatch_intent' AND NEW.state IN ('response_validated','rejected','outcome_unknown')
)
BEGIN SELECT RAISE(ABORT, 'invalid apply step transition'); END;
CREATE TRIGGER apply_step_state_transition_required BEFORE UPDATE ON apply_steps
WHEN NEW.state IS OLD.state
BEGIN SELECT RAISE(ABORT, 'apply step history is immutable'); END;
CREATE TRIGGER apply_step_result_transition_valid BEFORE UPDATE ON apply_steps
WHEN NEW.state IN ('response_validated','rejected','skipped') AND NOT EXISTS(
  SELECT 1 FROM apply_attempts a JOIN stages s ON s.id=a.stage_id
    JOIN stage_revisions r ON r.stage_id=a.stage_id AND r.revision=a.revision
  WHERE a.id=NEW.attempt_id AND json_type(NEW.result_json)='object' AND (
    NEW.state='rejected' AND (SELECT count(*) FROM json_each(NEW.result_json))=1
      AND json_type(NEW.result_json,'$.status')='integer' AND json_extract(NEW.result_json,'$.status') BETWEEN 400 AND 499
    OR NEW.state='skipped' AND NEW.condition='if_missing' AND (SELECT count(*) FROM json_each(NEW.result_json))=1
      AND json_type(NEW.result_json,'$.reason')='text' AND json_extract(NEW.result_json,'$.reason')='already_satisfied'
    OR NEW.state='response_validated' AND NEW.kind='upload_attachment' AND (SELECT count(*) FROM json_each(NEW.result_json))=1
      AND json_type(NEW.result_json,'$.fileId')='text' AND length(json_extract(NEW.result_json,'$.fileId'))>0
    OR NEW.state='response_validated' AND NEW.kind='create_post' AND (SELECT count(*) FROM json_each(NEW.result_json))=5
      AND json_type(NEW.result_json,'$.postId')='text' AND length(json_extract(NEW.result_json,'$.postId'))>0
      AND json_type(NEW.result_json,'$.createAt')='integer' AND json_extract(NEW.result_json,'$.createAt')>0
      AND json_extract(NEW.result_json,'$.channelId')=json_extract(r.destination_json,'$.channelId')
      AND json_extract(NEW.result_json,'$.userId')=s.user_id AND json_extract(NEW.result_json,'$.pendingPostId')=a.pending_post_id
    OR NEW.state='response_validated' AND NEW.kind='edit_post' AND (SELECT count(*) FROM json_each(NEW.result_json))=2
      AND json_extract(NEW.result_json,'$.postId')=json_extract(r.destination_json,'$.postId')
      AND json_type(NEW.result_json,'$.updateAt')='integer' AND json_extract(NEW.result_json,'$.updateAt')>0
    OR NEW.state='response_validated' AND NEW.kind='delete_post' AND (SELECT count(*) FROM json_each(NEW.result_json))=2
      AND json_extract(NEW.result_json,'$.postId')=json_extract(r.destination_json,'$.postId')
      AND json_type(NEW.result_json,'$.deleteAt')='integer' AND json_extract(NEW.result_json,'$.deleteAt')>0
    OR NEW.state='response_validated' AND NEW.kind IN ('add_reaction','remove_reaction') AND (SELECT count(*) FROM json_each(NEW.result_json))=1
      AND json_extract(NEW.result_json,'$.postId')=json_extract(r.destination_json,'$.postId')
    OR NEW.state='response_validated' AND NEW.kind='resolve_conversation' AND (SELECT count(*) FROM json_each(NEW.result_json))=2
      AND json_type(NEW.result_json,'$.channelId')='text' AND length(json_extract(NEW.result_json,'$.channelId'))>0
      AND (json_extract(r.destination_json,'$.channelId') IS NULL OR json_extract(NEW.result_json,'$.channelId')=json_extract(r.destination_json,'$.channelId'))
      AND json_extract(NEW.result_json,'$.participantIds')=json_extract(r.destination_json,'$.participantIds')
  )
)
BEGIN SELECT RAISE(ABORT, 'invalid apply step result binding'); END;
CREATE TRIGGER apply_steps_history_immutable_delete BEFORE DELETE ON apply_steps
WHEN OLD.state!='pending' OR NOT EXISTS(
  SELECT 1 FROM apply_attempts a JOIN stages s ON s.id=a.stage_id
  WHERE a.id=OLD.attempt_id AND a.outcome IS NULL AND s.lifecycle='open' AND s.claim_attempt_id IS NULL
)
BEGIN SELECT RAISE(ABORT, 'dispatched apply steps are immutable'); END;
CREATE TRIGGER apply_events_immutable_update BEFORE UPDATE ON apply_events BEGIN SELECT RAISE(ABORT, 'apply events are immutable'); END;
CREATE TRIGGER apply_events_history_immutable_delete BEFORE DELETE ON apply_events
WHEN NOT EXISTS(SELECT 1 FROM apply_attempts a WHERE a.id=OLD.attempt_id AND a.outcome IS NULL)
 OR EXISTS(SELECT 1 FROM apply_steps WHERE attempt_id=OLD.attempt_id AND state!='pending')
BEGIN SELECT RAISE(ABORT, 'dispatched apply events are immutable'); END;
CREATE TRIGGER apply_requests_immutable_update BEFORE UPDATE ON apply_requests BEGIN SELECT RAISE(ABORT, 'apply requests are immutable'); END;
CREATE TRIGGER apply_requests_history_immutable_delete BEFORE DELETE ON apply_requests
WHEN NOT EXISTS(SELECT 1 FROM apply_attempts a WHERE a.id=OLD.attempt_id AND a.outcome IS NULL)
 OR EXISTS(SELECT 1 FROM apply_steps WHERE attempt_id=OLD.attempt_id AND state!='pending')
BEGIN SELECT RAISE(ABORT, 'dispatched apply requests are immutable'); END;
CREATE TRIGGER apply_receipts_immutable_update BEFORE UPDATE ON apply_receipts BEGIN SELECT RAISE(ABORT, 'apply receipts are immutable'); END;
CREATE TRIGGER apply_receipts_immutable_delete BEFORE DELETE ON apply_receipts BEGIN SELECT RAISE(ABORT, 'apply receipts are immutable'); END;
CREATE TRIGGER stage_revision_semantics_immutable BEFORE UPDATE OF stage_id,revision,created_at,semantic_digest,destination_json,plan_json ON stage_revisions
WHEN NEW.stage_id IS NOT OLD.stage_id OR NEW.revision IS NOT OLD.revision OR NEW.created_at IS NOT OLD.created_at
 OR NEW.semantic_digest IS NOT OLD.semantic_digest OR NEW.destination_json IS NOT OLD.destination_json OR NEW.plan_json IS NOT OLD.plan_json
BEGIN SELECT RAISE(ABORT, 'stage revision semantics are immutable'); END;
CREATE TRIGGER stage_revision_state_transition_valid BEFORE UPDATE OF state ON stage_revisions
WHEN NOT (OLD.state='current' AND NEW.state='superseded' AND EXISTS(
  SELECT 1 FROM stages s WHERE s.id=OLD.stage_id AND s.claim_attempt_id IS NULL AND s.current_revision=OLD.revision
    AND (s.lifecycle='open' AND s.recovery!='forbidden' OR s.lifecycle='expired' AND s.recovery='forbidden')
))
BEGIN SELECT RAISE(ABORT, 'invalid stage revision state transition'); END;
CREATE TRIGGER stage_revision_insert_lifecycle_valid BEFORE INSERT ON stage_revisions
WHEN EXISTS(SELECT 1 FROM stages s WHERE s.id=NEW.stage_id) AND NOT EXISTS(
  SELECT 1 FROM stages s WHERE s.id=NEW.stage_id AND s.claim_attempt_id IS NULL AND NEW.state='current' AND (
    NEW.revision=s.current_revision AND NOT EXISTS(SELECT 1 FROM stage_revisions r WHERE r.stage_id=NEW.stage_id)
    OR NEW.revision=s.current_revision+1 AND (s.lifecycle='open' AND s.recovery!='forbidden' OR s.lifecycle='expired' AND s.recovery='forbidden')
  )
)
BEGIN SELECT RAISE(ABORT, 'stage revision insertion is not eligible'); END;
CREATE TRIGGER stage_revision_body_erasure_only BEFORE UPDATE OF body ON stage_revisions
WHEN NEW.body IS NOT OLD.body AND NOT (OLD.body IS NOT NULL AND NEW.body IS NULL AND EXISTS(
  SELECT 1 FROM stages s WHERE s.id=OLD.stage_id AND s.lifecycle='completed' AND s.recovery='forbidden'
))
BEGIN SELECT RAISE(ABORT, 'stage revision body is immutable'); END;
CREATE TRIGGER stage_revisions_history_immutable_delete BEFORE DELETE ON stage_revisions
BEGIN SELECT RAISE(ABORT, 'stage revision history is immutable'); END;
CREATE TRIGGER stage_attachment_immutable_update BEFORE UPDATE ON stage_attachments
BEGIN SELECT RAISE(ABORT, 'stage attachment bindings are immutable'); END;
CREATE TRIGGER stage_attachment_delete_after_completion BEFORE DELETE ON stage_attachments
WHEN NOT EXISTS(SELECT 1 FROM stages s WHERE s.id=OLD.stage_id AND s.lifecycle='completed' AND s.recovery='forbidden')
BEGIN SELECT RAISE(ABORT, 'stage attachment bindings are immutable'); END;
CREATE TRIGGER stage_attachment_insert_before_completion BEFORE INSERT ON stage_attachments
WHEN EXISTS(SELECT 1 FROM stages s WHERE s.id=NEW.stage_id AND s.lifecycle IN ('completed','pruned'))
BEGIN SELECT RAISE(ABORT, 'completed stage attachments are immutable'); END;
CREATE TRIGGER stage_current_revision_transition_valid BEFORE UPDATE OF current_revision ON stages
WHEN NEW.current_revision IS NOT OLD.current_revision AND NOT (
  OLD.claim_attempt_id IS NULL AND NEW.claim_attempt_id IS NULL AND NEW.current_revision=OLD.current_revision+1
  AND NEW.lifecycle='open' AND NEW.recovery!='forbidden'
  AND (OLD.lifecycle='open' AND OLD.recovery!='forbidden' OR OLD.lifecycle='expired' AND OLD.recovery='forbidden')
  AND EXISTS(SELECT 1 FROM stage_revisions r WHERE r.stage_id=OLD.id AND r.revision=OLD.current_revision AND r.state='superseded')
  AND EXISTS(SELECT 1 FROM stage_revisions r WHERE r.stage_id=OLD.id AND r.revision=NEW.current_revision AND r.state='current')
)
BEGIN SELECT RAISE(ABORT, 'invalid current stage revision transition'); END;
`}, {version: 7, name: "status-confirmed-delete-results", sql: `
DROP TRIGGER apply_step_result_transition_valid;
DROP TRIGGER apply_step_state_transition_required;
DROP TRIGGER apply_receipts_immutable_update;
UPDATE apply_steps
SET result_json=json_object('postId',json_extract(result_json,'$.postId'))
WHERE state='response_validated' AND kind='delete_post';
UPDATE apply_receipts
SET receipt_json=json_set(receipt_json,'$.steps[0].result',json_object('postId',json_extract(receipt_json,'$.steps[0].result.postId')))
WHERE json_extract(receipt_json,'$.operation')='delete_post'
  AND json_extract(receipt_json,'$.steps[0].kind')='delete_post'
  AND json_extract(receipt_json,'$.steps[0].state')='response_validated'
  AND json_type(receipt_json,'$.steps[0].result.deleteAt')='integer';
CREATE TRIGGER apply_step_state_transition_required BEFORE UPDATE ON apply_steps
WHEN NEW.state IS OLD.state
BEGIN SELECT RAISE(ABORT, 'apply step history is immutable'); END;
CREATE TRIGGER apply_receipts_immutable_update BEFORE UPDATE ON apply_receipts BEGIN SELECT RAISE(ABORT, 'apply receipts are immutable'); END;
CREATE TRIGGER apply_step_result_transition_valid BEFORE UPDATE ON apply_steps
WHEN NEW.state IN ('response_validated','rejected','skipped') AND NOT EXISTS(
  SELECT 1 FROM apply_attempts a JOIN stages s ON s.id=a.stage_id
    JOIN stage_revisions r ON r.stage_id=a.stage_id AND r.revision=a.revision
  WHERE a.id=NEW.attempt_id AND json_type(NEW.result_json)='object' AND (
    NEW.state='rejected' AND (SELECT count(*) FROM json_each(NEW.result_json))=1
      AND json_type(NEW.result_json,'$.status')='integer' AND json_extract(NEW.result_json,'$.status') BETWEEN 400 AND 499
    OR NEW.state='skipped' AND NEW.condition='if_missing' AND (SELECT count(*) FROM json_each(NEW.result_json))=1
      AND json_type(NEW.result_json,'$.reason')='text' AND json_extract(NEW.result_json,'$.reason')='already_satisfied'
    OR NEW.state='response_validated' AND NEW.kind='upload_attachment' AND (SELECT count(*) FROM json_each(NEW.result_json))=1
      AND json_type(NEW.result_json,'$.fileId')='text' AND length(json_extract(NEW.result_json,'$.fileId'))>0
    OR NEW.state='response_validated' AND NEW.kind='create_post' AND (SELECT count(*) FROM json_each(NEW.result_json))=5
      AND json_type(NEW.result_json,'$.postId')='text' AND length(json_extract(NEW.result_json,'$.postId'))>0
      AND json_type(NEW.result_json,'$.createAt')='integer' AND json_extract(NEW.result_json,'$.createAt')>0
      AND json_extract(NEW.result_json,'$.channelId')=json_extract(r.destination_json,'$.channelId')
      AND json_extract(NEW.result_json,'$.userId')=s.user_id AND json_extract(NEW.result_json,'$.pendingPostId')=a.pending_post_id
    OR NEW.state='response_validated' AND NEW.kind='edit_post' AND (SELECT count(*) FROM json_each(NEW.result_json))=2
      AND json_extract(NEW.result_json,'$.postId')=json_extract(r.destination_json,'$.postId')
      AND json_type(NEW.result_json,'$.updateAt')='integer' AND json_extract(NEW.result_json,'$.updateAt')>0
    OR NEW.state='response_validated' AND NEW.kind='delete_post' AND (SELECT count(*) FROM json_each(NEW.result_json))=1
      AND json_extract(NEW.result_json,'$.postId')=json_extract(r.destination_json,'$.postId')
    OR NEW.state='response_validated' AND NEW.kind IN ('add_reaction','remove_reaction') AND (SELECT count(*) FROM json_each(NEW.result_json))=1
      AND json_extract(NEW.result_json,'$.postId')=json_extract(r.destination_json,'$.postId')
    OR NEW.state='response_validated' AND NEW.kind='resolve_conversation' AND (SELECT count(*) FROM json_each(NEW.result_json))=2
      AND json_type(NEW.result_json,'$.channelId')='text' AND length(json_extract(NEW.result_json,'$.channelId'))>0
      AND (json_extract(r.destination_json,'$.channelId') IS NULL OR json_extract(NEW.result_json,'$.channelId')=json_extract(r.destination_json,'$.channelId'))
      AND json_extract(NEW.result_json,'$.participantIds')=json_extract(r.destination_json,'$.participantIds')
  )
)
BEGIN SELECT RAISE(ABORT, 'invalid apply step result binding'); END;
`}, {version: 8, name: "already-satisfied-edit-apply", sql: `
DROP TRIGGER apply_step_identity_immutable;
DROP TRIGGER apply_step_state_transition_valid;
DROP TRIGGER apply_step_state_transition_required;
DROP TRIGGER apply_step_result_transition_valid;
DROP TRIGGER apply_steps_history_immutable_delete;
DROP TRIGGER stage_apply_claim_release_valid;
DROP TRIGGER stage_lifecycle_recovery_transition_valid;
DROP TRIGGER apply_attempt_history_immutable_delete;
DROP TRIGGER apply_events_history_immutable_delete;
DROP TRIGGER apply_requests_history_immutable_delete;
CREATE TABLE apply_steps_v8 (
  attempt_id TEXT NOT NULL REFERENCES apply_attempts(id) ON DELETE CASCADE,
  ordinal INTEGER NOT NULL CHECK (ordinal > 0),
  kind TEXT NOT NULL,
  condition TEXT NOT NULL CHECK (condition IN ('always','if_missing')),
  state TEXT NOT NULL CHECK (state IN ('pending','dispatch_intent','response_validated','rejected','outcome_unknown','skipped','not_dispatched')),
  result_json TEXT CHECK (result_json IS NULL OR json_valid(result_json)),
  started_at TEXT,
  ended_at TEXT,
  PRIMARY KEY (attempt_id, ordinal),
  CHECK (
    state='pending' AND result_json IS NULL AND started_at IS NULL AND ended_at IS NULL
    OR state='dispatch_intent' AND result_json IS NULL AND started_at IS NOT NULL AND ended_at IS NULL
    OR state IN ('response_validated','rejected') AND result_json IS NOT NULL AND started_at IS NOT NULL AND ended_at IS NOT NULL
    OR state='outcome_unknown' AND result_json IS NULL AND started_at IS NOT NULL AND ended_at IS NOT NULL
    OR state='skipped' AND (condition='if_missing' OR kind='edit_post' AND condition='always') AND result_json IS NOT NULL AND started_at IS NULL AND ended_at IS NOT NULL
    OR state='not_dispatched' AND result_json IS NULL AND started_at IS NULL AND ended_at IS NOT NULL
  )
) STRICT;
INSERT INTO apply_steps_v8(attempt_id,ordinal,kind,condition,state,result_json,started_at,ended_at)
SELECT attempt_id,ordinal,kind,condition,state,result_json,started_at,ended_at FROM apply_steps;
DROP TABLE apply_steps;
ALTER TABLE apply_steps_v8 RENAME TO apply_steps;
CREATE TRIGGER apply_step_identity_immutable BEFORE UPDATE ON apply_steps
WHEN NEW.attempt_id IS NOT OLD.attempt_id OR NEW.ordinal IS NOT OLD.ordinal
 OR NEW.kind IS NOT OLD.kind OR NEW.condition IS NOT OLD.condition
BEGIN SELECT RAISE(ABORT, 'apply step identity is immutable'); END;
CREATE TRIGGER apply_step_state_transition_valid BEFORE UPDATE OF state ON apply_steps
WHEN NOT (
  OLD.state='pending' AND NEW.state IN ('dispatch_intent','skipped','not_dispatched')
  OR OLD.state='dispatch_intent' AND NEW.state IN ('response_validated','rejected','outcome_unknown')
)
BEGIN SELECT RAISE(ABORT, 'invalid apply step transition'); END;
CREATE TRIGGER apply_step_state_transition_required BEFORE UPDATE ON apply_steps
WHEN NEW.state IS OLD.state
BEGIN SELECT RAISE(ABORT, 'apply step history is immutable'); END;
CREATE TRIGGER apply_step_result_transition_valid BEFORE UPDATE ON apply_steps
WHEN NEW.state IN ('response_validated','rejected','skipped') AND NOT EXISTS(
  SELECT 1 FROM apply_attempts a JOIN stages s ON s.id=a.stage_id
    JOIN stage_revisions r ON r.stage_id=a.stage_id AND r.revision=a.revision
  WHERE a.id=NEW.attempt_id AND json_type(NEW.result_json)='object' AND (
    NEW.state='rejected' AND (SELECT count(*) FROM json_each(NEW.result_json))=1
      AND json_type(NEW.result_json,'$.status')='integer' AND json_extract(NEW.result_json,'$.status') BETWEEN 400 AND 499
    OR NEW.state='skipped' AND (NEW.condition='if_missing' OR NEW.kind='edit_post' AND NEW.condition='always') AND (SELECT count(*) FROM json_each(NEW.result_json))=1
      AND json_type(NEW.result_json,'$.reason')='text' AND json_extract(NEW.result_json,'$.reason')='already_satisfied'
    OR NEW.state='response_validated' AND NEW.kind='upload_attachment' AND (SELECT count(*) FROM json_each(NEW.result_json))=1
      AND json_type(NEW.result_json,'$.fileId')='text' AND length(json_extract(NEW.result_json,'$.fileId'))>0
    OR NEW.state='response_validated' AND NEW.kind='create_post' AND (SELECT count(*) FROM json_each(NEW.result_json))=5
      AND json_type(NEW.result_json,'$.postId')='text' AND length(json_extract(NEW.result_json,'$.postId'))>0
      AND json_type(NEW.result_json,'$.createAt')='integer' AND json_extract(NEW.result_json,'$.createAt')>0
      AND json_extract(NEW.result_json,'$.channelId')=json_extract(r.destination_json,'$.channelId')
      AND json_extract(NEW.result_json,'$.userId')=s.user_id AND json_extract(NEW.result_json,'$.pendingPostId')=a.pending_post_id
    OR NEW.state='response_validated' AND NEW.kind='edit_post' AND (SELECT count(*) FROM json_each(NEW.result_json))=2
      AND json_extract(NEW.result_json,'$.postId')=json_extract(r.destination_json,'$.postId')
      AND json_type(NEW.result_json,'$.updateAt')='integer' AND json_extract(NEW.result_json,'$.updateAt')>0
    OR NEW.state='response_validated' AND NEW.kind='delete_post' AND (SELECT count(*) FROM json_each(NEW.result_json))=1
      AND json_extract(NEW.result_json,'$.postId')=json_extract(r.destination_json,'$.postId')
    OR NEW.state='response_validated' AND NEW.kind IN ('add_reaction','remove_reaction') AND (SELECT count(*) FROM json_each(NEW.result_json))=1
      AND json_extract(NEW.result_json,'$.postId')=json_extract(r.destination_json,'$.postId')
    OR NEW.state='response_validated' AND NEW.kind='resolve_conversation' AND (SELECT count(*) FROM json_each(NEW.result_json))=2
      AND json_type(NEW.result_json,'$.channelId')='text' AND length(json_extract(NEW.result_json,'$.channelId'))>0
      AND (json_extract(r.destination_json,'$.channelId') IS NULL OR json_extract(NEW.result_json,'$.channelId')=json_extract(r.destination_json,'$.channelId'))
      AND json_extract(NEW.result_json,'$.participantIds')=json_extract(r.destination_json,'$.participantIds')
  )
)
BEGIN SELECT RAISE(ABORT, 'invalid apply step result binding'); END;
CREATE TRIGGER apply_steps_history_immutable_delete BEFORE DELETE ON apply_steps
WHEN OLD.state!='pending' OR NOT EXISTS(
  SELECT 1 FROM apply_attempts a JOIN stages s ON s.id=a.stage_id
  WHERE a.id=OLD.attempt_id AND a.outcome IS NULL AND s.lifecycle='open' AND s.claim_attempt_id IS NULL
)
BEGIN SELECT RAISE(ABORT, 'dispatched apply steps are immutable'); END;
CREATE TRIGGER stage_apply_claim_release_valid BEFORE UPDATE OF lifecycle,claim_attempt_id ON stages
WHEN OLD.lifecycle='applying' AND NEW.lifecycle!='applying' AND EXISTS(
  SELECT 1 FROM apply_attempts a WHERE a.id=OLD.claim_attempt_id AND a.outcome IS NULL
    AND EXISTS(SELECT 1 FROM apply_steps p WHERE p.attempt_id=a.id AND p.state!='pending')
)
BEGIN SELECT RAISE(ABORT, 'dispatched apply claim cannot be released'); END;
CREATE TRIGGER stage_lifecycle_recovery_transition_valid BEFORE UPDATE OF lifecycle,recovery ON stages
WHEN (NEW.lifecycle IS NOT OLD.lifecycle OR NEW.recovery IS NOT OLD.recovery) AND NOT (
  OLD.lifecycle='open' AND NEW.lifecycle='applying' AND NEW.recovery=OLD.recovery AND NEW.claim_attempt_id IS NOT NULL
    AND EXISTS(SELECT 1 FROM apply_attempts a WHERE a.id=NEW.claim_attempt_id AND a.stage_id=OLD.id AND a.outcome IS NULL)
  OR OLD.lifecycle='applying' AND NEW.lifecycle='open' AND NEW.recovery=OLD.recovery AND NEW.claim_attempt_id IS NULL
    AND EXISTS(SELECT 1 FROM apply_attempts a WHERE a.id=OLD.claim_attempt_id AND a.outcome IS NULL)
    AND NOT EXISTS(SELECT 1 FROM apply_steps p WHERE p.attempt_id=OLD.claim_attempt_id AND p.state!='pending')
  OR OLD.lifecycle='applying' AND NEW.claim_attempt_id IS NULL AND EXISTS(
    SELECT 1 FROM apply_attempts a WHERE a.id=OLD.claim_attempt_id AND a.outcome IS NOT NULL AND (
      a.outcome IN ('succeeded','already_satisfied') AND NEW.lifecycle='completed' AND NEW.recovery='forbidden'
      OR a.outcome='rejected' AND NEW.lifecycle='open' AND NEW.recovery=a.prior_recovery
      OR a.outcome='partial' AND NEW.lifecycle='open' AND NEW.recovery=CASE WHEN a.prior_recovery='force_unknown' THEN 'force_unknown' ELSE 'resume_partial' END
      OR a.outcome='unknown' AND NEW.lifecycle='open' AND NEW.recovery='force_unknown'
    )
  )
  OR OLD.lifecycle='open' AND NEW.lifecycle='canceled' AND NEW.recovery='forbidden' AND OLD.claim_attempt_id IS NULL AND NEW.claim_attempt_id IS NULL
  OR OLD.lifecycle='open' AND OLD.recovery='none' AND NEW.lifecycle='expired' AND NEW.recovery='forbidden' AND OLD.claim_attempt_id IS NULL AND NEW.claim_attempt_id IS NULL
  OR OLD.lifecycle='expired' AND OLD.recovery='forbidden' AND NEW.lifecycle='open' AND NEW.recovery='none'
    AND OLD.claim_attempt_id IS NULL AND NEW.claim_attempt_id IS NULL AND NEW.current_revision=OLD.current_revision+1
    AND EXISTS(SELECT 1 FROM stage_revisions r WHERE r.stage_id=OLD.id AND r.revision=OLD.current_revision AND r.state='superseded')
    AND EXISTS(SELECT 1 FROM stage_revisions r WHERE r.stage_id=OLD.id AND r.revision=NEW.current_revision AND r.state='current')
)
BEGIN SELECT RAISE(ABORT, 'invalid stage lifecycle or recovery transition'); END;
CREATE TRIGGER apply_attempt_history_immutable_delete BEFORE DELETE ON apply_attempts
WHEN OLD.outcome IS NOT NULL OR EXISTS(SELECT 1 FROM apply_steps WHERE attempt_id=OLD.id AND state!='pending')
BEGIN SELECT RAISE(ABORT, 'dispatched apply history is immutable'); END;
CREATE TRIGGER apply_events_history_immutable_delete BEFORE DELETE ON apply_events
WHEN NOT EXISTS(SELECT 1 FROM apply_attempts a WHERE a.id=OLD.attempt_id AND a.outcome IS NULL)
 OR EXISTS(SELECT 1 FROM apply_steps WHERE attempt_id=OLD.attempt_id AND state!='pending')
BEGIN SELECT RAISE(ABORT, 'dispatched apply events are immutable'); END;
CREATE TRIGGER apply_requests_history_immutable_delete BEFORE DELETE ON apply_requests
WHEN NOT EXISTS(SELECT 1 FROM apply_attempts a WHERE a.id=OLD.attempt_id AND a.outcome IS NULL)
 OR EXISTS(SELECT 1 FROM apply_steps WHERE attempt_id=OLD.attempt_id AND state!='pending')
BEGIN SELECT RAISE(ABORT, 'dispatched apply requests are immutable'); END;
`}, {version: 9, name: "attachment-identity-binding", sql: `
CREATE TABLE stage_attachment_identities (
  stage_id TEXT NOT NULL,
  revision INTEGER NOT NULL,
  ordinal INTEGER NOT NULL,
  file_identity BLOB NOT NULL CHECK (length(file_identity) = 32),
  PRIMARY KEY (stage_id,revision,ordinal),
  FOREIGN KEY (stage_id,revision,ordinal) REFERENCES stage_attachments(stage_id,revision,ordinal) ON DELETE CASCADE
) STRICT;
CREATE TRIGGER stage_attachment_identity_immutable_update BEFORE UPDATE ON stage_attachment_identities
BEGIN SELECT RAISE(ABORT, 'stage attachment identity is immutable'); END;
CREATE TRIGGER stage_attachment_identity_immutable_delete BEFORE DELETE ON stage_attachment_identities
WHEN EXISTS(SELECT 1 FROM stage_attachments a WHERE a.stage_id=OLD.stage_id AND a.revision=OLD.revision AND a.ordinal=OLD.ordinal)
BEGIN SELECT RAISE(ABORT, 'stage attachment identity is immutable'); END;
`}, {version: 10, name: "validated-upload-reuse", sql: `
DROP TRIGGER apply_step_state_transition_valid;
CREATE TABLE apply_step_reuse (
  attempt_id TEXT NOT NULL,
  ordinal INTEGER NOT NULL CHECK (ordinal > 0),
  source_attempt_id TEXT NOT NULL,
  source_ordinal INTEGER NOT NULL CHECK (source_ordinal > 0),
  file_id TEXT NOT NULL CHECK (length(file_id) > 0),
  PRIMARY KEY (attempt_id,ordinal),
  FOREIGN KEY (attempt_id,ordinal) REFERENCES apply_steps(attempt_id,ordinal) ON DELETE CASCADE,
  FOREIGN KEY (source_attempt_id,source_ordinal) REFERENCES apply_steps(attempt_id,ordinal),
  CHECK (attempt_id != source_attempt_id),
  CHECK (ordinal = source_ordinal)
) STRICT;
CREATE TRIGGER apply_step_reuse_insert_valid BEFORE INSERT ON apply_step_reuse
WHEN NOT EXISTS(
  SELECT 1 FROM apply_steps d
  JOIN apply_attempts da ON da.id=d.attempt_id
  JOIN apply_steps s ON s.attempt_id=NEW.source_attempt_id AND s.ordinal=NEW.source_ordinal
  JOIN apply_attempts sa ON sa.id=s.attempt_id
  WHERE d.attempt_id=NEW.attempt_id AND d.ordinal=NEW.ordinal
    AND d.kind='upload_attachment' AND d.state='pending' AND da.recovery_mode='resume_partial'
    AND s.kind='upload_attachment' AND s.state='response_validated'
    AND json_extract(s.result_json,'$.fileId')=NEW.file_id
    AND sa.outcome IS NOT NULL
    AND da.stage_id=sa.stage_id AND da.revision=sa.revision AND da.semantic_digest=sa.semantic_digest
    AND NOT EXISTS(SELECT 1 FROM apply_step_reuse prior WHERE prior.attempt_id=s.attempt_id AND prior.ordinal=s.ordinal)
    AND NOT EXISTS(SELECT 1 FROM apply_steps uncertain WHERE uncertain.attempt_id=sa.id AND uncertain.state='outcome_unknown')
)
BEGIN SELECT RAISE(ABORT, 'invalid validated upload reuse'); END;
CREATE TRIGGER apply_step_reuse_immutable_update BEFORE UPDATE ON apply_step_reuse
BEGIN SELECT RAISE(ABORT, 'validated upload reuse is immutable'); END;
CREATE TRIGGER apply_step_reuse_immutable_delete BEFORE DELETE ON apply_step_reuse
BEGIN SELECT RAISE(ABORT, 'validated upload reuse is immutable'); END;
CREATE TRIGGER apply_step_state_transition_valid BEFORE UPDATE OF state ON apply_steps
WHEN NOT (
  OLD.state='pending' AND NEW.state IN ('dispatch_intent','skipped','not_dispatched')
  OR OLD.state='pending' AND NEW.state='response_validated' AND NEW.kind='upload_attachment'
    AND EXISTS(SELECT 1 FROM apply_step_reuse r WHERE r.attempt_id=NEW.attempt_id AND r.ordinal=NEW.ordinal AND json_extract(NEW.result_json,'$.fileId')=r.file_id)
  OR OLD.state='dispatch_intent' AND NEW.state IN ('response_validated','rejected','outcome_unknown')
)
BEGIN SELECT RAISE(ABORT, 'invalid apply step transition'); END;
`}}

func attachmentIdentityAvailable() bool {
	return len(migrations) >= 9 && migrations[8].version == 9 && migrations[8].name == "attachment-identity-binding"
}

func validatedUploadReuseAvailable() bool {
	return len(migrations) >= 10 && migrations[9].version == 10 && migrations[9].name == "validated-upload-reuse"
}
