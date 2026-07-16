package stagestore

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ardasevinc/mattermost-cli/internal/messageinput"
	"github.com/ardasevinc/mattermost-cli/internal/serverurl"
)

const (
	maxIdentityBytes  = 4096
	maxJSONBytes      = 1 << 20
	maxRequestID      = 256
	maxListLimit      = 100
	maxAttachments    = 100
	maxFilenameBytes  = 255
	maxMediaTypeBytes = 256
)

var (
	ErrConflict    = errors.New("stage store: request conflict")
	ErrNotFound    = errors.New("stage store: stage not found")
	ErrNotEligible = errors.New("stage store: lifecycle transition not allowed")
	ErrInvalid     = errors.New("stage store: invalid stage input")
	commitHook     struct {
		sync.RWMutex
		fn func()
	}
)

type Operation string

const (
	CreatePost     Operation = "create_post"
	Reply          Operation = "reply"
	EditPost       Operation = "edit_post"
	DeletePost     Operation = "delete_post"
	React          Operation = "react"
	Unreact        Operation = "unreact"
	ResolveDM      Operation = "resolve_dm"
	ResolveGroupDM Operation = "resolve_group_dm"
)

type Lifecycle string
type Recovery string

const (
	LifecycleOpen      Lifecycle = "open"
	LifecycleApplying  Lifecycle = "applying"
	LifecycleCompleted Lifecycle = "completed"
	LifecycleCanceled  Lifecycle = "canceled"
	LifecycleExpired   Lifecycle = "expired"
	LifecyclePruned    Lifecycle = "pruned"
	RecoveryNone       Recovery  = "none"
	RecoveryPartial    Recovery  = "resume_partial"
	RecoveryUnknown    Recovery  = "force_unknown"
	RecoveryForbidden  Recovery  = "forbidden"
)

type Attachment struct {
	SuppliedPath   string   `json:"suppliedPath"`
	CanonicalPath  string   `json:"canonicalPath"`
	RemoteFilename string   `json:"remoteFilename"`
	ByteLength     int64    `json:"byteLength"`
	MediaType      string   `json:"mediaType,omitempty"`
	ContentDigest  [32]byte `json:"contentDigest"`
}
type RevisionContent struct {
	Body        []byte
	Destination json.RawMessage
	Plan        json.RawMessage
	Attachments []Attachment
}
type Composition struct {
	Body        []byte       `json:"body,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"`
}
type CreateInput struct {
	RequestID                   string
	RequestDigest               [32]byte
	Operation                   Operation
	ServerURL, ServerID, UserID string
	Content                     RevisionContent
}
type CreateRecord struct {
	MutationResult `json:"-"`
	Result         MutationResult  `json:"result"`
	RequestDigest  [32]byte        `json:"requestDigest"`
	Destination    json.RawMessage `json:"destination"`
	Plan           json.RawMessage `json:"plan"`
}
type ReviseInput struct {
	StageID, RequestID string
	ExpectedRevision   int64
	ExpectedDigest     [32]byte
	Revive             bool
	Composition        Composition
}
type CancelInput struct {
	StageID, RequestID string
	ExpectedRevision   int64
	ExpectedDigest     [32]byte
}

