package stageoutput

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/ardasevinc/mattermost-cli/internal/stagecursor"
	"github.com/ardasevinc/mattermost-cli/internal/stagestore"
	"github.com/ardasevinc/mattermost-cli/internal/staging"
)

func TestConstructorsProduceStrictDocumentsAndSealInputs(t *testing.T) {
	destination := conversationDestination()
	plan := staging.Plan{Steps: []staging.PlanStep{{Ordinal: 1, Type: "create_post", Condition: "always"}}}
	preview, err := NewPreview(stagestore.CreatePost, staging.Preview{ServerURL: "https://mattermost.example/api/v4", UserID: "user-1", Destination: destination, Plan: plan}, nil)
	if err != nil {
		t.Fatal(err)
	}
	destination.ParticipantIDs = append(destination.ParticipantIDs, "mutated")
	plan.Steps[0].Type = "delete_post"
	if len(preview.Destination.ParticipantIDs) != 0 || preview.Plan.Steps[0].Type != "create_post" {
		t.Fatal("preview aliases mutable input")
	}
	detail := validDetail()
	stage, err := NewStage(detail, nil)
	if err != nil {
		t.Fatal(err)
	}
	detail.Body[0] = 'X'
	detail.Attachments[0].SuppliedPath = "changed"
	if *stage.Content.Body != "hello" || stage.Attachments[0].Path != "/tmp/a.txt" {
		t.Fatal("stage aliases mutable input")
	}
	encoded, _ := json.Marshal(stage)
	if string(encoded) == "" || stage.Stage.CreatedAt != "2026-07-17T08:00:00.123Z" {
		t.Fatalf("bad stage projection: %s", encoded)
	}

	mutation := stagestore.MutationResult{Action: "create", Stage: detail.StageSummary, RecordedAt: detail.CreatedAt, Replay: true}
	receipt, err := NewReceipt(mutation, detail.Destination, nil)
	if err != nil || receipt.Action != "created" || !receipt.Replayed {
		t.Fatalf("receipt: %#v, %v", receipt, err)
	}
	list, err := NewStages(stagestore.ListPage{Records: []stagestore.ListRecord{{StageSummary: detail.StageSummary, Destination: detail.Destination}}}, nil)
	if err != nil || len(list.Stages) != 1 || list.NextCursor != nil {
		t.Fatalf("list: %#v, %v", list, err)
	}
}

func TestConstructorsFailClosedOnCorruptionAndCredentials(t *testing.T) {
	detail := validDetail()
	for name, mutate := range map[string]func(*stagestore.StageDetail){
		"operation":   func(v *stagestore.StageDetail) { v.Operation = "bogus" },
		"destination": func(v *stagestore.StageDetail) { v.Destination = json.RawMessage(`{"kind":"conversation"}`) },
		"plan":        func(v *stagestore.StageDetail) { v.Plan = json.RawMessage(`{"steps":[]}`) },
		"lifecycle":   func(v *stagestore.StageDetail) { v.Lifecycle = stagestore.LifecycleCompleted },
	} {
		t.Run(name, func(t *testing.T) {
			v := detail
			mutate(&v)
			if _, err := NewStage(v, nil); !errors.Is(err, ErrInvalid) {
				t.Fatalf("err=%v", err)
			}
		})
	}
	for name, mutate := range map[string]func(*stagestore.StageDetail){
		"body":    func(v *stagestore.StageDetail) { v.Body = []byte("hello active-secret") },
		"path":    func(v *stagestore.StageDetail) { v.Attachments[0].SuppliedPath = "/active-secret/file" },
		"binding": func(v *stagestore.StageDetail) { v.UserID = "active-secret" },
		"destination": func(v *stagestore.StageDetail) {
			v.Destination = json.RawMessage(`{"kind":"conversation","channelId":"active-secret","channelType":"public","teamId":"team-1","postId":null,"rootPostId":null,"participantIds":[],"emoji":null,"postState":null,"reactionPresent":null}`)
		},
	} {
		t.Run("credential_"+name, func(t *testing.T) {
			v := detail
			mutate(&v)
			if _, err := NewStage(v, []string{"active-secret"}); !errors.Is(err, ErrInvalid) {
				t.Fatalf("err=%v", err)
			}
		})
	}
	if _, err := NewStages(stagestore.ListPage{Records: make([]stagestore.ListRecord, 101)}, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversize err=%v", err)
	}
}

