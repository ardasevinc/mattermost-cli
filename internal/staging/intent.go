package staging

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"

	"github.com/ardasevinc/mattermost-cli/v2/internal/stageinput"
	"github.com/ardasevinc/mattermost-cli/v2/internal/stagestore"
)

type callerIntent struct {
	Domain      string                      `json:"domain"`
	Operation   stagestore.Operation        `json:"operation"`
	Target      any                         `json:"target"`
	Body        *string                     `json:"body"`
	Emoji       *string                     `json:"emoji"`
	Attachments []stageinput.MetadataIntent `json:"attachments"`
}

func intentDigest(operation stagestore.Operation, target any, body []byte, emoji string, attachments []stageinput.MetadataIntent) [32]byte {
	return callerIntentDigest("mm/v2/stage-request/caller-intent/v1", operation, target, body, emoji, attachments)
}

func revisionRequestDigest(operation stagestore.Operation, target revisionIntent, body []byte, attachments []stageinput.MetadataIntent) [32]byte {
	return callerIntentDigest("mm/v2/stage-revise-request/caller-intent/v1", operation, target, body, "", attachments)
}

func callerIntentDigest(domain string, operation stagestore.Operation, target any, body []byte, emoji string, attachments []stageinput.MetadataIntent) [32]byte {
	var bodyValue *string
	if body != nil {
		value := string(body)
		bodyValue = &value
	}
	var emojiValue *string
	if emoji != "" {
		emojiValue = &emoji
	}
	value := callerIntent{domain, operation, target, bodyValue, emojiValue, attachments}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value)
	return sha256.Sum256(restoreJSONLineSeparators(bytes.TrimSuffix(out.Bytes(), []byte{'\n'})))
}

type conversationIntent struct {
	Conversation string      `json:"conversation"`
	Selector     string      `json:"selector"`
	Value        string      `json:"value"`
	Team         *teamIntent `json:"team"`
}
type teamIntent struct {
	Selector string `json:"selector"`
	Value    string `json:"value"`
}

func conversationCallerIntent(target Target) conversationIntent {
	conversation := map[ConversationType]string{Direct: "direct", Group: "group", Channel: "channel"}[target.Conversation]
	selector := map[SelectorType]string{ByUsername: "username", ByID: "id", ByName: "name"}[target.Selector]
	var team *teamIntent
	if target.Team != nil {
		team = &teamIntent{map[SelectorType]string{ByID: "id", ByName: "name"}[target.Team.By], target.Team.Value}
	}
	return conversationIntent{conversation, selector, target.Value, team}
}

type postIntent struct {
	PostID string `json:"postId"`
}

type revisionIntent struct {
	StageID          string   `json:"stageId"`
	ExpectedRevision int64    `json:"expectedRevision"`
	ExpectedDigest   [32]byte `json:"expectedDigest"`
	Revive           bool     `json:"revive"`
}
