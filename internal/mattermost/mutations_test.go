package mattermost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ardasevinc/mattermost-cli/v2/internal/api"
)

type mutationRoundTripFunc func(*http.Request) (*http.Response, error)

func (f mutationRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func mutationClient(t *testing.T, transport http.RoundTripper) *api.Client {
	t.Helper()
	client, err := api.New("https://mattermost.example", "active-token", api.WithRoundTripper(transport))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	return client
}

func mutationResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func postMutationJSON(id, channelID, userID, message, rootID, pendingID string, fileIDs []string, createAt, updateAt int64) string {
	raw, _ := json.Marshal(map[string]any{
		"id": id, "channel_id": channelID, "user_id": userID, "message": message, "create_at": createAt, "update_at": updateAt,
		"delete_at": 0, "root_id": rootID, "file_ids": fileIDs, "pending_post_id": pendingID, "type": "",
	})
	return string(raw)
}

func TestPreparedCreatePreservesShortAndLongMarkdownExactly(t *testing.T) {
	for name, message := range map[string]string{
		"short": "# heading\n\n**bold** and `code`\n",
		"long":  strings.Repeat("界", 16_382) + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			transport := mutationRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				calls.Add(1)
				if request.Method != http.MethodPost || request.URL.Path != "/api/v4/posts" || request.GetBody != nil {
					t.Fatalf("request = %s %s getBody=%v", request.Method, request.URL.Path, request.GetBody != nil)
				}
				body, err := io.ReadAll(request.Body)
				if err != nil {
					t.Fatal(err)
				}
				var decoded struct {
					ChannelID     string   `json:"channel_id"`
					Message       string   `json:"message"`
					RootID        string   `json:"root_id"`
					FileIDs       []string `json:"file_ids"`
					PendingPostID string   `json:"pending_post_id"`
				}
				if err = json.Unmarshal(body, &decoded); err != nil || decoded.Message != message || decoded.ChannelID != "channel-1" || decoded.RootID != "root-1" || !slicesEqual(decoded.FileIDs, []string{"file-1"}) || decoded.PendingPostID != "pending_1" {
					t.Fatalf("body mismatch: %s / %v", body, err)
				}
				return mutationResponse(http.StatusCreated, postMutationJSON("post-1", "channel-1", "user-1", message, "root-1", "pending_1", []string{"file-1"}, 100, 100)), nil
			})
			service := NewPostMutations(mutationClient(t, transport))
			prepared, err := service.PrepareCreate(CreatePostMutationInput{"channel-1", "user-1", message, "root-1", []string{"file-1"}, "pending_1"})
			if err != nil || calls.Load() != 0 {
				t.Fatalf("prepare = %v calls=%d", err, calls.Load())
			}
			result, err := prepared.Execute(context.Background())
			if err != nil || result != (CreatePostMutationResult{"post-1", 100, "channel-1", "user-1", "pending_1"}) || calls.Load() != 1 {
				t.Fatalf("result=%+v err=%v calls=%d", result, err, calls.Load())
			}
		})
	}
}

func TestPreparedEditFreezesBodyAndValidatesBoundPost(t *testing.T) {
	message := "edited **markdown**\n"
	transport := mutationRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(request.Body)
		if request.Method != http.MethodPut || request.URL.Path != "/api/v4/posts/post-1/patch" || string(body) != `{"message":"edited **markdown**\n"}` {
			t.Fatalf("request = %s %s %s", request.Method, request.URL.Path, body)
		}
		return mutationResponse(http.StatusOK, postMutationJSON("post-1", "channel-1", "user-1", message, "root-1", "", []string{"file-1"}, 100, 101)), nil
	})
	prepared, err := NewPostMutations(mutationClient(t, transport)).PrepareEdit(EditPostMutationInput{"post-1", "channel-1", "user-1", message, "root-1", []string{"file-1"}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := prepared.Execute(context.Background())
	if err != nil || result != (EditPostMutationResult{"post-1", 101}) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestPreparedEditRejectsDifferentResponsePost(t *testing.T) {
	transport := mutationRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return mutationResponse(http.StatusOK, postMutationJSON("post-2", "channel-1", "user-1", "edited", "", "", nil, 100, 101)), nil
	})
	prepared, err := NewPostMutations(mutationClient(t, transport)).PrepareEdit(EditPostMutationInput{"post-1", "channel-1", "user-1", "edited", "", nil})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = prepared.Execute(context.Background()); !outcomeUnknown(err) {
		t.Fatalf("error = %v", err)
	}
}

