//go:build e2e

package e2e_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"sort"
	"testing"
)

func TestGoApplyCreatesExactDirectAndGroupConversationsOnce(t *testing.T) {
	h := newLiveHarness(t)
	self := h.user("me")
	carol := h.user("username/carol")
	dave := h.user("username/dave")
	tests := []struct {
		name, operation, endpoint string
		peers                     []string
		args                      []string
	}{
		{"direct", "resolve_dm", "POST /api/v4/channels/direct", []string{carol.ID}, []string{"dm-create", "carol"}},
		{"group", "resolve_group_dm", "POST /api/v4/channels/group", []string{carol.ID, dave.ID}, []string{"group-create", "carol", "dave"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requestPrefix := "conversation-" + test.name
			beforeChannels := h.conversationIDs(self.ID)
			stageArgs := append([]string{"--json", "stage"}, test.args...)
			stageArgs = append(stageArgs, "--request-id", requestPrefix+"-stage")
			writesBeforeStage := h.mutationSnapshot()
			var staged stageReceipt
			decodeCLI(t, h.cli("", stageArgs...), &staged)
			h.assertMutationDelta(writesBeforeStage, nil)
			if staged.Schema != "mm/v2/stage-receipt" || staged.Stage.StageRef == "" {
				t.Fatalf("invalid stage receipt: %+v", staged)
			}

			writesBeforeApply := h.mutationSnapshot()
			var applied applyReceipt
			decodeCLI(t, h.cli("", "--json", "apply", staged.Stage.StageRef, "--request-id", requestPrefix+"-apply"), &applied)
			h.assertMutationDelta(writesBeforeApply, map[string]int{test.endpoint: 1})
			if applied.Schema != "mm/v2/apply-receipt" || applied.Outcome != "succeeded" {
				t.Fatalf("invalid apply receipt: %+v", applied)
			}
			channelID, receiptPeers := conversationResult(t, applied, test.operation)
			wantReceiptPeers := slices.Clone(test.peers)
			sort.Strings(wantReceiptPeers)
			if !slices.IsSorted(receiptPeers) || !slices.Equal(receiptPeers, wantReceiptPeers) {
				t.Fatalf("receipt peers=%v, want canonical %v", receiptPeers, wantReceiptPeers)
			}
			var created struct {
				ID   string `json:"id"`
				Type string `json:"type"`
			}
			h.api(http.MethodGet, "/channels/"+channelID, nil, &created)
			wantType := "D"
			if test.name == "group" {
				wantType = "G"
			}
			if created.ID != channelID || created.Type != wantType {
				t.Fatalf("channel=%+v, want id=%q type=%q", created, channelID, wantType)
			}
			if _, existed := beforeChannels[channelID]; existed {
				t.Fatalf("apply resolved pre-existing conversation %q instead of creating it", channelID)
			}
			if afterType := h.conversationIDs(self.ID)[channelID]; afterType != wantType {
				t.Fatalf("new conversation %q absent from live user channels or has type %q", channelID, afterType)
			}
			var members []struct {
				ChannelID string `json:"channel_id"`
				UserID    string `json:"user_id"`
			}
			h.api(http.MethodGet, "/channels/"+channelID+"/members?page=0&per_page=9", nil, &members)
			gotMembers := make([]string, 0, len(members))
			for _, member := range members {
				if member.ChannelID != channelID || member.UserID == "" {
					t.Fatalf("invalid member: %+v", member)
				}
				gotMembers = append(gotMembers, member.UserID)
			}
			wantMembers := append([]string{self.ID}, test.peers...)
			sort.Strings(gotMembers)
			sort.Strings(wantMembers)
			if !slices.Equal(gotMembers, wantMembers) {
				t.Fatalf("live members=%v, want %v", gotMembers, wantMembers)
			}

			writesBeforeReplay := h.mutationSnapshot()
			var replay applyReceipt
			decodeCLI(t, h.cli("", "--json", "apply", staged.Stage.StageRef, "--request-id", requestPrefix+"-apply"), &replay)
			h.assertMutationDelta(writesBeforeReplay, nil)
			if replay.AttemptID != applied.AttemptID || replay.Outcome != "succeeded" {
				t.Fatalf("invalid replay: %+v", replay)
			}
		})
	}
}

func (h *liveHarness) conversationIDs(userID string) map[string]string {
	h.t.Helper()
	var channels []struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	}
	h.api(http.MethodGet, "/users/"+userID+"/channels", nil, &channels)
	result := make(map[string]string)
	for _, channel := range channels {
		if channel.Type != "D" && channel.Type != "G" {
			continue
		}
		if channel.ID == "" {
			h.t.Fatal("live conversation list contains an empty channel id")
		}
		if _, duplicate := result[channel.ID]; duplicate {
			h.t.Fatalf("live conversation list duplicates %q", channel.ID)
		}
		result[channel.ID] = channel.Type
	}
	return result
}

func decodeCLI(t *testing.T, raw []byte, target any) {
	t.Helper()
	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatalf("invalid CLI JSON: %v", err)
	}
}

func conversationResult(t *testing.T, receipt applyReceipt, operation string) (string, []string) {
	t.Helper()
	for _, step := range receipt.Steps {
		if step.Kind != "resolve_conversation" {
			continue
		}
		var result struct {
			ChannelID     string   `json:"channelId"`
			ParticipantID []string `json:"participantIds"`
		}
		if err := json.Unmarshal(step.Result, &result); err == nil && result.ChannelID != "" {
			return result.ChannelID, result.ParticipantID
		}
	}
	t.Fatalf("%s receipt has no validated conversation result", operation)
	return "", nil
}
