package apply

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ardasevinc/mattermost-cli/internal/api"
	"github.com/ardasevinc/mattermost-cli/internal/mattermost"
	"github.com/ardasevinc/mattermost-cli/internal/stageinput"
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

func TestApplyAttachmentPostPreservesShortAndLongMarkdownAndOrderedBytes(t *testing.T) {
	for name, message := range map[string]string{
		"short": "# heading\n\n- one\n- **two**\n",
		"long":  strings.Repeat("界", 16_382) + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			var uploads, posts atomic.Int32
			files := [][]byte{[]byte("first\x00file"), []byte("second file\n")}
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				switch request.URL.Path {
				case "/api/v4/users/me":
					_, _ = io.WriteString(response, `{"id":"self","username":"arda"}`)
				case "/api/v4/channels/channel-1":
					_, _ = io.WriteString(response, `{"id":"channel-1","team_id":"team-1","type":"O","name":"town-square","display_name":"Town Square"}`)
				case "/api/v4/channels/channel-1/members/self":
					_, _ = io.WriteString(response, `{"channel_id":"channel-1","user_id":"self"}`)
				case "/api/v4/files":
					index := int(uploads.Add(1)) - 1
					got, _ := io.ReadAll(request.Body)
					filename := request.URL.Query().Get("filename")
					if index >= len(files) || !bytes.Equal(got, files[index]) || request.URL.Query().Get("channel_id") != "channel-1" || filename != fmt.Sprintf("file-%d.bin", index+1) {
						response.WriteHeader(http.StatusBadRequest)
						return
					}
					response.WriteHeader(http.StatusCreated)
					_, _ = fmt.Fprintf(response, `{"file_infos":[{"id":"file-%d","user_id":"self","channel_id":"channel-1","create_at":100,"update_at":100,"delete_at":0,"name":%q,"size":%d}]}`, index+1, filename, len(got))
				case "/api/v4/posts":
					posts.Add(1)
					var input struct {
						ChannelID     string   `json:"channel_id"`
						Message       string   `json:"message"`
						PendingPostID string   `json:"pending_post_id"`
						FileIDs       []string `json:"file_ids"`
					}
					if json.NewDecoder(request.Body).Decode(&input) != nil || input.ChannelID != "channel-1" || input.Message != message || !slices.Equal(input.FileIDs, []string{"file-1", "file-2"}) {
						response.WriteHeader(http.StatusBadRequest)
						return
					}
					response.WriteHeader(http.StatusCreated)
					_, _ = io.WriteString(response, mutationPostResponse("post-1", "channel-1", "self", message, "", input.PendingPostID, input.FileIDs, 101, 101))
				default:
					http.NotFound(response, request)
				}
			}))
			defer server.Close()
			store := openApplyStore(t)
			stage := createAttachmentApplyStage(t, store, server.URL+"/api/v4", message, files)
			service, client := applyAttachmentService(t, server.URL, store)
			defer client.Close()
			receipt, err := service.Apply(context.Background(), applyClaim(stage, "apply-attachments-"+name))
			if err != nil || receipt.Outcome != stagestore.OutcomeSucceeded || len(receipt.Steps) != 3 || uploads.Load() != 2 || posts.Load() != 1 {
				t.Fatalf("receipt=%+v uploads=%d posts=%d err=%v", receipt, uploads.Load(), posts.Load(), err)
			}
		})
	}
}

func TestApplyPreSpoolsEveryAttachmentBeforeFirstDispatch(t *testing.T) {
	var writes atomic.Int32
	server := attachmentTargetServer(t, func(response http.ResponseWriter, request *http.Request) {
		writes.Add(1)
		response.WriteHeader(http.StatusInternalServerError)
	})
	defer server.Close()
	store := openApplyStore(t)
	files := [][]byte{[]byte("first"), []byte("second")}
	stage, paths := createAttachmentApplyStageWithPaths(t, store, server.URL+"/api/v4", "body", files)
	if err := os.WriteFile(paths[1], []byte("drifted"), 0o600); err != nil {
		t.Fatal(err)
	}
	service, client := applyAttachmentService(t, server.URL, store)
	defer client.Close()
	_, err := service.Apply(context.Background(), applyClaim(stage, "apply-drifted-attachment"))
	detail, showErr := store.Show(context.Background(), stage.Stage.ID)
	if !errors.Is(err, ErrTargetDrift) || showErr != nil || detail.Lifecycle != stagestore.LifecycleOpen || writes.Load() != 0 {
		t.Fatalf("detail=%+v writes=%d err=%v show=%v", detail.StageSummary, writes.Load(), err, showErr)
	}
}

