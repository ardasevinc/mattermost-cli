package apply

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ardasevinc/mattermost-cli/internal/mattermost"
	"github.com/ardasevinc/mattermost-cli/internal/stagestore"
	"github.com/ardasevinc/mattermost-cli/internal/staging"
)

func TestApplyCreatePostPreservesStagedMarkdownExactly(t *testing.T) {
	for name, message := range map[string]string{
		"short": "# heading\n\n**bold** and `code`\n",
		"long":  strings.Repeat("界", 16_382) + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			var writes atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				switch request.URL.RequestURI() {
				case "/api/v4/users/me":
					_, _ = io.WriteString(response, `{"id":"self","username":"arda"}`)
				case "/api/v4/channels/channel-1":
					_, _ = io.WriteString(response, `{"id":"channel-1","team_id":"team-1","type":"O","name":"town-square","display_name":"Town Square"}`)
				case "/api/v4/channels/channel-1/members/self":
					_, _ = io.WriteString(response, `{"channel_id":"channel-1","user_id":"self"}`)
				case "/api/v4/posts":
					writes.Add(1)
					var input struct {
						ChannelID     string `json:"channel_id"`
						Message       string `json:"message"`
						PendingPostID string `json:"pending_post_id"`
					}
					decoder := json.NewDecoder(request.Body)
					if request.Method != http.MethodPost || decoder.Decode(&input) != nil || input.ChannelID != "channel-1" || input.Message != message || input.PendingPostID == "" {
						response.WriteHeader(http.StatusBadRequest)
						return
					}
					response.WriteHeader(http.StatusCreated)
					_, _ = io.WriteString(response, mutationPostResponse("created-1", "channel-1", "self", message, "", input.PendingPostID, nil, 100, 100))
				default:
					http.NotFound(response, request)
				}
			}))
			defer server.Close()
			store := openApplyStore(t)
			stage := createPostApplyStage(t, store, server.URL+"/api/v4", stagestore.CreatePost, message, staging.Destination{
				Kind: "conversation", ChannelID: "channel-1", ChannelType: "public", TeamID: stringPointer("team-1"), ParticipantIDs: []string{},
			})
			service, client := applyService(t, server.URL, store)
			defer client.Close()
			receipt, err := service.Apply(context.Background(), applyClaim(stage, "apply-create-"+name))
			if err != nil || receipt.Outcome != stagestore.OutcomeSucceeded || receipt.Steps[0].State != stagestore.StepValidated || writes.Load() != 1 {
				t.Fatalf("receipt=%+v err=%v writes=%d", receipt, err, writes.Load())
			}
		})
	}
}

