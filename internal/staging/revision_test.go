package staging

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/ardasevinc/mattermost-cli/internal/stageinput"
	"github.com/ardasevinc/mattermost-cli/internal/stagestore"
)

type revisionStoreStub struct {
	detail                              stagestore.StageDetail
	showErr, reviseErr, cancelErr       error
	findErr                             error
	findResult                          stagestore.MutationResult
	findFound                           bool
	findCalls, reviseCalls, cancelCalls int
	findDigest                          [32]byte
	reviseIn                            stagestore.ReviseInput
}

func (s *revisionStoreStub) Show(context.Context, string) (stagestore.StageDetail, error) {
	return s.detail, s.showErr
}
func (s *revisionStoreStub) Revise(_ context.Context, in stagestore.ReviseInput) (stagestore.MutationResult, error) {
	s.reviseCalls++
	s.reviseIn = in
	return stagestore.MutationResult{Action: "revise"}, s.reviseErr
}
func (s *revisionStoreStub) FindRevise(_ context.Context, _, _, _ string, digest [32]byte) (stagestore.MutationResult, bool, error) {
	s.findCalls++
	s.findDigest = digest
	return s.findResult, s.findFound, s.findErr
}
func (s *revisionStoreStub) Cancel(context.Context, stagestore.CancelInput) (stagestore.MutationResult, error) {
	s.cancelCalls++
	return stagestore.MutationResult{Action: "cancel", Stage: s.detail.StageSummary}, s.cancelErr
}

func revisionFixture(operation stagestore.Operation) (*revisionStoreStub, [32]byte) {
	digest := [32]byte{1}
	return &revisionStoreStub{detail: stagestore.StageDetail{StageSummary: stagestore.StageSummary{ID: "stage-1", Operation: operation, Revision: 1, SemanticDigest: digest}, Destination: []byte(`{"kind":"conversation"}`)}}, digest
}

func TestReviserPreservesBodyAttachmentOrderAndDestinationSnapshot(t *testing.T) {
	store, digest := revisionFixture(stagestore.Reply)
	binder := func(_ context.Context, in []stageinput.Attachment, credentials [][]byte) ([]stagestore.Attachment, error) {
		if len(in) != 2 || string(credentials[0]) != "active-token" {
			t.Fatal("binder did not receive ordered inputs and protected credential")
		}
		return []stagestore.Attachment{
			{SuppliedPath: "/a", CanonicalPath: "/a", RemoteFilename: "a", ByteLength: 1, ContentDigest: [32]byte{1}},
			{SuppliedPath: "/b", CanonicalPath: "/b", RemoteFilename: "b", ByteLength: 1, ContentDigest: [32]byte{2}},
		}, nil
	}
	r, err := NewReviser([]string{"active-token"}, store, binder)
	if err != nil {
		t.Fatal(err)
	}
	result, err := r.Revise(context.Background(), ReviseInput{StageID: "stage-1", RequestID: "request-1", ExpectedRevision: 1, ExpectedDigest: digest,
		Body: strings.NewReader(" exact body\n"), Attachments: []Attachment{{Path: "/a"}, {Path: "/b"}}})
	if err != nil || store.reviseCalls != 1 || string(store.reviseIn.Composition.Body) != " exact body\n" || store.reviseIn.Composition.Attachments[1].RemoteFilename != "b" {
		t.Fatalf("unexpected revision: result=%+v err=%v calls=%d input=%+v", result, err, store.reviseCalls, store.reviseIn)
	}
	if string(store.reviseIn.Composition.Plan) != `{"steps":[{"ordinal":1,"type":"upload_attachment","condition":"always"},{"ordinal":2,"type":"upload_attachment","condition":"always"},{"ordinal":3,"type":"create_post","condition":"always"}]}` {
		t.Fatalf("revision plan = %s", store.reviseIn.Composition.Plan)
	}
	store.detail.Destination[0] = 'x'
	if string(result.Destination) != `{"kind":"conversation"}` {
		t.Fatal("result destination aliases store detail")
	}
}