func TestPreparedMutationsClassifyUnvalidatedSuccessAsUnknown(t *testing.T) {
	valid := postMutationJSON("post-1", "channel-1", "user-1", "hello", "", "pending_1", nil, 100, 100)
	for name, test := range map[string]struct {
		status int
		body   string
	}{
		"wrong status":     {http.StatusOK, valid},
		"wrong channel":    {http.StatusCreated, postMutationJSON("post-1", "other", "user-1", "hello", "", "pending_1", nil, 100, 100)},
		"wrong message":    {http.StatusCreated, postMutationJSON("post-1", "channel-1", "user-1", "changed", "", "pending_1", nil, 100, 100)},
		"wrong pending id": {http.StatusCreated, postMutationJSON("post-1", "channel-1", "user-1", "hello", "", "other", nil, 100, 100)},
		"duplicate field":  {http.StatusCreated, strings.Replace(valid, `"id":"post-1"`, `"id":"post-1","id":"post-2"`, 1)},
	} {
		t.Run(name, func(t *testing.T) {
			transport := mutationRoundTripFunc(func(*http.Request) (*http.Response, error) { return mutationResponse(test.status, test.body), nil })
			prepared, err := NewPostMutations(mutationClient(t, transport)).PrepareCreate(CreatePostMutationInput{"channel-1", "user-1", "hello", "", nil, "pending_1"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = prepared.Execute(context.Background()); !outcomeUnknown(err) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestPreparedDeleteRequiresExactStatusOK(t *testing.T) {
	for name, test := range map[string]struct {
		body    string
		unknown bool
	}{"exact": {`{"status":"OK"}`, false}, "wrong case": {`{"status":"ok"}`, true}, "extra": {`{"status":"OK","post_id":"post-1"}`, true}} {
		t.Run(name, func(t *testing.T) {
			transport := mutationRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				if request.Method != http.MethodDelete || request.URL.Path != "/api/v4/posts/post-1" || request.GetBody != nil {
					t.Fatalf("request = %s %s getBody=%v", request.Method, request.URL.Path, request.GetBody != nil)
				}
				return mutationResponse(http.StatusOK, test.body), nil
			})
			prepared, err := NewPostMutations(mutationClient(t, transport)).PrepareDelete(DeletePostMutationInput{"post-1"})
			if err != nil {
				t.Fatal(err)
			}
			result, err := prepared.Execute(context.Background())
			if test.unknown != outcomeUnknown(err) || !test.unknown && result.PostID != "post-1" {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestPostMutationPreparationRejectsInvalidInputsWithoutDispatch(t *testing.T) {
	var calls atomic.Int32
	transport := mutationRoundTripFunc(func(*http.Request) (*http.Response, error) { calls.Add(1); return nil, fmt.Errorf("unexpected") })
	service := NewPostMutations(mutationClient(t, transport))
	invalidUTF8 := string([]byte{0xff})
	for name, prepare := range map[string]func() error{
		"invalid channel": func() error {
			_, err := service.PrepareCreate(CreatePostMutationInput{"bad/channel", "user-1", "hi", "", nil, "pending_1"})
			return err
		},
		"invalid utf8": func() error {
			_, err := service.PrepareCreate(CreatePostMutationInput{"channel-1", "user-1", invalidUTF8, "", nil, "pending_1"})
			return err
		},
		"too many runes": func() error {
			_, err := service.PrepareCreate(CreatePostMutationInput{"channel-1", "user-1", strings.Repeat("a", 16_384), "", nil, "pending_1"})
			return err
		},
		"duplicate files": func() error {
			_, err := service.PrepareEdit(EditPostMutationInput{"post-1", "channel-1", "user-1", "hi", "", []string{"file-1", "file-1"}})
			return err
		},
		"too many files": func() error {
			_, err := service.PrepareCreate(CreatePostMutationInput{"channel-1", "user-1", "hi", "", []string{"a", "b", "c", "d", "e", "f"}, "pending_1"})
			return err
		},
		"invalid post": func() error { _, err := service.PrepareDelete(DeletePostMutationInput{"../post"}); return err },
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

func outcomeUnknown(err error) bool {
	var unknown *api.OutcomeUnknownError
	return errors.As(err, &unknown)
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
