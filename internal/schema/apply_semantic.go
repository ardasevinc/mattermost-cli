package schema

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"
)

type applyReceiptDocument struct {
	Operation    string                  `json:"operation"`
	RecoveryMode string                  `json:"recoveryMode"`
	Destination  applyReceiptDestination `json:"destination"`
	Outcome      string                  `json:"outcome"`
	Recovery     string                  `json:"recovery"`
	StartedAt    time.Time               `json:"startedAt"`
	RecordedAt   time.Time               `json:"recordedAt"`
	Steps        []applyReceiptStep      `json:"steps"`
}

type applyReceiptDestination struct {
	Kind           string          `json:"kind"`
	ChannelID      *string         `json:"channelId"`
	ChannelType    *string         `json:"channelType"`
	TeamID         *string         `json:"teamId"`
	PostID         *string         `json:"postId"`
	RootPostID     *string         `json:"rootPostId"`
	ParticipantIDs []string        `json:"participantIds"`
	Emoji          json.RawMessage `json:"emoji"`
	PostState      json.RawMessage `json:"postState"`
	ReactionState  json.RawMessage `json:"reactionPresent"`
}

type applyReceiptStep struct {
	Ordinal   int             `json:"ordinal"`
	Kind      string          `json:"kind"`
	Condition string          `json:"condition"`
	State     string          `json:"state"`
	Result    json.RawMessage `json:"result"`
	StartedAt *time.Time      `json:"startedAt"`
	EndedAt   *time.Time      `json:"endedAt"`
}

func validateSemanticDocument(id string, data []byte) error {
	if id != "mm/v2/apply-receipt" {
		return nil
	}
	var receipt applyReceiptDocument
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&receipt); err != nil {
		return err
	}
	return validateApplyReceiptDocument(receipt)
}

func validateApplyReceiptDocument(receipt applyReceiptDocument) error {
	if receipt.RecordedAt.Before(receipt.StartedAt) || !validApplyReceiptPlan(receipt.Operation, receipt.Destination, receipt.Steps) || !validApplyReceiptStepOrder(receipt.Steps) {
		return errors.New("invalid apply receipt plan")
	}
	for i, step := range receipt.Steps {
		if step.Ordinal != i+1 || step.StartedAt != nil && step.StartedAt.Before(receipt.StartedAt) ||
			step.EndedAt != nil && (step.EndedAt.Before(receipt.StartedAt) || step.EndedAt.After(receipt.RecordedAt)) ||
			step.StartedAt != nil && step.EndedAt != nil && step.EndedAt.Before(*step.StartedAt) ||
			!validApplyReceiptResultTarget(step, receipt.Destination) {
			return fmt.Errorf("invalid apply receipt step %d", i+1)
		}
	}
	outcome, recovery, ok := deriveApplyReceiptResult(receipt.RecoveryMode, receipt.Steps)
	if !ok || receipt.Outcome != outcome || receipt.Recovery != recovery {
		return errors.New("invalid apply receipt outcome")
	}
	return nil
}

func validApplyReceiptStepOrder(steps []applyReceiptStep) bool {
	stopped := false
	for _, step := range steps {
		switch step.State {
		case "response_validated", "skipped":
			if stopped {
				return false
			}
		case "rejected", "outcome_unknown":
			if stopped {
				return false
			}
			stopped = true
		case "not_dispatched":
			stopped = true
		default:
			return false
		}
	}
	return true
}

func deriveApplyReceiptResult(mode string, steps []applyReceiptStep) (string, string, bool) {
	prior := map[string]string{"ordinary": "none", "resume_partial": "resume_partial", "force_unknown": "force_unknown"}[mode]
	if prior == "" {
		return "", "", false
	}
	validated, skipped, notSent, rejected, unknown := 0, 0, 0, 0, 0
	for _, step := range steps {
		switch step.State {
		case "response_validated":
			validated++
		case "skipped":
			skipped++
		case "not_dispatched":
			notSent++
		case "rejected":
			rejected++
		case "outcome_unknown":
			unknown++
		default:
			return "", "", false
		}
	}
	if unknown > 0 {
		return "unknown", "force_unknown", true
	}
	if rejected > 0 {
		if rejected != 1 {
			return "", "", false
		}
		if validated > 0 {
			return "partial", maxApplyRecovery(prior, "resume_partial"), true
		}
		return "rejected", prior, true
	}
	if notSent > 0 {
		if validated+skipped == 0 {
			return "rejected", prior, true
		}
		return "partial", maxApplyRecovery(prior, "resume_partial"), true
	}
	if validated+skipped != len(steps) {
		return "", "", false
	}
	if validated == 0 {
		return "already_satisfied", "forbidden", true
	}
	return "succeeded", "forbidden", true
}