func TestApplyReplyRevalidatesSelectedPostAndCanonicalRoot(t *testing.T) {
	const message = "thread **reply**\n"
	var writes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.RequestURI() {
		case "/api/v4/users/me":
			_, _ = io.WriteString(response, `{"id":"self","username":"arda"}`)
		case "/api/v4/posts/reply-1":
			_, _ = io.WriteString(response, livePostJSON("reply-1", "channel-1", "peer", "old", "root-1", nil, 2))
		case "/api/v4/posts/root-1":
			_, _ = io.WriteString(response, livePostJSON("root-1", "channel-1", "peer", "root", "", nil, 1))
		case "/api/v4/channels/channel-1":
			_, _ = io.WriteString(response, `{"id":"channel-1","team_id":"team-1","type":"P","name":"private","display_name":"Private"}`)
		case "/api/v4/channels/channel-1/members/self":
			_, _ = io.WriteString(response, `{"channel_id":"channel-1","user_id":"self"}`)
		case "/api/v4/posts":
			writes.Add(1)
			var input struct {
				ChannelID     string `json:"channel_id"`
				Message       string `json:"message"`
				RootID        string `json:"root_id"`
				PendingPostID string `json:"pending_post_id"`
			}
			if json.NewDecoder(request.Body).Decode(&input) != nil || input.ChannelID != "channel-1" || input.Message != message || input.RootID != "root-1" {
				response.WriteHeader(http.StatusBadRequest)
				return
			}
			response.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(response, mutationPostResponse("new-reply", "channel-1", "self", message, "root-1", input.PendingPostID, nil, 100, 100))
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	store := openApplyStore(t)
	stage := createPostApplyStage(t, store, server.URL+"/api/v4", stagestore.Reply, message, staging.Destination{
		Kind: "post", ChannelID: "channel-1", ChannelType: "private", TeamID: stringPointer("team-1"), PostID: stringPointer("reply-1"), RootPostID: stringPointer("root-1"), ParticipantIDs: []string{},
	})
	service, client := applyService(t, server.URL, store)
	defer client.Close()
	receipt, err := service.Apply(context.Background(), applyClaim(stage, "apply-reply"))
	if err != nil || receipt.Outcome != stagestore.OutcomeSucceeded || writes.Load() != 1 {
		t.Fatalf("receipt=%+v err=%v writes=%d", receipt, err, writes.Load())
	}
}

func TestApplyEditSkipsSatisfiedAndRejectsBoundStateDrift(t *testing.T) {
	for _, test := range []struct {
		name, liveMessage, stagedMessage string
		liveUpdate                       int64
		wantSkip                         bool
		wantDrift                        bool
	}{
		{name: "already-satisfied", liveMessage: "same", stagedMessage: "same", liveUpdate: 2, wantSkip: true},
		{name: "stale-update", liveMessage: "old", stagedMessage: "new", liveUpdate: 3, wantDrift: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var writes atomic.Int32
			bound := mattermost.Post{ID: "post-1", ChannelID: "channel-1", UserID: "self", Message: test.liveMessage, CreateAt: 1, UpdateAt: 2, RootID: "", Type: "", FileIDs: []string{}}
			server := postTargetServer(t, func(response http.ResponseWriter, request *http.Request) bool {
				if request.URL.RequestURI() == "/api/v4/posts/post-1" {
					_, _ = io.WriteString(response, livePostJSON("post-1", "channel-1", "self", test.liveMessage, "", nil, test.liveUpdate))
					return true
				}
				if request.URL.RequestURI() == "/api/v4/posts/post-1/patch" {
					writes.Add(1)
					response.WriteHeader(http.StatusInternalServerError)
					return true
				}
				return false
			})
			defer server.Close()
			store := openApplyStore(t)
			stage := createPostApplyStage(t, store, server.URL+"/api/v4", stagestore.EditPost, test.stagedMessage, boundPostDestination(bound))
			service, client := applyService(t, server.URL, store)
			defer client.Close()
			receipt, err := service.Apply(context.Background(), applyClaim(stage, "apply-edit-"+test.name))
			if test.wantDrift {
				detail, showErr := store.Show(context.Background(), stage.Stage.ID)
				if !errors.Is(err, ErrTargetDrift) || showErr != nil || detail.Lifecycle != stagestore.LifecycleOpen || writes.Load() != 0 {
					t.Fatalf("detail=%+v receipt=%+v err=%v showErr=%v writes=%d", detail.StageSummary, receipt, err, showErr, writes.Load())
				}
				return
			}
			if err != nil || !test.wantSkip || receipt.Outcome != stagestore.OutcomeAlreadySatisfied || writes.Load() != 0 {
				t.Fatalf("receipt=%+v err=%v writes=%d", receipt, err, writes.Load())
			}
		})
	}
}

func TestApplyEditAndDeleteExecuteOneBoundMutation(t *testing.T) {
	for _, operation := range []stagestore.Operation{stagestore.EditPost, stagestore.DeletePost} {
		t.Run(string(operation), func(t *testing.T) {
			var writes atomic.Int32
			post := mattermost.Post{ID: "post-1", ChannelID: "channel-1", UserID: "self", Message: "old", CreateAt: 1, UpdateAt: 2, RootID: "", Type: "", FileIDs: []string{}}
			server := postTargetServer(t, func(response http.ResponseWriter, request *http.Request) bool {
				if request.URL.RequestURI() == "/api/v4/posts/post-1" && request.Method == http.MethodGet {
					_, _ = io.WriteString(response, livePostJSON("post-1", "channel-1", "self", "old", "", nil, 2))
					return true
				}
				if request.URL.RequestURI() == "/api/v4/posts/post-1/patch" && request.Method == http.MethodPut {
					writes.Add(1)
					_, _ = io.WriteString(response, mutationPostResponse("post-1", "channel-1", "self", "new", "", "", nil, 1, 3))
					return true
				}
				return false
			})
			if operation == stagestore.DeletePost {
				server.Close()
				server = postTargetServer(t, func(response http.ResponseWriter, request *http.Request) bool {
					if request.URL.RequestURI() == "/api/v4/posts/post-1" && request.Method == http.MethodGet {
						_, _ = io.WriteString(response, livePostJSON("post-1", "channel-1", "self", "old", "", nil, 2))
						return true
					}
					if request.URL.RequestURI() == "/api/v4/posts/post-1" && request.Method == http.MethodDelete {
						writes.Add(1)
						_, _ = io.WriteString(response, `{"status":"OK"}`)
						return true
					}
					return false
				})
			}
			defer server.Close()
			body := "new"
			if operation == stagestore.DeletePost {
				body = ""
			}
			store := openApplyStore(t)
			stage := createPostApplyStage(t, store, server.URL+"/api/v4", operation, body, boundPostDestination(post))
			service, client := applyService(t, server.URL, store)
			defer client.Close()
			receipt, err := service.Apply(context.Background(), applyClaim(stage, "apply-"+string(operation)))
			if err != nil || receipt.Outcome != stagestore.OutcomeSucceeded || writes.Load() != 1 {
				t.Fatalf("receipt=%+v err=%v writes=%d", receipt, err, writes.Load())
			}
		})
	}
}