type StageSummary struct {
	ID             string    `json:"id"`
	ServerURL      string    `json:"serverUrl"`
	ServerID       string    `json:"serverId,omitempty"`
	UserID         string    `json:"userId"`
	Operation      Operation `json:"operation"`
	Lifecycle      Lifecycle `json:"lifecycle"`
	Recovery       Recovery  `json:"recovery"`
	Revision       int64     `json:"revision"`
	SemanticDigest [32]byte  `json:"semanticDigest"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}
type StageDetail struct {
	StageSummary
	RevisionCreatedAt time.Time
	Body              []byte
	Destination, Plan json.RawMessage
	Attachments       []Attachment
}
type MutationResult struct {
	Schema     string       `json:"schema"`
	Action     string       `json:"action"`
	Stage      StageSummary `json:"stage"`
	Revived    bool         `json:"revived"`
	RecordedAt time.Time    `json:"recordedAt"`
	Replay     bool         `json:"-"`
}
type ListOptions struct{ Limit int }

func (s *Store) Create(ctx context.Context, in CreateInput) (CreateRecord, error) {
	if err := ctx.Err(); err != nil {
		return CreateRecord{}, err
	}
	content, err := normalizeContent(in.Operation, in.Content)
	if err != nil || !validOperation(in.Operation) || !canonicalServerURL(in.ServerURL) || !bounded(in.UserID, maxIdentityBytes) || (in.ServerID != "" && !bounded(in.ServerID, maxIdentityBytes)) || !validRequestID(in.RequestID) {
		return CreateRecord{}, ErrInvalid
	}
	if in.RequestDigest == ([32]byte{}) {
		return CreateRecord{}, ErrInvalid
	}
	semantic := semanticDigest(in.Operation, in.ServerURL, in.ServerID, in.UserID, content)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CreateRecord{}, localError(err)
	}
	defer tx.Rollback()
	if in.RequestID != "" {
		if result, found, e := findCreate(ctx, tx, in.ServerURL, in.UserID, in.RequestID); e != nil {
			return CreateRecord{}, e
		} else if found {
			if result.RequestDigest != in.RequestDigest {
				return CreateRecord{}, ErrConflict
			}
			result.Replay, result.Result.Replay = true, true
			return result, nil
		}
	}
	id, err := newStageID()
	if err != nil {
		return CreateRecord{}, errors.New("stage store: random identity unavailable")
	}
	now := time.Now().UTC()
	stamp := formatTime(now)
	if _, err = tx.ExecContext(ctx, `INSERT INTO stages(id,created_at,updated_at,operation,server_url,server_id,user_id,lifecycle,recovery,current_revision) VALUES(?,?,?,?,?,?,?,?,?,1)`, id, stamp, stamp, in.Operation, in.ServerURL, nullable(in.ServerID), in.UserID, LifecycleOpen, RecoveryNone); err != nil {
		return CreateRecord{}, localError(err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO stage_revisions(stage_id,revision,state,created_at,semantic_digest,body,destination_json,plan_json) VALUES(?,1,'current',?,?,?,?,?)`, id, stamp, semantic[:], nullableBytes(content.Body), string(content.Destination), string(content.Plan)); err != nil {
		return CreateRecord{}, localError(err)
	}
	if err = insertAttachments(ctx, tx, id, 1, content.Attachments); err != nil {
		return CreateRecord{}, err
	}
	summary := StageSummary{id, in.ServerURL, in.ServerID, in.UserID, in.Operation, LifecycleOpen, RecoveryNone, 1, semantic, now, now}
	result := MutationResult{"mm/v2/stage-mutation-receipt", "create", summary, false, now, false}
	record := CreateRecord{result, result, in.RequestDigest, bytes.Clone(in.Content.Destination), bytes.Clone(in.Content.Plan)}
	if record.Destination, record.Plan, err = normalizeCreateProjection(record.Destination, record.Plan); err != nil {
		return CreateRecord{}, localError(err)
	}
	if err = persistCreate(ctx, tx, in.ServerURL, in.UserID, in.RequestID, record, stamp); err != nil {
		return CreateRecord{}, err
	}
	if err = tx.Commit(); err != nil {
		return CreateRecord{}, localError(err)
	}
	runCommitHook()
	return record, nil
}

