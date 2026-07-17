package mattermost

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ardasevinc/mattermost-cli/internal/api"
)

func conversationMutationJSON(id, channelType, name, displayName string) string {
	raw, _ := json.Marshal(map[string]any{
		"id": id, "create_at": 100, "update_at": 100, "delete_at": 0,
		"team_id": "", "type": channelType, "name": name, "display_name": displayName,
	})
	return string(raw)
}

func TestPreparedDirectFreezesExactMembersAndBindsChannelIdentity(t *testing.T) {
	var calls atomic.Int32
	peers := []string{"peer"}
	transport := mutationRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		body, _ := io.ReadAll(request.Body)
		if request.Method != http.MethodPost || request.URL.Path != "/api/v4/channels/direct" || string(body) != `["peer","self"]` || request.GetBody != nil {
			t.Fatalf("request = %s %s %s getBody=%v", request.Method, request.URL.Path, body, request.GetBody != nil)
		}
		return mutationResponse(http.StatusCreated, conversationMutationJSON("channel-1", "D", "peer__self", "")), nil
	})
	prepared, err := NewConversationMutations(mutationClient(t, transport)).PrepareDirect(ResolveConversationMutationInput{"self", peers})
	if err != nil || calls.Load() != 0 {
		t.Fatalf("prepare=%v calls=%d", err, calls.Load())
	}
	peers[0] = "changed-after-prepare"
	result, err := prepared.Execute(context.Background())
	if err != nil || result.ChannelID != "channel-1" || !slices.Equal(result.ParticipantIDs, []string{"peer"}) || calls.Load() != 1 {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, calls.Load())
	}
}

func TestPreparedConversationPreservesKnownRejection(t *testing.T) {
	transport := mutationRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return mutationResponse(http.StatusForbidden, `{"message":"not allowed"}`), nil
	})
	prepared, err := NewConversationMutations(mutationClient(t, transport)).PrepareDirect(ResolveConversationMutationInput{"self", []string{"peer"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = prepared.Execute(context.Background())
	var apiErr *api.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden || outcomeUnknown(err) {
		t.Fatalf("error = %v", err)
	}
}

func TestPreparedGroupAcceptsSevenPeersAndFreezesCanonicalMembership(t *testing.T) {
	peers := []string{"u7", "u2", "u6", "u1", "u5", "u3", "u4"}
	members := []string{"self", "u1", "u2", "u3", "u4", "u5", "u6", "u7"}
	digest := sha1.Sum([]byte(strings.Join(members, "")))
	name := hex.EncodeToString(digest[:])
	transport := mutationRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(request.Body)
		if request.URL.Path != "/api/v4/channels/group" || string(body) != `["self","u1","u2","u3","u4","u5","u6","u7"]` {
			t.Fatalf("request = %s %s", request.URL.Path, body)
		}
		return mutationResponse(http.StatusCreated, conversationMutationJSON("group-1", "G", name, "arda, peers")), nil
	})
	prepared, err := NewConversationMutations(mutationClient(t, transport)).PrepareGroup(ResolveConversationMutationInput{"self", peers})
	if err != nil {
		t.Fatal(err)
	}
	peers[0] = "changed-after-prepare"
	result, err := prepared.Execute(context.Background())
	if err != nil || result.ChannelID != "group-1" || !slices.Equal(result.ParticipantIDs, members[1:]) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestPreparedConversationRejectsUnboundSuccessAsUnknown(t *testing.T) {
	valid := conversationMutationJSON("channel-1", "D", "peer__self", "")
	for name, test := range map[string]struct {
		status int
		body   string
	}{
		"wrong status":  {http.StatusOK, valid},
		"wrong type":    {http.StatusCreated, conversationMutationJSON("channel-1", "G", "peer__self", "peers")},
		"wrong name":    {http.StatusCreated, conversationMutationJSON("channel-1", "D", "other__self", "")},
		"nonempty team": {http.StatusCreated, strings.Replace(valid, `"team_id":""`, `"team_id":"team"`, 1)},
		"deleted":       {http.StatusCreated, strings.Replace(valid, `"delete_at":0`, `"delete_at":1`, 1)},
		"duplicate id":  {http.StatusCreated, strings.Replace(valid, `"id":"channel-1"`, `"id":"channel-1","id":"other"`, 1)},
	} {
		t.Run(name, func(t *testing.T) {
			transport := mutationRoundTripFunc(func(*http.Request) (*http.Response, error) {
				return mutationResponse(test.status, test.body), nil
			})
			prepared, err := NewConversationMutations(mutationClient(t, transport)).PrepareDirect(ResolveConversationMutationInput{"self", []string{"peer"}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = prepared.Execute(context.Background()); !outcomeUnknown(err) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestConversationPreparationRejectsInvalidMembershipWithoutDispatch(t *testing.T) {
	var calls atomic.Int32
	transport := mutationRoundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, fmt.Errorf("unexpected")
	})
	service := NewConversationMutations(mutationClient(t, transport))
	if _, err := service.PrepareGroup(ResolveConversationMutationInput{"self", []string{"a", "b"}}); err != nil {
		t.Fatalf("two peers rejected: %v", err)
	}
	eight := []string{"a", "b", "c", "d", "e", "f", "g", "h"}
	for name, prepare := range map[string]func() error{
		"direct missing peer": func() error {
			_, err := service.PrepareDirect(ResolveConversationMutationInput{"self", nil})
			return err
		},
		"direct extra peer": func() error {
			_, err := service.PrepareDirect(ResolveConversationMutationInput{"self", []string{"a", "b"}})
			return err
		},
		"group missing peer": func() error {
			_, err := service.PrepareGroup(ResolveConversationMutationInput{"self", []string{"a"}})
			return err
		},
		"group too many peers": func() error {
			_, err := service.PrepareGroup(ResolveConversationMutationInput{"self", eight})
			return err
		},
		"self peer": func() error {
			_, err := service.PrepareDirect(ResolveConversationMutationInput{"self", []string{"self"}})
			return err
		},
		"duplicate peer": func() error {
			_, err := service.PrepareGroup(ResolveConversationMutationInput{"self", []string{"a", "a"}})
			return err
		},
		"unsafe peer": func() error {
			_, err := service.PrepareDirect(ResolveConversationMutationInput{"self", []string{"../peer"}})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := prepare(); !errors.Is(err, ErrInvalidMutationRequest) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("calls = %d", calls.Load())
	}
}
