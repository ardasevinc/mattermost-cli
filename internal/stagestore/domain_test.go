//go:build darwin || linux

package stagestore

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

func openDomainStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), testPath(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}
func attachment(name string) Attachment {
	return Attachment{"/tmp/" + name, "/private/tmp/" + name, name, 3, "text/plain", sha256.Sum256([]byte(name))}
}
func createInput(request, body string) CreateInput {
	return CreateInput{request, CreatePost, "https://mattermost.example/api/v4", "server-1", "user-1", RevisionContent{[]byte(body), json.RawMessage(`{"kind":"O","channelId":"channel-1"}`), json.RawMessage(`{"steps":[{"kind":"create_post"}]}`), []Attachment{attachment("a.txt"), attachment("b.txt")}}}
}
func reviseInput(stage StageSummary, request, body string) ReviseInput {
	content := createInput("", body).Content
	return ReviseInput{stage.ID, request, stage.Revision, stage.SemanticDigest, false, Composition{content.Body, content.Attachments}}
}

func TestRevisePreservesImmutableDestinationAndPlan(t *testing.T) {
	s := openDomainStore(t)
	input := createInput("", "one")
	input.Content.Destination = json.RawMessage(`{ "binding": {"postId":"post-1"}, "channelId":"channel-1" }`)
	input.Content.Plan = json.RawMessage(`{ "steps": [{"kind":"edit","postId":"post-1"}], "targetVersion":7 }`)
	created, err := s.Create(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.Show(context.Background(), created.Stage.ID)
	if err != nil {
		t.Fatal(err)
	}
	revised, err := s.Revise(context.Background(), reviseInput(created.Stage, "revise-binding", "two"))
	if err != nil {
		t.Fatal(err)
	}
	after, err := s.Show(context.Background(), revised.Stage.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(after.Destination) != string(before.Destination) || string(after.Plan) != string(before.Plan) {
		t.Fatalf("binding changed: destination %s -> %s, plan %s -> %s", before.Destination, after.Destination, before.Plan, after.Plan)
	}
}

func TestCreateShowListPrivacyAttachmentsAndCanonicalDigest(t *testing.T) {
	s := openDomainStore(t)
	created, err := s.Create(context.Background(), createInput("create-1", "private body\n"))
	if err != nil {
		t.Fatal(err)
	}
	if created.Action != "create" || created.Replay || created.Stage.Revision != 1 {
		t.Fatalf("receipt=%#v", created)
	}
	if _, ok := reflect.TypeOf(MutationResult{}).FieldByName("Body"); ok {
		t.Fatal("receipt can carry body")
	}
	if _, ok := reflect.TypeOf(StageSummary{}).FieldByName("Body"); ok {
		t.Fatal("summary can carry body")
	}
	shown, err := s.Show(context.Background(), created.Stage.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(shown.Body) != "private body\n" || string(shown.Destination) != `{"channelId":"channel-1","kind":"O"}` || len(shown.Attachments) != 2 || shown.Attachments[0].RemoteFilename != "a.txt" || shown.Attachments[1].RemoteFilename != "b.txt" {
		t.Fatalf("shown=%#v", shown)
	}
	list, err := s.List(context.Background(), ListOptions{})
	if err != nil || len(list) != 1 || list[0].SemanticDigest != shown.SemanticDigest {
		t.Fatalf("list=%#v err=%v", list, err)
	}
	reordered := createInput("", "private body\n")
	reordered.Content.Destination = json.RawMessage(`{"channelId":"channel-1","kind":"O"}`)
	other, err := s.Create(context.Background(), reordered)
	if err != nil || other.Stage.SemanticDigest != created.Stage.SemanticDigest {
		t.Fatalf("canonical digest differs: %x %x err=%v", other.Stage.SemanticDigest, created.Stage.SemanticDigest, err)
	}
	third, err := s.Create(context.Background(), createInput("", "newer"))
	if err != nil {
		t.Fatal(err)
	}
	const tied = "2026-01-01T00:00:00.000000000Z"
	if _, err = s.db.Exec(`UPDATE stages SET updated_at=? WHERE id IN (?,?)`, tied, created.Stage.ID, third.Stage.ID); err != nil {
		t.Fatal(err)
	}
	list, err = s.List(context.Background(), ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var tiedIDs []string
	for _, item := range list {
		if item.ID == created.Stage.ID || item.ID == third.Stage.ID {
			tiedIDs = append(tiedIDs, item.ID)
		}
	}
	if len(tiedIDs) != 2 || tiedIDs[0] > tiedIDs[1] {
		t.Fatalf("nondeterministic tie order: %v", tiedIDs)
	}
}

func TestImmutableReplaySnapshots(t *testing.T) {
	s := openDomainStore(t)
	first, err := s.Create(context.Background(), createInput("same.request:1", "one"))
	if err != nil {
		t.Fatal(err)
	}
	revised, err := s.Revise(context.Background(), reviseInput(first.Stage, "revise-1", "two"))
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.Create(context.Background(), createInput("same.request:1", "one"))
	if err != nil || !replay.Replay || replay.Stage.Revision != 1 || replay.Stage.Lifecycle != LifecycleOpen || replay.RecordedAt != first.RecordedAt {
		t.Fatalf("replay=%#v err=%v", replay, err)
	}
	if _, err = s.Create(context.Background(), createInput("same.request:1", "changed")); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflict=%v", err)
	}
	cancel, err := s.Cancel(context.Background(), CancelInput{revised.Stage.ID, "cancel-1", revised.Stage.Revision, revised.Stage.SemanticDigest})
	if err != nil {
		t.Fatal(err)
	}
	cancelReplay, err := s.Cancel(context.Background(), CancelInput{revised.Stage.ID, "cancel-1", revised.Stage.Revision, revised.Stage.SemanticDigest})
	if err != nil || !cancelReplay.Replay || cancelReplay.Stage.Lifecycle != LifecycleCanceled || cancelReplay.RecordedAt != cancel.RecordedAt {
		t.Fatalf("cancel replay=%#v err=%v", cancelReplay, err)
	}
	var count int
	if err = s.db.QueryRow(`SELECT count(*) FROM local_requests`).Scan(&count); err != nil || count != 3 {
		t.Fatalf("requests=%d err=%v", count, err)
	}
}

func TestReviewedStateCASAndApplying(t *testing.T) {
	s := openDomainStore(t)
	created, _ := s.Create(context.Background(), createInput("", "one"))
	editorA := reviseInput(created.Stage, "a", "two")
	editorB := reviseInput(created.Stage, "b", "three")
	revised, err := s.Revise(context.Background(), editorA)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Revise(context.Background(), editorB); !errors.Is(err, ErrConflict) {
		t.Fatalf("two-editor=%v", err)
	}
	staleCancel := CancelInput{revised.Stage.ID, "cancel-stale", 1, created.Stage.SemanticDigest}
	if _, err = s.Cancel(context.Background(), staleCancel); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale cancel=%v", err)
	}
	if _, err = s.db.Exec(`UPDATE stages SET lifecycle='applying' WHERE id=?`, revised.Stage.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Revise(context.Background(), reviseInput(revised.Stage, "applying", "four")); !errors.Is(err, ErrNotEligible) {
		t.Fatalf("applying revise=%v", err)
	}
	if _, err = s.Cancel(context.Background(), CancelInput{revised.Stage.ID, "applying-cancel", revised.Stage.Revision, revised.Stage.SemanticDigest}); !errors.Is(err, ErrNotEligible) {
		t.Fatalf("applying cancel=%v", err)
	}
}

func TestSimultaneousMutationCAS(t *testing.T) {
	t.Run("revise revise", func(t *testing.T) {
		s := openDomainStore(t)
		created, _ := s.Create(context.Background(), createInput("", "one"))
		start := make(chan struct{})
		results := make(chan error, 2)
		var wg sync.WaitGroup
		for _, in := range []ReviseInput{reviseInput(created.Stage, "race-a", "two"), reviseInput(created.Stage, "race-b", "three")} {
			wg.Add(1)
			go func(in ReviseInput) {
				defer wg.Done()
				<-start
				_, err := s.Revise(context.Background(), in)
				results <- err
			}(in)
		}
		close(start)
		wg.Wait()
		close(results)
		success, conflict := 0, 0
		for err := range results {
			if err == nil {
				success++
			} else if errors.Is(err, ErrConflict) {
				conflict++
			} else {
				t.Fatalf("unexpected error=%v", err)
			}
		}
		if success != 1 || conflict != 1 {
			t.Fatalf("success=%d conflict=%d", success, conflict)
		}
	})
	t.Run("revise cancel", func(t *testing.T) {
		s := openDomainStore(t)
		created, _ := s.Create(context.Background(), createInput("", "one"))
		start := make(chan struct{})
		results := make(chan error, 2)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, err := s.Revise(context.Background(), reviseInput(created.Stage, "race-revise", "two"))
			results <- err
		}()
		go func() {
			defer wg.Done()
			<-start
			_, err := s.Cancel(context.Background(), CancelInput{created.Stage.ID, "race-cancel", created.Stage.Revision, created.Stage.SemanticDigest})
			results <- err
		}()
		close(start)
		wg.Wait()
		close(results)
		success, refused := 0, 0
		for err := range results {
			if err == nil {
				success++
			} else if errors.Is(err, ErrConflict) || errors.Is(err, ErrNotEligible) {
				refused++
			} else {
				t.Fatalf("unexpected error=%v", err)
			}
		}
		if success != 1 || refused != 1 {
			t.Fatalf("success=%d refused=%d", success, refused)
		}
	})
}

func TestReviveOnlyLegalExpiredForbidden(t *testing.T) {
	s := openDomainStore(t)
	created, _ := s.Create(context.Background(), createInput("", "one"))
	if _, err := s.db.Exec(`UPDATE stages SET lifecycle='expired',recovery='forbidden' WHERE id=?`, created.Stage.ID); err != nil {
		t.Fatal(err)
	}
	input := reviseInput(created.Stage, "revive", "two")
	input.Revive = true
	revived, err := s.Revise(context.Background(), input)
	if err != nil || !revived.Revived || revived.Stage.Lifecycle != LifecycleOpen || revived.Stage.Recovery != RecoveryNone {
		t.Fatalf("revived=%#v err=%v", revived, err)
	}
	other, _ := s.Create(context.Background(), createInput("", "x"))
	if _, err := s.db.Exec(`UPDATE stages SET lifecycle='expired',recovery='force_unknown' WHERE id=?`, other.Stage.ID); err != nil {
		t.Fatal(err)
	}
	illegal := reviseInput(other.Stage, "illegal", "y")
	illegal.Revive = true
	if _, err = s.Revise(context.Background(), illegal); !errors.Is(err, ErrNotEligible) {
		t.Fatalf("illegal revive=%v", err)
	}
	partial, _ := s.Create(context.Background(), createInput("", "p"))
	if _, err := s.db.Exec(`UPDATE stages SET recovery='resume_partial' WHERE id=?`, partial.Stage.ID); err != nil {
		t.Fatal(err)
	}
	normal := reviseInput(partial.Stage, "partial", "q")
	got, err := s.Revise(context.Background(), normal)
	if err != nil || got.Stage.Recovery != RecoveryPartial {
		t.Fatalf("monotonic=%#v err=%v", got, err)
	}
}

func TestOperationContentApplicability(t *testing.T) {
	s := openDomainStore(t)
	for name, test := range map[string]CreateInput{"empty create": createInput("", " \n"), "edit attachments": func() CreateInput { v := createInput("", "body"); v.Operation = EditPost; return v }(), "delete body": func() CreateInput {
		v := createInput("", "body")
		v.Operation = DeletePost
		v.Content.Attachments = nil
		return v
	}()} {
		t.Run(name, func(t *testing.T) {
			if _, err := s.Create(context.Background(), test); !errors.Is(err, ErrInvalid) {
				t.Fatalf("err=%v", err)
			}
		})
	}
	validDelete := createInput("", "body")
	validDelete.Operation = DeletePost
	validDelete.Content.Body = nil
	validDelete.Content.Attachments = nil
	if _, err := s.Create(context.Background(), validDelete); err != nil {
		t.Fatal(err)
	}
}

func TestStrictCanonicalObjects(t *testing.T) {
	s := openDomainStore(t)
	for name, raw := range map[string]string{"array": `[]`, "duplicate": `{"a":1,"a":2}`, "nested duplicate": `{"x":{"a":1,"a":2}}`, "high surrogate": `{"x":"\ud800"}`, "low surrogate": `{"x":"\udc00"}`, "invalid utf8": string([]byte{'{', '"', 'x', '"', ':', '"', 0xff, '"', '}'})} {
		t.Run(name, func(t *testing.T) {
			v := createInput("", "body")
			v.Content.Destination = json.RawMessage(raw)
			if _, err := s.Create(context.Background(), v); !errors.Is(err, ErrInvalid) {
				t.Fatalf("err=%v", err)
			}
		})
	}
	valid := createInput("", "body")
	valid.Content.Destination = json.RawMessage(`{"emoji":"\ud83d\ude00"}`)
	if _, err := s.Create(context.Background(), valid); err != nil {
		t.Fatal(err)
	}
	literal := createInput("", "body")
	literal.Content.Destination = json.RawMessage("{\"line\":\"\u2028\"}")
	escaped := createInput("", "body")
	escaped.Content.Destination = json.RawMessage(`{"line":"\u2028"}`)
	one, err := s.Create(context.Background(), literal)
	if err != nil {
		t.Fatal(err)
	}
	two, err := s.Create(context.Background(), escaped)
	if err != nil || one.Stage.SemanticDigest != two.Stage.SemanticDigest {
		t.Fatalf("line separator canonicalization differs: %x %x err=%v", one.Stage.SemanticDigest, two.Stage.SemanticDigest, err)
	}
}

func TestBoundsRequestIDOrderingAndCommitCancellation(t *testing.T) {
	s := openDomainStore(t)
	bad := createInput("-bad", "body")
	if _, err := s.Create(context.Background(), bad); !errors.Is(err, ErrInvalid) {
		t.Fatalf("request id=%v", err)
	}
	tooMany := createInput("", "body")
	tooMany.Content.Attachments = make([]Attachment, maxAttachments+1)
	if _, err := s.Create(context.Background(), tooMany); !errors.Is(err, ErrInvalid) {
		t.Fatalf("attachments=%v", err)
	}
	for name, mutate := range map[string]func(*CreateInput){
		"zero digest":      func(v *CreateInput) { v.Content.Attachments[0].ContentDigest = [32]byte{} },
		"path control":     func(v *CreateInput) { v.Content.Attachments[0].SuppliedPath = "/tmp/a\x00b" },
		"filename control": func(v *CreateInput) { v.Content.Attachments[0].RemoteFilename = "a\nb.txt" },
	} {
		t.Run(name, func(t *testing.T) {
			v := createInput("", "body")
			mutate(&v)
			if _, err := s.Create(context.Background(), v); !errors.Is(err, ErrInvalid) {
				t.Fatalf("err=%v", err)
			}
		})
	}
	if _, err := s.List(context.Background(), ListOptions{maxListLimit + 1}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("list=%v", err)
	}
	preCanceled, stop := context.WithCancel(context.Background())
	stop()
	if _, err := s.Create(preCanceled, createInput("never", "body")); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled create=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	commitHook.Lock()
	commitHook.fn = cancel
	commitHook.Unlock()
	t.Cleanup(func() { commitHook.Lock(); commitHook.fn = nil; commitHook.Unlock() })
	receipt, err := s.Create(ctx, createInput("durable", "body"))
	if err != nil || ctx.Err() != context.Canceled {
		t.Fatalf("receipt=%#v err=%v ctx=%v", receipt, err, ctx.Err())
	}
	replay, err := s.Create(context.Background(), createInput("durable", "body"))
	if err != nil || !replay.Replay || replay.Stage.ID != receipt.Stage.ID {
		t.Fatalf("durable replay=%#v err=%v", replay, err)
	}
	if _, err := s.db.Exec(`UPDATE local_requests SET request_schema='changed' WHERE request_id='durable'`); err == nil {
		t.Fatal("immutable receipt update succeeded")
	}
}

func TestReplayReceiptValidationFailsClosed(t *testing.T) {
	base := MutationResult{"mm/v2/stage-mutation-receipt", "create", StageSummary{
		ID: "stg_valid", ServerURL: "https://mattermost.example/api/v4", UserID: "user-1", Operation: CreatePost,
		Lifecycle: LifecycleOpen, Recovery: RecoveryNone, Revision: 1, SemanticDigest: sha256.Sum256([]byte("stage")),
		CreatedAt: mustTime(t, "2026-01-01T00:00:00Z"), UpdatedAt: mustTime(t, "2026-01-01T00:00:00Z"),
	}, false, mustTime(t, "2026-01-01T00:00:00Z"), false}
	if !validReplayResult(base, "mm/v2/stage-request", base.Stage.ServerURL, base.Stage.UserID) {
		t.Fatal("valid receipt rejected")
	}
	for name, mutate := range map[string]func(*MutationResult){
		"schema": func(v *MutationResult) { v.Schema = "wrong" }, "action": func(v *MutationResult) { v.Action = "cancel" },
		"digest": func(v *MutationResult) { v.Stage.SemanticDigest = [32]byte{} }, "timestamp": func(v *MutationResult) { v.RecordedAt = v.Stage.CreatedAt.Add(-1) },
	} {
		t.Run(name, func(t *testing.T) {
			v := base
			mutate(&v)
			if validReplayResult(v, "mm/v2/stage-request", base.Stage.ServerURL, base.Stage.UserID) {
				t.Fatal("corrupt receipt accepted")
			}
		})
	}
}
func mustTime(t *testing.T, value string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestV1RequestReplayMigratesToConflictTombstone(t *testing.T) {
	original := migrations
	migrations = append([]migration(nil), migrations[:1]...)
	t.Cleanup(func() { migrations = original })
	path := testPath(t)
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("legacy"))
	const stamp = "2026-01-01T00:00:00Z"
	if _, err = tx.Exec(`INSERT INTO stages(id,created_at,updated_at,operation,server_url,user_id,lifecycle,recovery,current_revision) VALUES('legacy-stage',?,?,'create_post','https://mattermost.example/api/v4','user-1','open','none',1)`, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`INSERT INTO stage_revisions(stage_id,revision,state,created_at,semantic_digest,body,destination_json,plan_json) VALUES('legacy-stage',1,'current',?,?,?,'{}','{}')`, stamp, digest[:], []byte("body")); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`INSERT INTO request_replays(server_url,user_id,request_id,request_schema,semantic_digest,stage_id,revision,created_at) VALUES('https://mattermost.example/api/v4','user-1','legacy-id','old',?,'legacy-stage',1,?)`, digest[:], stamp); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	migrations = original
	s, err = Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var schema string
	if err = s.db.QueryRow(`SELECT request_schema FROM local_requests WHERE request_id='legacy-id'`).Scan(&schema); err != nil || schema != "mm/v2/legacy-request-conflict" {
		t.Fatalf("schema=%q err=%v", schema, err)
	}
	in := createInput("legacy-id", "body")
	if _, err = s.Create(context.Background(), in); !errors.Is(err, ErrConflict) {
		t.Fatalf("legacy reuse=%v", err)
	}
}

func FuzzCanonicalObject(f *testing.F) {
	for _, seed := range [][]byte{[]byte(`{}`), []byte(`{"b":2,"a":1}`), []byte(`{"a":1,"a":2}`), []byte(`[]`), []byte(`{"x":"\ud800"}`)} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		canonical, err := canonicalObject(raw)
		if err != nil {
			return
		}
		again, err := canonicalObject(canonical)
		if err != nil || !reflect.DeepEqual(canonical, again) {
			t.Fatalf("canonicalization unstable: %q -> %q err=%v", canonical, again, err)
		}
	})
}
