package mattermost

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

const validReactionResponse = `{"user_id":"user-1","post_id":"post-1","channel_id":"channel-1","emoji_name":"ship_it+1","create_at":100,"update_at":100,"delete_at":0,"remote_id":""}`

func TestPreparedAddReactionBindsExactRequestAndResponse(t *testing.T) {
	var calls atomic.Int32
	transport := mutationRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		body, _ := io.ReadAll(request.Body)
		if request.Method != http.MethodPost || request.URL.Path != "/api/v4/reactions" || string(body) != `{"user_id":"user-1","post_id":"post-1","emoji_name":"ship_it+1"}` {
			t.Fatalf("request = %s %s %s", request.Method, request.URL.Path, body)
		}
		return mutationResponse(http.StatusOK, validReactionResponse), nil
	})
	prepared, err := NewPostMutations(mutationClient(t, transport)).PrepareAddReaction(ReactionMutationInput{"post-1", "channel-1", "user-1", "Ship_It+1"})
	if err != nil || calls.Load() != 0 {
		t.Fatalf("prepare=%v calls=%d", err, calls.Load())
	}
	result, err := prepared.Execute(context.Background())
	if err != nil || result.PostID != "post-1" || calls.Load() != 1 {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, calls.Load())
	}
}

func TestPreparedAddReactionRejectsUnboundSuccess(t *testing.T) {
	for name, body := range map[string]string{
		"wrong user":      strings.Replace(validReactionResponse, `"user_id":"user-1"`, `"user_id":"user-2"`, 1),
		"wrong post":      strings.Replace(validReactionResponse, `"post_id":"post-1"`, `"post_id":"post-2"`, 1),
		"wrong channel":   strings.Replace(validReactionResponse, `"channel_id":"channel-1"`, `"channel_id":"channel-2"`, 1),
		"wrong emoji":     strings.Replace(validReactionResponse, `"emoji_name":"ship_it+1"`, `"emoji_name":"other"`, 1),
		"deleted":         strings.Replace(validReactionResponse, `"delete_at":0`, `"delete_at":1`, 1),
		"remote":          strings.Replace(validReactionResponse, `"remote_id":""`, `"remote_id":"remote-1"`, 1),
		"null remote":     strings.Replace(validReactionResponse, `"remote_id":""`, `"remote_id":null`, 1),
		"duplicate field": strings.Replace(validReactionResponse, `"user_id":"user-1"`, `"user_id":"user-1","user_id":"user-2"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			transport := mutationRoundTripFunc(func(*http.Request) (*http.Response, error) { return mutationResponse(http.StatusOK, body), nil })
			prepared, err := NewPostMutations(mutationClient(t, transport)).PrepareAddReaction(ReactionMutationInput{"post-1", "channel-1", "user-1", "ship_it+1"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = prepared.Execute(context.Background()); !outcomeUnknown(err) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestPreparedRemoveReactionUsesExactPathAndStatus(t *testing.T) {
	transport := mutationRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodDelete || request.URL.Path != "/api/v4/users/user-1/posts/post-1/reactions/ship_it+1" || request.GetBody != nil {
			t.Fatalf("request = %s %s getBody=%v", request.Method, request.URL.Path, request.GetBody != nil)
		}
		return mutationResponse(http.StatusOK, `{"status":"OK"}`), nil
	})
	prepared, err := NewPostMutations(mutationClient(t, transport)).PrepareRemoveReaction(ReactionMutationInput{"post-1", "channel-1", "user-1", "Ship_It+1"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := prepared.Execute(context.Background())
	if err != nil || result.PostID != "post-1" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestReactionPreparationRejectsInvalidIdentityWithoutDispatch(t *testing.T) {
	var calls atomic.Int32
	transport := mutationRoundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return mutationResponse(http.StatusOK, validReactionResponse), nil
	})
	service := NewPostMutations(mutationClient(t, transport))
	for name, input := range map[string]ReactionMutationInput{
		"post":    {"bad/post", "channel-1", "user-1", "ship_it"},
		"channel": {"post-1", "bad/channel", "user-1", "ship_it"},
		"user":    {"post-1", "channel-1", "bad/user", "ship_it"},
		"emoji":   {"post-1", "channel-1", "user-1", "not:emoji"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := service.PrepareAddReaction(input); err != ErrInvalidMutationRequest {
				t.Fatalf("error = %v", err)
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("calls = %d", calls.Load())
	}
}