func TestStageRejectsRetentionPlanAndTimestampContradictions(t *testing.T) {
	base := validDetail()
	pruned := base
	pruned.Lifecycle, pruned.Recovery, pruned.Body, pruned.Attachments = stagestore.LifecyclePruned, stagestore.RecoveryForbidden, nil, nil
	projected, err := NewStage(pruned, nil)
	if err != nil || projected.AttachmentState != "pruned" {
		t.Fatalf("valid pruned projection: %v", err)
	}
	pruned.Plan = json.RawMessage(`{"steps":[{"ordinal":1,"type":"create_post","condition":"always"}]}`)
	projected, err = NewStage(pruned, nil)
	if err != nil || projected.AttachmentState != "none" {
		t.Fatalf("pruned no-attachment projection: %#v %v", projected, err)
	}
	cases := map[string]func(*stagestore.StageDetail){
		"pruned body retained": func(v *stagestore.StageDetail) {
			v.Lifecycle, v.Recovery = stagestore.LifecyclePruned, stagestore.RecoveryForbidden
		},
		"pruned attachment retained": func(v *stagestore.StageDetail) {
			v.Lifecycle, v.Recovery, v.Body = stagestore.LifecyclePruned, stagestore.RecoveryForbidden, nil
		},
		"attachment count": func(v *stagestore.StageDetail) { v.Attachments = nil },
		"ordinal gap": func(v *stagestore.StageDetail) {
			v.Plan = json.RawMessage(`{"steps":[{"ordinal":2,"type":"upload_attachment","condition":"always"},{"ordinal":3,"type":"create_post","condition":"always"}]}`)
		},
		"step order": func(v *stagestore.StageDetail) {
			v.Plan = json.RawMessage(`{"steps":[{"ordinal":1,"type":"create_post","condition":"always"},{"ordinal":2,"type":"upload_attachment","condition":"always"}]}`)
		},
		"zero revision time":     func(v *stagestore.StageDetail) { v.RevisionCreatedAt = time.Time{} },
		"revision before create": func(v *stagestore.StageDetail) { v.RevisionCreatedAt = v.CreatedAt.Add(-time.Millisecond) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			v := base
			mutate(&v)
			if _, err := NewStage(v, nil); !errors.Is(err, ErrInvalid) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestReceiptsAndListsEnforceEmittedTimestampOrderAndUniqueIDs(t *testing.T) {
	detail := validDetail()
	detail.CreatedAt = time.Time{}
	if _, err := NewStages(stagestore.ListPage{Records: []stagestore.ListRecord{{StageSummary: detail.StageSummary, Destination: detail.Destination}}}, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("zero timestamp err=%v", err)
	}

	detail = validDetail()
	mutation := stagestore.MutationResult{Action: "create", Stage: detail.StageSummary, RecordedAt: detail.UpdatedAt.Add(-time.Millisecond)}
	if _, err := NewReceipt(mutation, detail.Destination, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("receipt timestamp err=%v", err)
	}

	first := stagestore.ListRecord{StageSummary: detail.StageSummary, Destination: detail.Destination}
	second := first
	second.UpdatedAt = first.UpdatedAt.Add(-time.Nanosecond)
	if _, err := NewStages(stagestore.ListPage{Records: []stagestore.ListRecord{first, second}}, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("duplicate stage err=%v", err)
	}
	first.ID, second.ID = "stg_zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz", "stg_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := NewStages(stagestore.ListPage{Records: []stagestore.ListRecord{first, second}}, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("millisecond ordering err=%v", err)
	}
	cursor, err := stagecursor.Encode(stagecursor.Boundary{UpdatedAt: first.UpdatedAt.UTC(), StageID: first.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = NewStages(stagestore.ListPage{Records: []stagestore.ListRecord{first}, NextCursor: &cursor}, nil); err != nil {
		t.Fatalf("valid cursor err=%v", err)
	}
	wrong, _ := stagecursor.Encode(stagecursor.Boundary{UpdatedAt: first.UpdatedAt.UTC(), StageID: second.ID})
	if _, err = NewStages(stagestore.ListPage{Records: []stagestore.ListRecord{first}, NextCursor: &wrong}, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unbound cursor err=%v", err)
	}
}

func TestNewPruneResultUsesBoundedMillisecondContract(t *testing.T) {
	recorded := time.Date(2026, 7, 17, 12, 0, 0, 999999999, time.UTC)
	document, err := NewPruneResult(stagestore.BulkPruneResult{
		Schema: "mm/v2/stage-prune-result", Action: "pruned", Cutoff: recorded.Add(-720 * time.Hour), PrunedCount: 3, RecordedAt: recorded,
	}, nil)
	if err != nil || document.PrunedCount != 3 || document.RecordedAt != "2026-07-17T12:00:00.999Z" {
		t.Fatalf("document=%+v err=%v", document, err)
	}
	if _, err = NewPruneResult(stagestore.BulkPruneResult{Schema: "mm/v2/stage-prune-result", Action: "pruned", Cutoff: recorded, RecordedAt: recorded}, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nonpositive age accepted: %v", err)
	}
}

func conversationDestination() staging.Destination {
	team := "team-1"
	return staging.Destination{Kind: "conversation", ChannelID: "channel-1", ChannelType: "public", TeamID: &team, ParticipantIDs: []string{}}
}
func validDetail() stagestore.StageDetail {
	tm := time.Date(2026, 7, 17, 10, 0, 0, 123456789, time.FixedZone("offset", 7200))
	attachmentDigest := [32]byte{2}
	d, _ := json.Marshal(conversationDestination())
	p, _ := json.Marshal(staging.Plan{Steps: []staging.PlanStep{{Ordinal: 1, Type: "upload_attachment", Condition: "always"}, {Ordinal: 2, Type: "create_post", Condition: "always"}}})
	detail := stagestore.StageDetail{StageSummary: stagestore.StageSummary{ID: "stg_0123456789abcdefghijklmnopqrstuv", ServerURL: "https://mattermost.example/api/v4", UserID: "user-1", Operation: stagestore.CreatePost, Lifecycle: stagestore.LifecycleOpen, Recovery: stagestore.RecoveryNone, Revision: 1, CreatedAt: tm, UpdatedAt: tm}, RevisionCreatedAt: tm, Body: []byte("hello"), Destination: d, Plan: p, Attachments: []stagestore.Attachment{{SuppliedPath: "/tmp/a.txt", CanonicalPath: "/private/tmp/a.txt", RemoteFilename: "a.txt", ByteLength: 5, MediaType: "text/plain", ContentDigest: attachmentDigest}}}
	detail.SemanticDigest, _ = stagestore.ComputeSemanticDigest(detail.Operation, detail.ServerURL, detail.ServerID, detail.UserID,
		stagestore.RevisionContent{Body: detail.Body, Destination: detail.Destination, Plan: detail.Plan, Attachments: detail.Attachments})
	return detail
}
