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
`}}