func TestReviserRejectsBeforeStoreMutation(t *testing.T) {
	for _, tc := range []struct {
		name, body  string
		op          stagestore.Operation
		attachments []Attachment
		want        error
	}{
		{"credential body", "before active-token after", stagestore.Reply, nil, ErrCredential},
		{"oversized body", strings.Repeat("x", 65536), stagestore.Reply, nil, ErrInput},
		{"inapplicable operation", "body", stagestore.DeletePost, nil, ErrNotEligible},
		{"edit attachments", "body", stagestore.EditPost, []Attachment{{Path: "/a"}}, ErrNotEligible},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, digest := revisionFixture(tc.op)
			r, _ := NewReviser([]string{"active-token"}, store, func(context.Context, []stageinput.Attachment, [][]byte) ([]stagestore.Attachment, error) {
				t.Fatal("unexpected bind")
				return nil, nil
			})
			_, err := r.Revise(context.Background(), ReviseInput{StageID: "stage-1", RequestID: "request-1", ExpectedRevision: 1, ExpectedDigest: digest, Body: strings.NewReader(tc.body), Attachments: tc.attachments})
			if !errors.Is(err, tc.want) || store.reviseCalls != 0 {
				t.Fatalf("err=%v calls=%d", err, store.reviseCalls)
			}
		})
	}
}

func TestReviserMapsMutationErrorsAndPreservesCancellation(t *testing.T) {
	for _, tc := range []struct{ source, want error }{{stagestore.ErrConflict, ErrConflict}, {stagestore.ErrNotFound, ErrNotFound}, {stagestore.ErrNotEligible, ErrNotEligible}, {errors.New("secret backend detail"), ErrStore}, {context.Canceled, context.Canceled}} {
		store, digest := revisionFixture(stagestore.EditPost)
		store.reviseErr = tc.source
		r, _ := NewReviser(nil, store, stageinput.Bind)
		_, err := r.Revise(context.Background(), ReviseInput{StageID: "stage-1", RequestID: "request-1", ExpectedRevision: 1, ExpectedDigest: digest, Body: bytes.NewBufferString("body")})
		if !errors.Is(err, tc.want) || store.reviseCalls != 1 {
			t.Fatalf("source=%v err=%v calls=%d", tc.source, err, store.reviseCalls)
		}
	}
}

func TestReviserCancelCallsStoreOnceAndReturnsPreloadedDestination(t *testing.T) {
	store, digest := revisionFixture(stagestore.CreatePost)
	r, _ := NewReviser(nil, store, stageinput.Bind)
	result, err := r.Cancel(context.Background(), CancelInput{StageID: "stage-1", RequestID: "cancel-1", ExpectedRevision: 1, ExpectedDigest: digest})
	if err != nil || store.cancelCalls != 1 || result.Stored.Action != "cancel" || string(result.Destination) != `{"kind":"conversation"}` {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, store.cancelCalls)
	}
}

func TestReviserReplayDoesNotOpenAttachment(t *testing.T) {
	store, digest := revisionFixture(stagestore.Reply)
	store.detail.ServerURL, store.detail.UserID = "https://mattermost.example/api/v4", "user-1"
	store.findFound = true
	store.findResult = stagestore.MutationResult{Action: "revise", Replay: true, Stage: store.detail.StageSummary}
	r, _ := NewReviser(nil, store, func(context.Context, []stageinput.Attachment, [][]byte) ([]stagestore.Attachment, error) {
		t.Fatal("replay attempted attachment binding")
		return nil, nil
	})
	result, err := r.Revise(context.Background(), ReviseInput{StageID: "stage-1", RequestID: "replay-1", ExpectedRevision: 1, ExpectedDigest: digest,
		Body: strings.NewReader("body"), Attachments: []Attachment{{Path: "/missing-after-first-attempt"}}})
	if err != nil || !result.Stored.Replay || store.findCalls != 1 || store.reviseCalls != 0 || store.findDigest == ([32]byte{}) {
		t.Fatalf("result=%+v err=%v find=%d revise=%d", result, err, store.findCalls, store.reviseCalls)
	}
}