func createPostApplyStage(t *testing.T, store *stagestore.Store, serverURL string, operation stagestore.Operation, body string, destination staging.Destination) stagestore.MutationResult {
	t.Helper()
	destinationJSON, err := json.Marshal(destination)
	if err != nil {
		t.Fatal(err)
	}
	step := "create_post"
	if operation == stagestore.EditPost {
		step = "edit_post"
	} else if operation == stagestore.DeletePost {
		step = "delete_post"
	}
	plan := json.RawMessage(fmt.Sprintf(`{"steps":[{"ordinal":1,"type":%q,"condition":"always"}]}`, step))
	result, err := store.Create(context.Background(), stagestore.CreateInput{RequestDigest: sha256.Sum256([]byte(string(operation) + "\x00" + body)), Operation: operation, ServerURL: serverURL, UserID: "self", Content: stagestore.RevisionContent{Body: []byte(body), Destination: destinationJSON, Plan: plan}})
	if err != nil {
		t.Fatal(err)
	}
	return result.MutationResult
}

func boundPostDestination(post mattermost.Post) staging.Destination {
	postID := post.ID
	return staging.Destination{Kind: "post", ChannelID: post.ChannelID, ChannelType: "public", TeamID: stringPointer("team-1"), PostID: &postID, ParticipantIDs: []string{}, PostState: &staging.PostState{
		AuthorUserID: post.UserID, UpdateAt: post.UpdateAt, ContentDigest: staging.PostContentDigest(post, [][]byte{[]byte("token")}),
	}}
}

func postTargetServer(t *testing.T, handle func(http.ResponseWriter, *http.Request) bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.RequestURI() {
		case "/api/v4/users/me":
			_, _ = io.WriteString(response, `{"id":"self","username":"arda"}`)
		case "/api/v4/channels/channel-1":
			_, _ = io.WriteString(response, `{"id":"channel-1","team_id":"team-1","type":"O","name":"town-square","display_name":"Town Square"}`)
		case "/api/v4/channels/channel-1/members/self":
			_, _ = io.WriteString(response, `{"channel_id":"channel-1","user_id":"self"}`)
		default:
			if !handle(response, request) {
				http.NotFound(response, request)
			}
		}
	}))
}

func livePostJSON(id, channelID, userID, message, rootID string, fileIDs []string, updateAt int64) string {
	raw, _ := json.Marshal(map[string]any{"id": id, "channel_id": channelID, "user_id": userID, "message": message, "create_at": 1, "update_at": updateAt, "delete_at": 0, "root_id": rootID, "type": "", "file_ids": fileIDs})
	return string(raw)
}

func mutationPostResponse(id, channelID, userID, message, rootID, pendingID string, fileIDs []string, createAt, updateAt int64) string {
	raw, _ := json.Marshal(map[string]any{"id": id, "channel_id": channelID, "user_id": userID, "message": message, "create_at": createAt, "update_at": updateAt, "delete_at": 0, "root_id": rootID, "file_ids": fileIDs, "pending_post_id": pendingID, "type": ""})
	return string(raw)
}

func stringPointer(value string) *string { return &value }