func (s *Store) Revise(ctx context.Context, in ReviseInput) (MutationResult, error) {
	if err := ctx.Err(); err != nil {
		return MutationResult{}, err
	}
	if !bounded(in.StageID, maxIdentityBytes) || in.ExpectedRevision < 1 || !validRequestID(in.RequestID) {
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
	composition, err := normalizeComposition(base.Operation, in.Composition)
	if err != nil {
		return MutationResult{}, ErrInvalid
	}
	content := RevisionContent{composition.Body, bytes.Clone(base.Destination), bytes.Clone(base.Plan), composition.Attachments}
	requestDigest := digestValue(struct {
		Action, StageID  string
		ExpectedRevision int64
		ExpectedDigest   [32]byte
		Revive           bool
		Composition      Composition
	}{"revise", in.StageID, in.ExpectedRevision, in.ExpectedDigest, in.Revive, composition})
	if in.RequestID != "" {
		if result, found, e := loadReplay(ctx, tx, base.ServerURL, base.UserID, in.RequestID, "mm/v2/stage-revise-request", requestDigest); e != nil {
			return MutationResult{}, e
		} else if found {
			return result, nil
		}
	}
	if base.Revision != in.ExpectedRevision || base.SemanticDigest != in.ExpectedDigest {
		return MutationResult{}, ErrConflict
	}
	recovery := base.Recovery
	if in.Revive {
		if base.Lifecycle != LifecycleExpired || base.Recovery != RecoveryForbidden {
			return MutationResult{}, ErrNotEligible
		}
		recovery = RecoveryNone
	} else if base.Lifecycle != LifecycleOpen || base.Recovery == RecoveryForbidden {
		return MutationResult{}, ErrNotEligible
	}
	next := base.Revision + 1
	semantic := semanticDigest(base.Operation, base.ServerURL, base.ServerID, base.UserID, content)
	now := time.Now().UTC()
	stamp := formatTime(now)
	resultSQL, err := tx.ExecContext(ctx, `UPDATE stage_revisions SET state='superseded' WHERE stage_id=? AND revision=? AND state='current' AND semantic_digest=?`, in.StageID, in.ExpectedRevision, in.ExpectedDigest[:])
	if err != nil {
		return MutationResult{}, localError(err)
	}
	if !oneRow(resultSQL) {
		return MutationResult{}, ErrConflict
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO stage_revisions(stage_id,revision,state,created_at,semantic_digest,body,destination_json,plan_json) VALUES(?,?,'current',?,?,?,?,?)`, in.StageID, next, stamp, semantic[:], nullableBytes(content.Body), string(content.Destination), string(content.Plan)); err != nil {
		return MutationResult{}, localError(err)
	}
	if err = insertAttachments(ctx, tx, in.StageID, next, content.Attachments); err != nil {
		return MutationResult{}, err
	}
	resultSQL, err = tx.ExecContext(ctx, `UPDATE stages SET updated_at=?,lifecycle='open',recovery=?,current_revision=? WHERE id=? AND current_revision=? AND lifecycle=? AND recovery=?`, stamp, recovery, next, in.StageID, in.ExpectedRevision, base.Lifecycle, base.Recovery)
	if err != nil {
		return MutationResult{}, localError(err)
	}
	if !oneRow(resultSQL) {
		return MutationResult{}, ErrConflict
	}
	summary := StageSummary{in.StageID, base.ServerURL, base.ServerID, base.UserID, base.Operation, LifecycleOpen, recovery, next, semantic, base.CreatedAt, now}
	result := MutationResult{"mm/v2/stage-mutation-receipt", "revise", summary, in.Revive, now, false}
	if err = persistReplay(ctx, tx, base.ServerURL, base.UserID, in.RequestID, "mm/v2/stage-revise-request", requestDigest, result, stamp); err != nil {
		return MutationResult{}, err
	}
	if err = tx.Commit(); err != nil {
		return MutationResult{}, localError(err)
	}
	runCommitHook()
	return result, nil
}

func (s *Store) Cancel(ctx context.Context, in CancelInput) (MutationResult, error) {
	if err := ctx.Err(); err != nil {
		return MutationResult{}, err
	}
	if !bounded(in.StageID, maxIdentityBytes) || in.ExpectedRevision < 1 || !validRequestID(in.RequestID) {
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
	digest := digestValue(struct {
		Action, StageID  string
		ExpectedRevision int64
		ExpectedDigest   [32]byte
	}{"cancel", in.StageID, in.ExpectedRevision, in.ExpectedDigest})
	if in.RequestID != "" {
		if result, found, e := loadReplay(ctx, tx, base.ServerURL, base.UserID, in.RequestID, "mm/v2/stage-cancel-request", digest); e != nil {
			return MutationResult{}, e
		} else if found {
			return result, nil
		}
	}
	if base.Revision != in.ExpectedRevision || base.SemanticDigest != in.ExpectedDigest {
		return MutationResult{}, ErrConflict
	}
	if base.Lifecycle != LifecycleOpen {
		return MutationResult{}, ErrNotEligible
	}
	now := time.Now().UTC()
	stamp := formatTime(now)
	resultSQL, err := tx.ExecContext(ctx, `UPDATE stages SET lifecycle='canceled',recovery='forbidden',updated_at=? WHERE id=? AND current_revision=? AND lifecycle='open' AND EXISTS(SELECT 1 FROM stage_revisions WHERE stage_id=? AND revision=? AND state='current' AND semantic_digest=?)`, stamp, in.StageID, in.ExpectedRevision, in.StageID, in.ExpectedRevision, in.ExpectedDigest[:])
	if err != nil {
		return MutationResult{}, localError(err)
	}
	if !oneRow(resultSQL) {
		return MutationResult{}, ErrConflict
	}
	summary := base.StageSummary
	summary.Lifecycle = LifecycleCanceled
	summary.Recovery = RecoveryForbidden
	summary.UpdatedAt = now
	result := MutationResult{"mm/v2/stage-mutation-receipt", "cancel", summary, false, now, false}
	if err = persistReplay(ctx, tx, base.ServerURL, base.UserID, in.RequestID, "mm/v2/stage-cancel-request", digest, result, stamp); err != nil {
		return MutationResult{}, err
	}
	if err = tx.Commit(); err != nil {
		return MutationResult{}, localError(err)
	}
	runCommitHook()
	return result, nil
}

func (s *Store) Show(ctx context.Context, id string) (StageDetail, error) {
	if !bounded(id, maxIdentityBytes) {
		return StageDetail{}, ErrInvalid
	}
	detail, err := scanDetail(s.db.QueryRowContext(ctx, currentDetailSQL, id))
	if err != nil {
		return detail, err
	}
	detail.Attachments, err = readAttachments(ctx, s.db, id, detail.Revision)
	return detail, err
}
func (s *Store) List(ctx context.Context, o ListOptions) ([]StageSummary, error) {
	limit := o.Limit
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > maxListLimit {
		return nil, ErrInvalid
	}
	rows, err := s.db.QueryContext(ctx, `SELECT s.id,s.server_url,coalesce(s.server_id,''),s.user_id,s.operation,s.lifecycle,s.recovery,r.revision,r.semantic_digest,s.created_at,s.updated_at FROM stages s JOIN stage_revisions r ON r.stage_id=s.id AND r.revision=s.current_revision ORDER BY s.updated_at DESC,s.id ASC LIMIT ?`, limit)
	if err != nil {
		return nil, localError(err)
	}
	defer rows.Close()
	out := make([]StageSummary, 0)
	for rows.Next() {
		v, e := scanSummary(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	if err = rows.Err(); err != nil {
		return nil, localError(err)
	}
	return out, nil
}

const currentDetailSQL = `SELECT s.id,s.server_url,coalesce(s.server_id,''),s.user_id,s.operation,s.lifecycle,s.recovery,r.revision,r.semantic_digest,s.created_at,s.updated_at,r.created_at,r.body,r.destination_json,r.plan_json FROM stages s JOIN stage_revisions r ON r.stage_id=s.id AND r.revision=s.current_revision WHERE s.id=?`

type rowScanner interface{ Scan(...any) error }

func scanSummary(row rowScanner) (StageSummary, error) {
	var v StageSummary
	var digest []byte
	var created, updated string
	err := row.Scan(&v.ID, &v.ServerURL, &v.ServerID, &v.UserID, &v.Operation, &v.Lifecycle, &v.Recovery, &v.Revision, &digest, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return v, ErrNotFound
	}
	if err != nil {
		return v, localError(err)
	}
	if len(digest) != 32 {
		return v, localError(errors.New("digest"))
	}
	copy(v.SemanticDigest[:], digest)
	if v.CreatedAt, err = parseTime(created); err != nil {
		return v, err
	}
	if v.UpdatedAt, err = parseTime(updated); err != nil {
		return v, err
	}
	return v, nil
}
func scanDetail(row rowScanner) (StageDetail, error) {
	var v StageDetail
	var digest, body []byte
	var created, updated, revCreated, destination, plan string
	err := row.Scan(&v.ID, &v.ServerURL, &v.ServerID, &v.UserID, &v.Operation, &v.Lifecycle, &v.Recovery, &v.Revision, &digest, &created, &updated, &revCreated, &body, &destination, &plan)
	if errors.Is(err, sql.ErrNoRows) {
		return v, ErrNotFound
	}
	if err != nil {
		return v, localError(err)
	}
	if len(digest) != 32 {
		return v, localError(errors.New("digest"))
	}
	copy(v.SemanticDigest[:], digest)
	if v.CreatedAt, err = parseTime(created); err != nil {
		return v, err
	}
	if v.UpdatedAt, err = parseTime(updated); err != nil {
		return v, err
	}
	if v.RevisionCreatedAt, err = parseTime(revCreated); err != nil {
		return v, err
	}
	v.Body = bytes.Clone(body)
	v.Destination = json.RawMessage(destination)
	v.Plan = json.RawMessage(plan)
	return v, nil
}
func scanCurrent(ctx context.Context, tx *sql.Tx, id string) (StageDetail, error) {
	return scanDetail(tx.QueryRowContext(ctx, currentDetailSQL, id))
}

type semanticContent struct {
	Body        []byte          `json:"body,omitempty"`
	Destination json.RawMessage `json:"destination"`
	Plan        json.RawMessage `json:"plan"`
	Attachments []Attachment    `json:"attachments,omitempty"`
}

func (v RevisionContent) semantic() semanticContent {
	return semanticContent{v.Body, v.Destination, v.Plan, v.Attachments}
}
func semanticDigest(op Operation, server, serverID, user string, c RevisionContent) [32]byte {
	return digestValue(struct {
		Operation Operation       `json:"operation"`
		ServerURL string          `json:"serverUrl"`
		ServerID  string          `json:"serverId,omitempty"`
		UserID    string          `json:"userId"`
		Content   semanticContent `json:"content"`
	}{op, server, serverID, user, c.semantic()})
}
func normalizeContent(op Operation, v RevisionContent) (RevisionContent, error) {
	destination, err := canonicalObject(v.Destination)
	if err != nil {
		return v, err
	}
	plan, err := canonicalObject(v.Plan)
	if err != nil {
		return v, err
	}
	v.Destination, v.Plan = destination, plan
	composition, err := normalizeComposition(op, Composition{v.Body, v.Attachments})
	if err != nil {
		return v, err
	}
	v.Body, v.Attachments = composition.Body, composition.Attachments
	return v, nil
}
func normalizeComposition(op Operation, v Composition) (Composition, error) {
	v.Body = bytes.Clone(v.Body)
	v.Attachments = append([]Attachment(nil), v.Attachments...)
	if len(v.Attachments) > maxAttachments {
		return v, ErrInvalid
	}
	switch op {
	case CreatePost, Reply, EditPost:
		if err := messageinput.Validate(v.Body); err != nil {
			return v, ErrInvalid
		}
	default:
		if len(v.Body) != 0 {
			return v, ErrInvalid
		}
	}
	if op != CreatePost && op != Reply && len(v.Attachments) > 0 {
		return v, ErrInvalid
	}
	for _, a := range v.Attachments {
		if !boundedMetadata(a.SuppliedPath, maxIdentityBytes) || !boundedMetadata(a.CanonicalPath, maxIdentityBytes) || !boundedMetadata(a.RemoteFilename, maxFilenameBytes) || a.ByteLength < 0 || a.ContentDigest == ([32]byte{}) || (a.MediaType != "" && !boundedMetadata(a.MediaType, maxMediaTypeBytes)) {
			return v, ErrInvalid
		}
	}
	return v, nil
}

func canonicalObject(raw []byte) (json.RawMessage, error) {
	if len(raw) == 0 || len(raw) > maxJSONBytes || !utf8.Valid(raw) || !validJSONStringEscapes(raw) {
		return nil, ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	value, err := decodeUnique(d)
	if err != nil {
		return nil, ErrInvalid
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, ErrInvalid
	}
	if err = d.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, ErrInvalid
	}
	var out bytes.Buffer
	e := json.NewEncoder(&out)
	e.SetEscapeHTML(false)
	if e.Encode(value) != nil {
		return nil, ErrInvalid
	}
	return restoreLineSeparators(bytes.TrimSuffix(out.Bytes(), []byte("\n"))), nil
}
func decodeUnique(d *json.Decoder) (any, error) {
	token, err := d.Token()
	if err != nil {
		return nil, err
	}
	switch token := token.(type) {
	case json.Delim:
		switch token {
		case '{':
			m := map[string]any{}
			for d.More() {
				keyToken, e := d.Token()
				if e != nil {
					return nil, e
				}
				key, ok := keyToken.(string)
				if !ok {
					return nil, ErrInvalid
				}
				if _, exists := m[key]; exists {
					return nil, ErrInvalid
				}
				value, e := decodeUnique(d)
				if e != nil {
					return nil, e
				}
				m[key] = value
			}
			_, err = d.Token()
			return m, err
		case '[':
			a := []any{}
			for d.More() {
				v, e := decodeUnique(d)
				if e != nil {
					return nil, e
				}
				a = append(a, v)
			}
			_, err = d.Token()
			return a, err
		default:
			return nil, ErrInvalid
		}
	default:
		return token, nil
	}
}
func validJSONStringEscapes(raw []byte) bool {
	in := false
	for i := 0; i < len(raw); i++ {
		if !in {
			if raw[i] == '"' {
				in = true
			}
			continue
		}
		if raw[i] == '"' {
			in = false
			continue
		}
		if raw[i] != '\\' {
			continue
		}
		i++
		if i >= len(raw) {
			return false
		}
		if raw[i] != 'u' {
			continue
		}
		if i+4 >= len(raw) {
			return false
		}
		code, ok := hex4(raw[i+1 : i+5])
		if !ok {
			return false
		}
		i += 4
		if code >= 0xD800 && code <= 0xDBFF {
			if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
				return false
			}
			low, ok := hex4(raw[i+3 : i+7])
			if !ok || low < 0xDC00 || low > 0xDFFF {
				return false
			}
			i += 6
		} else if code >= 0xDC00 && code <= 0xDFFF {
			return false
		}
	}
	return !in
}
func hex4(v []byte) (uint16, bool) {
	var out uint16
	for _, c := range v {
		out <<= 4
		switch {
		case c >= '0' && c <= '9':
			out += uint16(c - '0')
		case c >= 'a' && c <= 'f':
			out += uint16(c - 'a' + 10)
		case c >= 'A' && c <= 'F':
			out += uint16(c - 'A' + 10)
		default:
			return 0, false
		}
	}
	return out, true
}

func insertAttachments(ctx context.Context, tx *sql.Tx, stage string, revision int64, values []Attachment) error {
	for i, a := range values {
		if _, err := tx.ExecContext(ctx, `INSERT INTO stage_attachments(stage_id,revision,ordinal,supplied_path,canonical_path,remote_filename,byte_length,media_type,content_digest) VALUES(?,?,?,?,?,?,?,?,?)`, stage, revision, i, a.SuppliedPath, a.CanonicalPath, a.RemoteFilename, a.ByteLength, nullable(a.MediaType), a.ContentDigest[:]); err != nil {
			return localError(err)
		}
	}
	return nil
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readAttachments(ctx context.Context, q queryer, stage string, revision int64) ([]Attachment, error) {
	rows, err := q.QueryContext(ctx, `SELECT supplied_path,canonical_path,remote_filename,byte_length,coalesce(media_type,''),content_digest FROM stage_attachments WHERE stage_id=? AND revision=? ORDER BY ordinal`, stage, revision)
	if err != nil {
		return nil, localError(err)
	}
	defer rows.Close()
	out := make([]Attachment, 0)
	for rows.Next() {
		var a Attachment
		var digest []byte
		if err = rows.Scan(&a.SuppliedPath, &a.CanonicalPath, &a.RemoteFilename, &a.ByteLength, &a.MediaType, &digest); err != nil {
			return nil, localError(err)
		}
		if len(digest) != 32 {
			return nil, localError(errors.New("digest"))
		}
		copy(a.ContentDigest[:], digest)
		out = append(out, a)
	}
	return out, localError(rows.Err())
}

func loadReplay(ctx context.Context, tx *sql.Tx, server, user, id, schema string, digest [32]byte) (MutationResult, bool, error) {
	var storedSchema, raw, created string
	var stored []byte
	err := tx.QueryRowContext(ctx, `SELECT request_schema,request_digest,result_json,created_at FROM local_requests WHERE server_url=? AND user_id=? AND request_id=?`, server, user, id).Scan(&storedSchema, &stored, &raw, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return MutationResult{}, false, nil
	}
	if err != nil {
		return MutationResult{}, false, localError(err)
	}
	if storedSchema != schema || !bytes.Equal(stored, digest[:]) {
		return MutationResult{}, false, ErrConflict
	}
	var result MutationResult
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	recordedAt, timeErr := parseTime(created)
	if decoder.Decode(&result) != nil || decoder.Decode(new(any)) != io.EOF || timeErr != nil || !result.RecordedAt.Equal(recordedAt) || !validReplayResult(result, schema, server, user) {
		return MutationResult{}, false, localError(errors.New("receipt"))
	}
	result.Replay = true
	return result, true, nil
}

func (s *Store) FindCreate(ctx context.Context, server, user, id string) (CreateRecord, bool, error) {
	if ctx == nil || !canonicalServerURL(server) || !bounded(user, maxIdentityBytes) || !validRequestID(id) {
		return CreateRecord{}, false, ErrInvalid
	}
	record, found, err := findCreate(ctx, s.db, server, user, id)
	if found {
		record.Replay, record.Result.Replay = true, true
	}
	return record, found, err
}

func findCreate(ctx context.Context, q queryer, server, user, id string) (CreateRecord, bool, error) {
	var schemaName, raw, requestCreated string
	var digest []byte
	err := q.QueryRowContext(ctx, `SELECT request_schema,request_digest,result_json,created_at FROM local_requests WHERE server_url=? AND user_id=? AND request_id=?`, server, user, id).Scan(&schemaName, &digest, &raw, &requestCreated)
	if errors.Is(err, sql.ErrNoRows) {
		return CreateRecord{}, false, nil
	}
	if err != nil {
		return CreateRecord{}, false, localError(err)
	}
	if schemaName == "mm/v2/legacy-stage-request-conflict" || schemaName == "mm/v2/legacy-request-conflict" || schemaName == "mm/v2/stage-revise-request" || schemaName == "mm/v2/stage-cancel-request" {
		return CreateRecord{}, false, ErrConflict
	}
	if schemaName != "mm/v2/stage-request" || len(digest) != 32 {
		return CreateRecord{}, false, localError(errors.New("create receipt"))
	}
	var record CreateRecord
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	createdAt, createdErr := parseTime(requestCreated)
	if decoder.Decode(&record) != nil || decoder.Decode(new(any)) != io.EOF || createdErr != nil || !record.Result.RecordedAt.Equal(createdAt) || !bytes.Equal(digest, record.RequestDigest[:]) || !validReplayResult(record.Result, "mm/v2/stage-request", server, user) {
		return CreateRecord{}, false, localError(errors.New("create receipt"))
	}
	record.MutationResult = record.Result
	stage := record.Stage
	if stage.Revision != 1 || stage.Lifecycle != LifecycleOpen || stage.Recovery != RecoveryNone || record.Revived ||
		!stage.CreatedAt.Equal(stage.UpdatedAt) || !stage.CreatedAt.Equal(record.RecordedAt) {
		return CreateRecord{}, false, localError(errors.New("create receipt"))
	}
	var operation Operation
	var stageServer, serverID, stageUser, stageCreated, revisionCreated string
	var body []byte
	var destination, plan string
	var semantic []byte
	err = q.QueryRowContext(ctx, `SELECT s.operation,s.server_url,coalesce(s.server_id,''),s.user_id,s.created_at,r.created_at,r.body,r.destination_json,r.plan_json,r.semantic_digest FROM stages s JOIN stage_revisions r ON r.stage_id=s.id AND r.revision=? WHERE s.id=?`, stage.Revision, stage.ID).Scan(&operation, &stageServer, &serverID, &stageUser, &stageCreated, &revisionCreated, &body, &destination, &plan, &semantic)
	if err != nil {
		return CreateRecord{}, false, localError(err)
	}
	attachments, err := readAttachments(ctx, q, stage.ID, stage.Revision)
	if err != nil {
		return CreateRecord{}, false, err
	}
	content, err := normalizeContent(operation, RevisionContent{body, json.RawMessage(destination), json.RawMessage(plan), attachments})
	recordDestination, destinationErr := canonicalObject(record.Destination)
	recordPlan, planErr := canonicalObject(record.Plan)
	stageCreatedAt, stageCreatedErr := parseTime(stageCreated)
	revisionCreatedAt, revisionCreatedErr := parseTime(revisionCreated)
	if err != nil || destinationErr != nil || planErr != nil || stageCreatedErr != nil || revisionCreatedErr != nil || !stage.CreatedAt.Equal(stageCreatedAt) || !stage.CreatedAt.Equal(revisionCreatedAt) || len(semantic) != 32 || !bytes.Equal(semantic, stage.SemanticDigest[:]) || semanticDigest(operation, server, serverID, user, content) != stage.SemanticDigest ||
		!bytes.Equal(content.Destination, recordDestination) || !bytes.Equal(content.Plan, recordPlan) || operation != stage.Operation || stageServer != server || stageUser != user || serverID != stage.ServerID {
		return CreateRecord{}, false, localError(errors.New("create projection"))
	}
	record.Destination, record.Plan = bytes.Clone(record.Destination), bytes.Clone(record.Plan)
	return record, true, nil
}

func normalizeCreateProjection(destination, plan json.RawMessage) (json.RawMessage, json.RawMessage, error) {
	projection := struct {
		Destination json.RawMessage `json:"destination"`
		Plan        json.RawMessage `json:"plan"`
	}{destination, plan}
	raw, err := marshalCanonical(projection)
	if err != nil {
		return nil, nil, err
	}
	var normalized struct {
		Destination json.RawMessage `json:"destination"`
		Plan        json.RawMessage `json:"plan"`
	}
	if err = json.Unmarshal(raw, &normalized); err != nil {
		return nil, nil, err
	}
	return normalized.Destination, normalized.Plan, nil
}

func persistCreate(ctx context.Context, tx *sql.Tx, server, user, id string, record CreateRecord, stamp string) error {
	if id == "" {
		return nil
	}
	raw, err := marshalCanonical(record)
	if err != nil {
		return localError(err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO local_requests(server_url,user_id,request_id,request_schema,request_digest,result_json,created_at) VALUES(?,?,?,?,?,?,?)`, server, user, id, "mm/v2/stage-request", record.RequestDigest[:], string(raw), stamp)
	return localError(err)
}
func validReplayResult(result MutationResult, requestSchema, server, user string) bool {
	action := map[string]string{"mm/v2/stage-request": "create", "mm/v2/stage-revise-request": "revise", "mm/v2/stage-cancel-request": "cancel"}[requestSchema]
	stage := result.Stage
	return action != "" && result.Schema == "mm/v2/stage-mutation-receipt" && result.Action == action && stage.ServerURL == server && stage.UserID == user &&
		bounded(stage.ID, maxIdentityBytes) && validOperation(stage.Operation) && stage.Revision > 0 && stage.SemanticDigest != ([32]byte{}) && validLifecycle(stage.Lifecycle) && validRecovery(stage.Recovery) &&
		!stage.CreatedAt.IsZero() && !stage.UpdatedAt.IsZero() && !result.RecordedAt.IsZero() && !stage.UpdatedAt.Before(stage.CreatedAt) && !result.RecordedAt.Before(stage.UpdatedAt)
}
func validLifecycle(v Lifecycle) bool {
	switch v {
	case LifecycleOpen, LifecycleApplying, LifecycleCompleted, LifecycleCanceled, LifecycleExpired, LifecyclePruned:
		return true
	}
	return false
}
func validRecovery(v Recovery) bool {
	switch v {
	case RecoveryNone, RecoveryPartial, RecoveryUnknown, RecoveryForbidden:
		return true
	}
	return false
}
func persistReplay(ctx context.Context, tx *sql.Tx, server, user, id, schema string, digest [32]byte, result MutationResult, stamp string) error {
	if id == "" {
		return nil
	}
	raw, err := marshalCanonical(result)
	if err != nil {
		return localError(err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO local_requests(server_url,user_id,request_id,request_schema,request_digest,result_json,created_at) VALUES(?,?,?,?,?,?,?)`, server, user, id, schema, digest[:], string(raw), stamp)
	return localError(err)
}
func digestValue(v any) [32]byte { raw, _ := marshalCanonical(v); return sha256.Sum256(raw) }
func marshalCanonical(v any) ([]byte, error) {
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(v); err != nil {
		return nil, err
	}
	return restoreLineSeparators(bytes.TrimSuffix(out.Bytes(), []byte("\n"))), nil
}
func restoreLineSeparators(data []byte) []byte {
	var out bytes.Buffer
	out.Grow(len(data))
	for i := 0; i < len(data); {
		if i+6 <= len(data) && data[i] == '\\' && (bytes.Equal(data[i:i+6], []byte(`\u2028`)) || bytes.Equal(data[i:i+6], []byte(`\u2029`))) {
			slashes := 0
			for cursor := i - 1; cursor >= 0 && data[cursor] == '\\'; cursor-- {
				slashes++
			}
			if slashes%2 == 0 {
				if data[i+5] == '8' {
					out.WriteString("\u2028")
				} else {
					out.WriteString("\u2029")
				}
				i += 6
				continue
			}
		}
		out.WriteByte(data[i])
		i++
	}
	return out.Bytes()
}
func newStageID() (string, error) {
	v := make([]byte, 24)
	if _, err := rand.Read(v); err != nil {
		return "", err
	}
	return "stg_" + base64.RawURLEncoding.EncodeToString(v), nil
}
func validOperation(v Operation) bool {
	switch v {
	case CreatePost, Reply, EditPost, DeletePost, React, Unreact, ResolveDM, ResolveGroupDM:
		return true
	}
	return false
}
func bounded(v string, max int) bool {
	return v != "" && len(v) <= max && utf8.ValidString(v) && strings.TrimSpace(v) == v
}
func boundedMetadata(v string, max int) bool {
	if !bounded(v, max) {
		return false
	}
	for _, r := range v {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func validRequestID(v string) bool {
	if v == "" {
		return true
	}
	if len(v) > maxRequestID || !requestChar(v[0], true) {
		return false
	}
	for i := 1; i < len(v); i++ {
		if !requestChar(v[i], false) {
			return false
		}
	}
	return true
}
func requestChar(c byte, first bool) bool {
	if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
		return true
	}
	return !first && strings.ContainsRune("._~:-", rune(c))
}
func canonicalServerURL(v string) bool {
	if !bounded(v, maxIdentityBytes) {
		return false
	}
	normalized, err := serverurl.Normalize(v)
	return err == nil && normalized == v
}
func nullable(v string) any {
	if v == "" {
		return nil
	}
	return v
}
func nullableBytes(v []byte) any {
	if v == nil {
		return nil
	}
	return v
}
func parseTime(v string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		return t, localError(err)
	}
	return t, nil
}
func formatTime(v time.Time) string { return v.UTC().Format("2006-01-02T15:04:05.000000000Z") }
func oneRow(v sql.Result) bool      { n, err := v.RowsAffected(); return err == nil && n == 1 }
func runCommitHook() {
	commitHook.RLock()
	fn := commitHook.fn
	commitHook.RUnlock()
	if fn != nil {
		fn()
	}
}