func TestApplyAttachmentRejectionAfterValidatedUploadIsPartial(t *testing.T) {
	var uploads, posts atomic.Int32
	server := attachmentTargetServer(t, func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/api/v4/posts" {
			posts.Add(1)
			response.WriteHeader(http.StatusInternalServerError)
			return
		}
		index := uploads.Add(1)
		if index == 2 {
			response.WriteHeader(http.StatusForbidden)
			return
		}
		name := request.URL.Query().Get("filename")
		body, _ := io.ReadAll(request.Body)
		response.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprintf(response, `{"file_infos":[{"id":"file-1","user_id":"self","channel_id":"channel-1","create_at":100,"update_at":100,"delete_at":0,"name":%q,"size":%d}]}`, name, len(body))
	})
	defer server.Close()
	store := openApplyStore(t)
	stage := createAttachmentApplyStage(t, store, server.URL+"/api/v4", "body", [][]byte{[]byte("first"), []byte("second")})
	service, client := applyAttachmentService(t, server.URL, store)
	defer client.Close()
	receipt, err := service.Apply(context.Background(), applyClaim(stage, "apply-partial-upload"))
	if err != nil || receipt.Outcome != stagestore.OutcomePartial || receipt.Recovery != stagestore.RecoveryPartial || receipt.Steps[0].State != stagestore.StepValidated || receipt.Steps[1].State != stagestore.StepRejected || receipt.Steps[2].State != stagestore.StepNotSent || posts.Load() != 0 {
		t.Fatalf("receipt=%+v uploads=%d posts=%d err=%v", receipt, uploads.Load(), posts.Load(), err)
	}
}

func TestApplyAttachmentTargetDriftAfterUploadsLeavesPartialWithoutPost(t *testing.T) {
	var channelReads, uploads, posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v4/users/me":
			_, _ = io.WriteString(response, `{"id":"self","username":"arda"}`)
		case "/api/v4/channels/channel-1":
			team := "team-1"
			if channelReads.Add(1) > 1 {
				team = "drifted"
			}
			_, _ = fmt.Fprintf(response, `{"id":"channel-1","team_id":%q,"type":"O","name":"town-square","display_name":"Town Square"}`, team)
		case "/api/v4/channels/channel-1/members/self":
			_, _ = io.WriteString(response, `{"channel_id":"channel-1","user_id":"self"}`)
		case "/api/v4/files":
			uploads.Add(1)
			name := request.URL.Query().Get("filename")
			body, _ := io.ReadAll(request.Body)
			response.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprintf(response, `{"file_infos":[{"id":"file-1","user_id":"self","channel_id":"channel-1","create_at":100,"update_at":100,"delete_at":0,"name":%q,"size":%d}]}`, name, len(body))
		case "/api/v4/posts":
			posts.Add(1)
			response.WriteHeader(http.StatusInternalServerError)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	store := openApplyStore(t)
	stage := createAttachmentApplyStage(t, store, server.URL+"/api/v4", "body", [][]byte{[]byte("file")})
	service, client := applyAttachmentService(t, server.URL, store)
	defer client.Close()
	receipt, err := service.Apply(context.Background(), applyClaim(stage, "apply-post-upload-drift"))
	if err != nil || receipt.Outcome != stagestore.OutcomePartial || receipt.Steps[0].State != stagestore.StepValidated || receipt.Steps[1].State != stagestore.StepNotSent || uploads.Load() != 1 || posts.Load() != 0 {
		t.Fatalf("receipt=%+v reads=%d uploads=%d posts=%d err=%v", receipt, channelReads.Load(), uploads.Load(), posts.Load(), err)
	}
}

