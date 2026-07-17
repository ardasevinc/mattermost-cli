package staging

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/ardasevinc/mattermost-cli/internal/messageinput"
	"github.com/ardasevinc/mattermost-cli/internal/stageinput"
	"github.com/ardasevinc/mattermost-cli/internal/stagestore"
)

var ErrNotEligible = errors.New("staging: lifecycle transition not allowed")

// Reviser admits offline revision and cancellation requests without resolving
// or mutating their immutable destination.
type Reviser struct {
	store       RevisionStore
	bind        AttachmentBinder
	credentials [][]byte
}

func NewReviser(credentials []string, store RevisionStore, bind AttachmentBinder) (*Reviser, error) {
	protected := credentialBytes(credentials)
	if nilDependency(store) || bind == nil || len(protected) > 64 {
		return nil, ErrInvalid
	}
	total := 0
	for _, credential := range protected {
		total += len(credential)
		if len(credential) > 4096 || total > 64<<10 {
			return nil, ErrInvalid
		}
	}
	return &Reviser{store: store, bind: bind, credentials: protected}, nil
}

func (r *Reviser) Revise(ctx context.Context, in ReviseInput) (RevisionResult, error) {
	if ctx == nil || !validStageMutation(in.StageID, in.RequestID, in.ExpectedRevision) {
		return RevisionResult{}, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return RevisionResult{}, err
	}
	if contaminated(r.credentials, in.StageID, in.RequestID, hex.EncodeToString(in.ExpectedDigest[:])) || callerAttachmentsContaminated(r.credentials, in.Attachments) {
		return RevisionResult{}, ErrCredential
	}
	var attachmentIntent []stageinput.MetadataIntent
	var err error
	if in.Attachments != nil {
		attachmentIntent, err = stageinput.Preflight(in.Attachments)
		if err != nil {
			return RevisionResult{}, ErrInput
		}
		if attachmentIntentContaminated(r.credentials, attachmentIntent) {
			return RevisionResult{}, ErrCredential
		}
	}
	detail, err := r.show(ctx, in.StageID)
	if err != nil {
		return RevisionResult{}, err
	}
	if detail.ID != in.StageID {
		return RevisionResult{}, ErrStore
	}
	if detail.Recovery == stagestore.RecoveryPartial {
		return RevisionResult{}, ErrNotEligible
	}
	if detail.Operation != stagestore.CreatePost && detail.Operation != stagestore.Reply && detail.Operation != stagestore.EditPost {
		return RevisionResult{}, ErrNotEligible
	}
	if attachmentsContaminated(r.credentials, detail.Attachments) {
		return RevisionResult{}, ErrCredential
	}
	if detail.Operation == stagestore.EditPost && in.Attachments != nil {
		return RevisionResult{}, ErrNotEligible
	}
	var body []byte
	if in.Body == nil {
		body = bytes.Clone(detail.Body)
	} else {
		body, err = messageinput.Read(in.Body)
		if err != nil {
			return RevisionResult{}, ErrInput
		}
	}
	if containsCredential(r.credentials, body) {
		return RevisionResult{}, ErrCredential
	}
	var suppliedBody []byte
	if in.Body != nil {
		suppliedBody = body
	}
	requestDigest := revisionRequestDigest(detail.Operation, revisionIntent{in.StageID, in.ExpectedRevision, in.ExpectedDigest, in.Revive}, suppliedBody, attachmentIntent)
	if in.RequestID != "" {
		replayed, found, findErr := r.store.FindRevise(ctx, detail.ServerURL, detail.UserID, in.RequestID, requestDigest)
		if findErr != nil {
			return RevisionResult{}, mapRevisionStoreError(findErr)
		}
		if found {
			if replayed.Stage.ID != in.StageID || replayed.Stage.Operation != detail.Operation || replayed.Stage.ServerURL != detail.ServerURL || replayed.Stage.UserID != detail.UserID {
				return RevisionResult{}, ErrStore
			}
			return RevisionResult{Stored: replayed, Destination: bytes.Clone(detail.Destination)}, nil
		}
	}
	attachments := append([]stagestore.Attachment(nil), detail.Attachments...)
	if in.Attachments != nil {
		attachments, err = r.bind(ctx, in.Attachments, cloneCredentials(r.credentials))
		if err != nil {
			return RevisionResult{}, mapBinderError(err)
		}
		if len(attachments) != len(in.Attachments) || !validBoundAttachments(attachments) {
			return RevisionResult{}, ErrInput
		}
		if attachmentsContaminated(r.credentials, attachments) {
			return RevisionResult{}, ErrCredential
		}
	}
	if err := ctx.Err(); err != nil {
		return RevisionResult{}, err
	}
	plan, err := json.Marshal(compositionPlan(detail.Operation, len(attachments)))
	if err != nil {
		return RevisionResult{}, ErrStore
	}
	stored, err := r.store.Revise(ctx, stagestore.ReviseInput{StageID: in.StageID, RequestID: in.RequestID, ExpectedRevision: in.ExpectedRevision,
		ExpectedDigest: in.ExpectedDigest, RequestDigest: requestDigest, Revive: in.Revive, Composition: stagestore.Composition{Body: bytes.Clone(body), Plan: plan, Attachments: attachments}})
	if err != nil {
		return RevisionResult{}, mapRevisionStoreError(err)
	}
	return RevisionResult{Stored: stored, Destination: bytes.Clone(detail.Destination)}, nil
}

