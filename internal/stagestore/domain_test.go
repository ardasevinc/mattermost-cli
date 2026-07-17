//go:build darwin || linux

package stagestore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/ardasevinc/mattermost-cli/internal/stagecursor"
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
	return CreateInput{request, sha256.Sum256([]byte(body)), CreatePost, "https://mattermost.example/api/v4", "server-1", "user-1", RevisionContent{[]byte(body), json.RawMessage(`{"kind":"O","channelId":"channel-1"}`), json.RawMessage(`{"steps":[{"kind":"create_post"}]}`), []Attachment{attachment("a.txt"), attachment("b.txt")}}}
}

func TestFindCreateUsesExactReceiptRevisionAndFailsClosedOnCorruption(t *testing.T) {
	s := openDomainStore(t)
	in := createInput("exact-revision", "one")
	created, err := s.Create(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	revised, err := s.Revise(context.Background(), ReviseInput{StageID: created.Stage.ID, RequestID: "revise-exact", ExpectedRevision: 1, ExpectedDigest: created.Stage.SemanticDigest,
		RequestDigest: sha256.Sum256([]byte("revise-exact")), Composition: Composition{Body: []byte("two"), Attachments: in.Content.Attachments}})
	if err != nil || revised.Stage.Revision != 2 {
		t.Fatalf("revise = %#v/%v", revised, err)
	}
	record, found, err := s.FindCreate(context.Background(), in.ServerURL, in.UserID, in.RequestID)
	if err != nil || !found || record.Stage.Revision != 1 || !reflect.DeepEqual(record.Destination, in.Content.Destination) {
		t.Fatalf("record = %#v/%v/%v", record, found, err)
	}
	if _, err = s.db.Exec(`DROP TRIGGER local_requests_immutable_update`); err != nil {
		t.Fatal(err)
	}
	var originalRaw string
	if err = s.db.QueryRow(`SELECT result_json FROM local_requests WHERE request_id=?`, in.RequestID).Scan(&originalRaw); err != nil {
		t.Fatal(err)
	}
	var later CreateRecord
	if err = json.Unmarshal([]byte(originalRaw), &later); err != nil {
		t.Fatal(err)
	}
	later.Result.Stage.Revision = revised.Stage.Revision
	later.Result.Stage.SemanticDigest = revised.Stage.SemanticDigest
	laterRaw, err := marshalCanonical(later)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`UPDATE local_requests SET result_json=? WHERE request_id=?`, string(laterRaw), in.RequestID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.FindCreate(context.Background(), in.ServerURL, in.UserID, in.RequestID); err == nil || errors.Is(err, ErrConflict) {
		t.Fatalf("later-revision corruption error = %v", err)
	}
	if _, err = s.db.Exec(`UPDATE local_requests SET result_json=? WHERE request_id=?`, originalRaw, in.RequestID); err != nil {
		t.Fatal(err)
	}
	other := createInput("other-receipt", "one")
	if _, err = s.Create(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	var otherRaw string
	if err = s.db.QueryRow(`SELECT result_json FROM local_requests WHERE request_id=?`, other.RequestID).Scan(&otherRaw); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`UPDATE local_requests SET result_json=? WHERE request_id=?`, otherRaw, in.RequestID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.FindCreate(context.Background(), in.ServerURL, in.UserID, in.RequestID); err == nil || errors.Is(err, ErrConflict) {
		t.Fatalf("cross-stage corruption error = %v", err)
	}
	if _, err = s.db.Exec(`UPDATE local_requests SET result_json='{}' WHERE request_id=?`, in.RequestID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.FindCreate(context.Background(), in.ServerURL, in.UserID, in.RequestID); err == nil || errors.Is(err, ErrConflict) {
		t.Fatalf("corruption error = %v", err)
	}
}

func TestConcurrentCreateReturnsOneAuthoritativeProjection(t *testing.T) {
	s := openDomainStore(t)
	base := createInput("concurrent-create", "body")
	const workers = 20
	results := make(chan CreateRecord, workers)
	failures := make(chan error, workers)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			in := base
			in.Content.Destination = json.RawMessage(`{"kind":"O","channelId":"channel-` + string(rune('a'+i)) + `"}`)
			record, err := s.Create(context.Background(), in)
			if err != nil {
				failures <- err
				return
			}
			results <- record
		}()
	}
	wg.Wait()
	close(results)
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	var id string
	var projection []byte
	firsts := 0
	count := 0
	for record := range results {
		count++
		if !record.Replay {
			firsts++
		}
		if id == "" {
			id, projection = record.Stage.ID, record.Destination
		}
		if record.Stage.ID != id || !bytes.Equal(record.Destination, projection) {
			t.Fatalf("non-authoritative record = %#v", record)
		}
	}
	if count != workers || firsts != 1 {
		t.Fatalf("count/firsts = %d/%d", count, firsts)
	}
}

func TestCreateAndReplayReturnCanonicalAuthoritativeProjection(t *testing.T) {
	s := openDomainStore(t)
	in := createInput("canonical-projection", "body")
	in.Content.Destination = json.RawMessage(`{ "kind": "O", "channelId": "channel-1" }`)
	in.Content.Plan = json.RawMessage(`{ "steps": [ { "kind": "create_post" } ] }`)

	created, err := s.Create(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	wantDestination := []byte(`{"kind":"O","channelId":"channel-1"}`)
	wantPlan := []byte(`{"steps":[{"kind":"create_post"}]}`)
	if !bytes.Equal(created.Destination, wantDestination) || !bytes.Equal(created.Plan, wantPlan) {
		t.Fatalf("created projection = %s / %s", created.Destination, created.Plan)
	}

	replayed, err := s.Create(context.Background(), in)
	if err != nil || !replayed.Replay {
		t.Fatalf("replay = %#v / %v", replayed, err)
	}
	if !bytes.Equal(replayed.Destination, created.Destination) || !bytes.Equal(replayed.Plan, created.Plan) {
		t.Fatalf("replay projection = %s / %s, want %s / %s", replayed.Destination, replayed.Plan, created.Destination, created.Plan)
	}
}

func TestCallerIntentMigrationsTombstoneLegacyCreateAndReviseReceipts(t *testing.T) {
	path := testPath(t)
	original := migrations
	migrations = append([]migration(nil), original[:2]...)
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	in := createInput("legacy-create", "body")
	if _, err = s.Create(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`INSERT INTO local_requests(server_url,user_id,request_id,request_schema,request_digest,result_json,created_at) VALUES(?,?,?,?,?,?,?)`, in.ServerURL, in.UserID, "revise-kept", "mm/v2/stage-revise-request", make([]byte, 32), `{}`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	migrations = original
	t.Cleanup(func() { migrations = original })
	s, err = Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	var createSchema, reviseSchema string
	if err = s.db.QueryRow(`SELECT request_schema FROM local_requests WHERE request_id='legacy-create'`).Scan(&createSchema); err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRow(`SELECT request_schema FROM local_requests WHERE request_id='revise-kept'`).Scan(&reviseSchema); err != nil {
		t.Fatal(err)
	}
	if createSchema != "mm/v2/legacy-stage-request-conflict" || reviseSchema != "mm/v2/legacy-stage-revise-conflict" {
		t.Fatalf("schemas = %s/%s", createSchema, reviseSchema)
	}
	if _, _, err = s.FindCreate(context.Background(), in.ServerURL, in.UserID, in.RequestID); !errors.Is(err, ErrConflict) {
		t.Fatalf("legacy lookup = %v", err)
	}
	if _, _, err = s.FindCreate(context.Background(), in.ServerURL, in.UserID, "revise-kept"); !errors.Is(err, ErrConflict) {
		t.Fatalf("legacy revise ID reuse = %v", err)
	}
}
func reviseInput(stage StageSummary, request, body string) ReviseInput {
	content := createInput("", body).Content
	return ReviseInput{StageID: stage.ID, RequestID: request, ExpectedRevision: stage.Revision, ExpectedDigest: stage.SemanticDigest,
		RequestDigest: sha256.Sum256([]byte(request + "\x00" + body)), Composition: Composition{Body: content.Body, Attachments: content.Attachments}}
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

func TestReviseCallerIntentReplayIgnoresBoundFileDrift(t *testing.T) {
	s := openDomainStore(t)
	created, err := s.Create(context.Background(), createInput("", "one"))
	if err != nil {
		t.Fatal(err)
	}
	requestDigest := sha256.Sum256([]byte("caller body and attachment metadata"))
	first := reviseInput(created.Stage, "revise-replay", "two")
	first.RequestDigest = requestDigest
	revised, err := s.Revise(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	retry := first
	retry.Composition = Composition{Body: []byte("different bound bytes"), Attachments: []Attachment{attachment("changed.txt")}}
	replayed, err := s.Revise(context.Background(), retry)
	if err != nil || !replayed.Replay || replayed.Stage != revised.Stage {
		t.Fatalf("replayed=%+v err=%v want=%+v", replayed, err, revised)
	}
	lookup, found, err := s.FindRevise(context.Background(), revised.Stage.ServerURL, revised.Stage.UserID, first.RequestID, requestDigest)
	if err != nil || !found || !lookup.Replay || lookup.Stage != revised.Stage {
		t.Fatalf("lookup=%+v found=%v err=%v", lookup, found, err)
	}
	wrong := requestDigest
	wrong[0] ^= 0xff
	if _, _, err = s.FindRevise(context.Background(), revised.Stage.ServerURL, revised.Stage.UserID, first.RequestID, wrong); !errors.Is(err, ErrConflict) {
		t.Fatalf("wrong caller intent = %v", err)
	}
	if _, err = s.db.Exec(`DROP TRIGGER local_requests_immutable_update`); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err = s.db.QueryRow(`SELECT result_json FROM local_requests WHERE request_id=?`, first.RequestID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var corrupted MutationResult
	if err = json.Unmarshal([]byte(raw), &corrupted); err != nil {
		t.Fatal(err)
	}
	corrupted.Stage.ID = "stg_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	encoded, err := marshalCanonical(corrupted)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`UPDATE local_requests SET result_json=? WHERE request_id=?`, string(encoded), first.RequestID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.FindRevise(context.Background(), revised.Stage.ServerURL, revised.Stage.UserID, first.RequestID, requestDigest); err == nil || errors.Is(err, ErrConflict) {
		t.Fatalf("corrupt revise projection = %v", err)
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

func TestListRecordsUsesHonestKeysetPagination(t *testing.T) {
	s := openDomainStore(t)
	for i := range 5 {
		created, err := s.Create(context.Background(), createInput("", string(rune('a'+i))))
		if err != nil {
			t.Fatal(err)
		}
		stamp := time.Date(2026, 1, 1, 0, 0, 0, 123456780+i, time.UTC)
		if _, err = s.db.Exec(`UPDATE stages SET updated_at=? WHERE id=?`, formatTime(stamp), created.Stage.ID); err != nil {
			t.Fatal(err)
		}
	}
	var after *stagecursor.Boundary
	seen := make(map[string]struct{})
	for pageNumber := 0; ; pageNumber++ {
		page, err := s.ListRecords(context.Background(), ListOptions{Limit: 2, After: after})
		if err != nil || len(page.Records) == 0 || len(page.Records) > 2 {
			t.Fatalf("page %d = %+v err=%v", pageNumber, page, err)
		}
		for _, record := range page.Records {
			if _, exists := seen[record.ID]; exists {
				t.Fatalf("duplicate stage %s", record.ID)
			}
			seen[record.ID] = struct{}{}
		}
		if page.NextCursor == nil {
			break
		}
		boundary, err := stagecursor.Decode(*page.NextCursor)
		if err != nil {
			t.Fatal(err)
		}
		after = &boundary
	}
	if len(seen) != 5 {
		t.Fatalf("listed %d stages", len(seen))
	}
}

func TestListRecordsFailsClosedWhenCurrentRevisionIsMissing(t *testing.T) {
	s := openDomainStore(t)
	created, err := s.Create(context.Background(), createInput("", "body"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`UPDATE stages SET current_revision=999 WHERE id=?`, created.Stage.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ListRecords(context.Background(), ListOptions{}); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("corrupt current revision = %v", err)
	}
}

func TestShowFailsClosedWhenRetainedContentBreaksSemanticDigest(t *testing.T) {
	s := openDomainStore(t)
	created, err := s.Create(context.Background(), createInput("", "original"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`UPDATE stage_revisions SET body=? WHERE stage_id=? AND revision=1`, []byte("modified"), created.Stage.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Show(context.Background(), created.Stage.ID); err == nil || errors.Is(err, ErrInvalid) || errors.Is(err, ErrNotFound) {
		t.Fatalf("semantic corruption = %v", err)
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
	createdDelete, err := s.Create(context.Background(), validDelete)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Revise(context.Background(), ReviseInput{StageID: createdDelete.Stage.ID, ExpectedRevision: 1, ExpectedDigest: createdDelete.Stage.SemanticDigest}); !errors.Is(err, ErrNotEligible) {
		t.Fatalf("delete revision error = %v", err)
	}
}

func TestReviseDistinguishesCorruptOperationFromImmutableOperation(t *testing.T) {
	s := openDomainStore(t)
	created, err := s.Create(context.Background(), createInput("operation-corruption", "one"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`PRAGMA ignore_check_constraints=ON`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`UPDATE stages SET operation='corrupt' WHERE id=?`, created.Stage.ID); err != nil {
		t.Fatal(err)
	}
	input := reviseInput(created.Stage, "operation-corruption-revise", "two")
	if _, err = s.Revise(context.Background(), input); err == nil || errors.Is(err, ErrInvalid) || errors.Is(err, ErrNotEligible) {
		t.Fatalf("corrupt operation error = %v", err)
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
	if _, err := s.List(context.Background(), ListOptions{Limit: maxListLimit + 1}); !errors.Is(err, ErrInvalid) {
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