func TestApplyAttachmentJournalHandoffAfterUploadReturnsDurablePartialReceipt(t *testing.T) {
	var uploads, posts atomic.Int32
	server := attachmentTargetServer(t, func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v4/files":
			uploads.Add(1)
			name := request.URL.Query().Get("filename")
			body, _ := io.ReadAll(request.Body)
			response.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprintf(response, `{"file_infos":[{"id":"file-1","user_id":"self","channel_id":"channel-1","create_at":100,"update_at":100,"delete_at":0,"name":%q,"size":%d}]}`, name, len(body))
		case "/api/v4/posts":
			posts.Add(1)
			response.WriteHeader(http.StatusInternalServerError)
		}
	})
	defer server.Close()
	store := openApplyStore(t)
	stage := createAttachmentApplyStage(t, store, server.URL+"/api/v4", "body", [][]byte{[]byte("file")})
	faults := &faultStore{Store: store, beginOrdinal: 2, beginErr: errors.New("journal unavailable")}
	service, client := applyAttachmentServiceWithStore(t, server.URL, faults, store.StateDir())
	defer client.Close()
	receipt, err := service.Apply(context.Background(), applyClaim(stage, "apply-post-handoff-failure"))
	if err != nil || receipt.Outcome != stagestore.OutcomePartial || receipt.Recovery != stagestore.RecoveryPartial || receipt.Steps[0].State != stagestore.StepValidated || receipt.Steps[1].State != stagestore.StepNotSent || uploads.Load() != 1 || posts.Load() != 0 {
		t.Fatalf("receipt=%+v uploads=%d posts=%d err=%v", receipt, uploads.Load(), posts.Load(), err)
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

func createAttachmentApplyStage(t *testing.T, store *stagestore.Store, serverURL, body string, files [][]byte) stagestore.MutationResult {
	stage, _ := createAttachmentApplyStageWithPaths(t, store, serverURL, body, files)
	return stage
}

func createAttachmentApplyStageWithPaths(t *testing.T, store *stagestore.Store, serverURL, body string, files [][]byte) (stagestore.MutationResult, []string) {
	t.Helper()
	dir, err := os.MkdirTemp(".", ".apply-attachment-")
	if err != nil {
		t.Fatal(err)
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	inputs := make([]stageinput.Attachment, len(files))
	paths := make([]string, len(files))
	for i, content := range files {
		paths[i] = filepath.Join(dir, fmt.Sprintf("file-%d.bin", i+1))
		if err = os.WriteFile(paths[i], content, 0o600); err != nil {
			t.Fatal(err)
		}
		inputs[i] = stageinput.Attachment{Path: paths[i], MediaType: "application/octet-stream"}
	}
	attachments, err := stageinput.Bind(context.Background(), inputs, [][]byte{[]byte("token")})
	if err != nil {
		t.Fatal(err)
	}
	destination, err := json.Marshal(staging.Destination{Kind: "conversation", ChannelID: "channel-1", ChannelType: "public", TeamID: stringPointer("team-1"), ParticipantIDs: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	steps := make([]string, 0, len(files)+1)
	for i := range files {
		steps = append(steps, fmt.Sprintf(`{"ordinal":%d,"type":"upload_attachment","condition":"always"}`, i+1))
	}
	steps = append(steps, fmt.Sprintf(`{"ordinal":%d,"type":"create_post","condition":"always"}`, len(files)+1))
	created, err := store.Create(context.Background(), stagestore.CreateInput{RequestDigest: sha256.Sum256([]byte(body)), Operation: stagestore.CreatePost, ServerURL: serverURL, UserID: "self",
		Content: stagestore.RevisionContent{Body: []byte(body), Destination: destination, Plan: json.RawMessage(`{"steps":[` + strings.Join(steps, ",") + `]}`), Attachments: attachments}})
	if err != nil {
		t.Fatal(err)
	}
	return created.MutationResult, paths
}

func applyAttachmentService(t *testing.T, serverURL string, store *stagestore.Store) (*Service, *api.Client) {
	return applyAttachmentServiceWithStore(t, serverURL, store, store.StateDir())
}

func applyAttachmentServiceWithStore(t *testing.T, serverURL string, store Store, stateDirectory string) (*Service, *api.Client) {
	t.Helper()
	client, err := api.New(serverURL, "token")
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(serverURL+"/api/v4", "", [][]byte{[]byte("token")}, store, mattermost.NewUsers(client), mattermost.NewChannels(client), mattermost.NewPosts(client),
		mattermost.NewConversationMutations(client), mattermost.NewPostMutations(client), WithAttachmentExecution(stateDirectory, mattermost.NewFileMutations(client)))
	if err != nil {
		client.Close()
		t.Fatal(err)
	}
	return service, client
}

func attachmentTargetServer(t *testing.T, write func(http.ResponseWriter, *http.Request)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v4/users/me":
			_, _ = io.WriteString(response, `{"id":"self","username":"arda"}`)
		case "/api/v4/channels/channel-1":
			_, _ = io.WriteString(response, `{"id":"channel-1","team_id":"team-1","type":"O","name":"town-square","display_name":"Town Square"}`)
		case "/api/v4/channels/channel-1/members/self":
			_, _ = io.WriteString(response, `{"channel_id":"channel-1","user_id":"self"}`)
		case "/api/v4/files", "/api/v4/posts":
			write(response, request)
		default:
			http.NotFound(response, request)
		}
	}))
}