func (r *Reviser) Cancel(ctx context.Context, in CancelInput) (RevisionResult, error) {
	if ctx == nil || !validStageMutation(in.StageID, in.RequestID, in.ExpectedRevision) {
		return RevisionResult{}, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return RevisionResult{}, err
	}
	if contaminated(r.credentials, in.StageID, in.RequestID, hex.EncodeToString(in.ExpectedDigest[:])) {
		return RevisionResult{}, ErrCredential
	}
	detail, err := r.show(ctx, in.StageID)
	if err != nil {
		return RevisionResult{}, err
	}
	if detail.ID != in.StageID {
		return RevisionResult{}, ErrStore
	}
	if err := ctx.Err(); err != nil {
		return RevisionResult{}, err
	}
	stored, err := r.store.Cancel(ctx, stagestore.CancelInput{StageID: in.StageID, RequestID: in.RequestID, ExpectedRevision: in.ExpectedRevision, ExpectedDigest: in.ExpectedDigest})
	if err != nil {
		return RevisionResult{}, mapRevisionStoreError(err)
	}
	if stored.Stage.ID != in.StageID || stored.Stage.Operation != detail.Operation || stored.Stage.ServerURL != detail.ServerURL || stored.Stage.UserID != detail.UserID {
		return RevisionResult{}, ErrStore
	}
	return RevisionResult{Stored: stored, Destination: bytes.Clone(detail.Destination)}, nil
}

func (r *Reviser) show(ctx context.Context, stageID string) (stagestore.StageDetail, error) {
	detail, err := r.store.Show(ctx, stageID)
	if err != nil {
		return stagestore.StageDetail{}, mapRevisionStoreError(err)
	}
	return detail, nil
}

func validStageMutation(stageID, requestID string, revision int64) bool {
	return validSelectorValue(stageID) && validRequestID(requestID) && revision > 0 && revision <= 9_007_199_254_740_991
}

func mapBinderError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, stageinput.ErrCredential) {
		return ErrCredential
	}
	return ErrInput
}

func mapRevisionStoreError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, stagestore.ErrConflict) {
		return ErrConflict
	}
	if errors.Is(err, stagestore.ErrNotFound) {
		return ErrNotFound
	}
	if errors.Is(err, stagestore.ErrNotEligible) {
		return ErrNotEligible
	}
	return ErrStore
}
