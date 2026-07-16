package output

import (
	"reflect"
	"testing"
	"time"
)

func TestGroupIntoThreadsGroupsSortsAndDoesNotMutateInput(t *testing.T) {
	base := time.Date(2026, time.February, 21, 10, 0, 0, 0, time.UTC)
	messages := []Message{
		{ID: "reply-2", RootID: "root", Timestamp: base.Add(3 * time.Minute)},
		{ID: "root", Timestamp: base, Replies: []Message{{ID: "stale"}}},
		{ID: "reply-1", RootID: "root", Timestamp: base.Add(time.Minute)},
	}
	result := GroupIntoThreads(messages)
	if got := messageIDs(result); !reflect.DeepEqual(got, []string{"root"}) {
		t.Fatalf("roots = %v", got)
	}
	if got := messageIDs(result[0].Replies); !reflect.DeepEqual(got, []string{"reply-1", "reply-2"}) {
		t.Fatalf("replies = %v", got)
	}
	if got := messageIDs(messages); !reflect.DeepEqual(got, []string{"reply-2", "root", "reply-1"}) || messages[1].Replies[0].ID != "stale" {
		t.Fatalf("input mutated: %#v", messages)
	}
}

func TestGroupIntoThreadsKeepsOrphansInGlobalTimestampThenIDOrder(t *testing.T) {
	stamp := time.Date(2026, time.February, 21, 10, 0, 0, 0, time.UTC)
	messages := []Message{
		{ID: "root-z", Timestamp: stamp},
		{ID: "orphan-b", RootID: "missing", Timestamp: stamp},
		{ID: "orphan-a", RootID: "missing", Timestamp: stamp},
		{ID: "root-early", Timestamp: stamp.Add(-time.Minute)},
	}
	if got := messageIDs(GroupIntoThreads(messages)); !reflect.DeepEqual(got, []string{"root-early", "orphan-a", "orphan-b", "root-z"}) {
		t.Fatalf("order = %v", got)
	}
}

func TestGroupIntoThreadsUsesCanonicalIdentityForGroupingAndTies(t *testing.T) {
	stamp := time.Date(2026, time.February, 21, 10, 0, 0, 0, time.UTC)
	messages := []Message{
		{ID: "visible-first", CanonicalID: "z", Timestamp: stamp},
		{ID: "visible-last", CanonicalID: "a", Timestamp: stamp},
		{ID: "masked-reply", RootID: "masked-root", CanonicalID: "r", CanonicalRootID: "z", Timestamp: stamp.Add(time.Second)},
	}
	result := GroupIntoThreads(messages)
	if got := messageIDs(result); !reflect.DeepEqual(got, []string{"visible-last", "visible-first"}) {
		t.Fatalf("canonical root order = %v", got)
	}
	if got := messageIDs(result[1].Replies); !reflect.DeepEqual(got, []string{"masked-reply"}) {
		t.Fatalf("canonical grouping = %v", got)
	}
}

func TestGroupIntoThreadsSortsEqualTimestampRepliesByCanonicalID(t *testing.T) {
	stamp := time.Date(2026, time.February, 21, 10, 0, 0, 0, time.UTC)
	result := GroupIntoThreads([]Message{
		{ID: "root", Timestamp: stamp},
		{ID: "visible-a", CanonicalID: "b", RootID: "root", Timestamp: stamp.Add(time.Second)},
		{ID: "visible-z", CanonicalID: "a", RootID: "root", Timestamp: stamp.Add(time.Second)},
	})
	if got := messageIDs(result[0].Replies); !reflect.DeepEqual(got, []string{"visible-z", "visible-a"}) {
		t.Fatalf("reply order = %v", got)
	}
}

func TestGroupIntoThreadsEmptyIsNonNil(t *testing.T) {
	result := GroupIntoThreads(nil)
	if result == nil || len(result) != 0 {
		t.Fatalf("result = %#v", result)
	}
}

func messageIDs(messages []Message) []string {
	ids := make([]string, len(messages))
	for index := range messages {
		ids[index] = messages[index].ID
	}
	return ids
}