func maxApplyRecovery(left, right string) string {
	weight := map[string]int{"none": 0, "resume_partial": 1, "force_unknown": 2, "forbidden": 3}
	if weight[left] >= weight[right] {
		return left
	}
	return right
}

func validApplyReceiptPlan(operation string, destination applyReceiptDestination, steps []applyReceiptStep) bool {
	single := func(kind, condition string) bool {
		return len(steps) == 1 && steps[0].Kind == kind && steps[0].Condition == condition
	}
	switch operation {
	case "create_post":
		return validResolvedConversation(destination) && validCreateSteps(steps)
	case "reply":
		return validChannelDestination(destination) && validPostOnlyDestination(destination) && destination.PostID != nil && destination.RootPostID != nil && isJSONNull(destination.PostState) && validCreateSteps(steps)
	case "edit_post":
		return validChannelDestination(destination) && validPostOnlyDestination(destination) && destination.PostID != nil && !isJSONNull(destination.PostState) && single("edit_post", "always")
	case "delete_post":
		return validChannelDestination(destination) && validPostOnlyDestination(destination) && destination.PostID != nil && !isJSONNull(destination.PostState) && single("delete_post", "always")
	case "react":
		return validChannelDestination(destination) && destination.Kind == "reaction" && destination.PostID != nil && single("add_reaction", "if_missing")
	case "unreact":
		return validChannelDestination(destination) && destination.Kind == "reaction" && destination.PostID != nil && single("remove_reaction", "if_missing")
	case "resolve_dm":
		return validUnresolvedConversation(destination, "dm", 1) && single("resolve_conversation", "if_missing")
	case "resolve_group_dm":
		return validUnresolvedConversation(destination, "group", 2) && single("resolve_conversation", "if_missing")
	default:
		return false
	}
}

func validPostOnlyDestination(destination applyReceiptDestination) bool {
	return destination.Kind == "post" && isJSONNull(destination.Emoji) && isJSONNull(destination.ReactionState)
}

func validCreateSteps(steps []applyReceiptStep) bool {
	if len(steps) == 0 || steps[len(steps)-1].Kind != "create_post" || steps[len(steps)-1].Condition != "always" {
		return false
	}
	for _, step := range steps[:len(steps)-1] {
		if step.Kind != "upload_attachment" || step.Condition != "always" {
			return false
		}
	}
	return true
}

func validResolvedConversation(destination applyReceiptDestination) bool {
	return destination.Kind == "conversation" && validChannelDestination(destination)
}

func validChannelDestination(destination applyReceiptDestination) bool {
	if destination.ChannelID == nil || destination.ChannelType == nil {
		return false
	}
	switch *destination.ChannelType {
	case "public", "private":
		return destination.TeamID != nil && len(destination.ParticipantIDs) == 0
	case "dm":
		return destination.TeamID == nil && len(destination.ParticipantIDs) == 1
	case "group":
		return destination.TeamID == nil && len(destination.ParticipantIDs) == 0
	default:
		return false
	}
}

func validUnresolvedConversation(destination applyReceiptDestination, channelType string, minimumParticipants int) bool {
	if destination.Kind != "conversation" || destination.ChannelID != nil || destination.ChannelType == nil || *destination.ChannelType != channelType || destination.TeamID != nil {
		return false
	}
	if channelType == "dm" {
		return len(destination.ParticipantIDs) == minimumParticipants
	}
	return len(destination.ParticipantIDs) >= minimumParticipants
}

func validApplyReceiptResultTarget(step applyReceiptStep, destination applyReceiptDestination) bool {
	if step.State != "response_validated" {
		return true
	}
	var result struct {
		PostID         string   `json:"postId"`
		ChannelID      string   `json:"channelId"`
		ParticipantIDs []string `json:"participantIds"`
	}
	if err := json.Unmarshal(step.Result, &result); err != nil {
		return false
	}
	switch step.Kind {
	case "create_post":
		return destination.ChannelID != nil && result.ChannelID == *destination.ChannelID
	case "edit_post", "delete_post", "add_reaction", "remove_reaction":
		return destination.PostID != nil && result.PostID == *destination.PostID
	case "resolve_conversation":
		return (destination.ChannelID == nil || result.ChannelID == *destination.ChannelID) && slices.Equal(result.ParticipantIDs, destination.ParticipantIDs)
	case "upload_attachment":
		return true
	default:
		return false
	}
}

func isJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}