func TestReviserNilBodyAndAttachmentsPreserveCurrentComposition(t *testing.T) {
	store, digest := revisionFixture(stagestore.Reply)
	store.detail.Body = []byte("existing body")
	store.detail.Attachments = []stagestore.Attachment{{SuppliedPath: "/a", CanonicalPath: "/a", RemoteFilename: "a", ByteLength: 1, ContentDigest: [32]byte{1}}}
	r, _ := NewReviser(nil, store, func(context.Context, []stageinput.Attachment, [][]byte) ([]stagestore.Attachment, error) {
		t.Fatal("preserve attempted attachment binding")
		return nil, nil
	})
	_, err := r.Revise(context.Background(), ReviseInput{StageID: "stage-1", ExpectedRevision: 1, ExpectedDigest: digest})
	if err != nil || string(store.reviseIn.Composition.Body) != "existing body" || len(store.reviseIn.Composition.Attachments) != 1 {
		t.Fatalf("err=%v input=%+v", err, store.reviseIn)
	}
	if string(store.reviseIn.Composition.Plan) != `{"steps":[{"ordinal":1,"type":"upload_attachment","condition":"always"},{"ordinal":2,"type":"create_post","condition":"always"}]}` {
		t.Fatalf("preserved attachment plan = %s", store.reviseIn.Composition.Plan)
	}
	store.detail.Body[0] = 'X'
	store.detail.Attachments[0].SuppliedPath = "/changed"
	if string(store.reviseIn.Composition.Body) != "existing body" || store.reviseIn.Composition.Attachments[0].SuppliedPath != "/a" {
		t.Fatal("preserved composition aliases store detail")
	}
}

func TestReviserClearingAttachmentsRemovesUploadSteps(t *testing.T) {
	store, digest := revisionFixture(stagestore.CreatePost)
	store.detail.Body = []byte("existing body")
	store.detail.Attachments = []stagestore.Attachment{{SuppliedPath: "/a", CanonicalPath: "/a", RemoteFilename: "a", ByteLength: 1, ContentDigest: [32]byte{1}}}
	r, _ := NewReviser(nil, store, func(_ context.Context, in []stageinput.Attachment, _ [][]byte) ([]stagestore.Attachment, error) {
		if len(in) != 0 {
			t.Fatalf("attachment input = %#v", in)
		}
		return []stagestore.Attachment{}, nil
	})
	_, err := r.Revise(context.Background(), ReviseInput{StageID: "stage-1", ExpectedRevision: 1, ExpectedDigest: digest, Attachments: []Attachment{}})
	if err != nil || len(store.reviseIn.Composition.Attachments) != 0 {
		t.Fatalf("err=%v input=%+v", err, store.reviseIn)
	}
	if string(store.reviseIn.Composition.Plan) != `{"steps":[{"ordinal":1,"type":"create_post","condition":"always"}]}` {
		t.Fatalf("cleared attachment plan = %s", store.reviseIn.Composition.Plan)
	}
}

func TestReviserRejectsCredentialInExpectedDigest(t *testing.T) {
	store, digest := revisionFixture(stagestore.Reply)
	credential := hex.EncodeToString(digest[:])
	r, err := NewReviser([]string{credential}, store, stageinput.Bind)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.Revise(context.Background(), ReviseInput{StageID: "stage-1", RequestID: "revise-1", ExpectedRevision: 1, ExpectedDigest: digest, Body: strings.NewReader("body")}); !errors.Is(err, ErrCredential) {
		t.Fatalf("revise digest credential = %v", err)
	}
	if _, err = r.Cancel(context.Background(), CancelInput{StageID: "stage-1", RequestID: "cancel-1", ExpectedRevision: 1, ExpectedDigest: digest}); !errors.Is(err, ErrCredential) {
		t.Fatalf("cancel digest credential = %v", err)
	}
	if store.reviseCalls != 0 || store.cancelCalls != 0 {
		t.Fatalf("store mutated: revise=%d cancel=%d", store.reviseCalls, store.cancelCalls)
	}
}
