package apply

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ardasevinc/mattermost-cli/internal/api"
	"github.com/ardasevinc/mattermost-cli/internal/mattermost"
	"github.com/ardasevinc/mattermost-cli/internal/stagestore"
)

func TestApplyResolveDMCreatesOnceAndReplaysDurableReceipt(t *testing.T) {
	var created atomic.Bool
	var writes atomic.Int32
	var invalidWrite atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.RequestURI() {
		case "/api/v4/users/me":
			_, _ = io.WriteString(response, `{"id":"self","username":"arda"}`)
		case "/api/v4/users/self/channels":
			if !created.Load() {
				_, _ = io.WriteString(response, `[]`)
				return
			}
			_, _ = io.WriteString(response, `[{"id":"dm-1","team_id":"","type":"D","name":"peer__self","display_name":""}]`)
		case "/api/v4/channels/dm-1/members?page=0&per_page=9":
			_, _ = io.WriteString(response, `[{"channel_id":"dm-1","user_id":"self"},{"channel_id":"dm-1","user_id":"peer"}]`)
		case "/api/v4/channels/direct":
			body, _ := io.ReadAll(request.Body)
			if request.Method != http.MethodPost || string(body) != `["peer","self"]` {
				invalidWrite.Store(true)
				response.WriteHeader(http.StatusBadRequest)
				return
			}
			writes.Add(1)
			created.Store(true)
			response.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(response, `{"id":"dm-1","create_at":100,"update_at":100,"delete_at":0,"team_id":"","type":"D","name":"peer__self","display_name":""}`)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	store := openApplyStore(t)
	createdStage := createResolveStage(t, store, server.URL+"/api/v4", stagestore.ResolveDM, "dm", []string{"peer"})
	detail, err := store.Show(context.Background(), createdStage.Stage.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = decodeResolveDestination(stagestore.ResolveDM, detail.Destination, "self"); err != nil {
		t.Fatalf("stored destination=%s err=%v", detail.Destination, err)
	}
	client, err := api.New(server.URL, "token")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	service, err := New(server.URL+"/api/v4", "", store, mattermost.NewUsers(client), mattermost.NewChannels(client), mattermost.NewConversationMutations(client))
	if err != nil {
		t.Fatal(err)
	}
	claim := applyClaim(createdStage, "apply-1")
	receipt, err := service.Apply(context.Background(), claim)
	if err != nil || receipt.Outcome != stagestore.OutcomeSucceeded || receipt.Recovery != stagestore.RecoveryForbidden || receipt.Steps[0].State != stagestore.StepValidated || writes.Load() != 1 || invalidWrite.Load() {
		t.Fatalf("receipt=%+v err=%v writes=%d invalid=%v", receipt, err, writes.Load(), invalidWrite.Load())
	}
	server.Close()
	replay, err := service.Apply(context.Background(), claim)
	if err != nil || !replay.Replay || replay.AttemptID != receipt.AttemptID || writes.Load() != 1 {
		t.Fatalf("replay=%+v err=%v writes=%d", replay, err, writes.Load())
	}
}

func TestDecodeResolveDestinationAcceptsCanonicalUnresolvedDM(t *testing.T) {
	raw := json.RawMessage(`{"channelId":null,"channelType":"dm","emoji":null,"kind":"conversation","participantIds":["peer"],"postId":null,"postState":null,"reactionPresent":null,"rootPostId":null,"teamId":null}`)
	destination, err := decodeResolveDestination(stagestore.ResolveDM, raw, "self")
	if err != nil || destination.ChannelType != "dm" || !slices.Equal(destination.ParticipantIDs, []string{"peer"}) {
		t.Fatalf("destination=%+v err=%v", destination, err)
	}
}

func TestApplyResolveDMSkipsExactExistingConversation(t *testing.T) {
	var writes atomic.Int32
	server := existingDirectServer(t, &writes)
	defer server.Close()
	store := openApplyStore(t)
	stage := createResolveStage(t, store, server.URL+"/api/v4", stagestore.ResolveDM, "dm", []string{"peer"})
	service, client := applyService(t, server.URL, store)
	defer client.Close()
	receipt, err := service.Apply(context.Background(), applyClaim(stage, "apply-skip"))
	if err != nil || receipt.Outcome != stagestore.OutcomeAlreadySatisfied || receipt.Steps[0].State != stagestore.StepSkipped || writes.Load() != 0 {
		t.Fatalf("receipt=%+v err=%v writes=%d", receipt, err, writes.Load())
	}
}

func TestApplyResolveGroupCreatesExactCanonicalConversation(t *testing.T) {
	peers := []string{"alpha", "beta"}
	nameDigest := sha1.Sum([]byte("alphabetaself"))
	groupName := fmt.Sprintf("%x", nameDigest)
	var created atomic.Bool
	var writes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.RequestURI() {
		case "/api/v4/users/me":
			_, _ = io.WriteString(response, `{"id":"self","username":"arda"}`)
		case "/api/v4/users/self/channels":
			if !created.Load() {
				_, _ = io.WriteString(response, `[]`)
				return
			}
			_, _ = fmt.Fprintf(response, `[{"id":"group-1","team_id":"","type":"G","name":%q,"display_name":"group"}]`, groupName)
		case "/api/v4/channels/group-1/members?page=0&per_page=9":
			_, _ = io.WriteString(response, `[{"channel_id":"group-1","user_id":"alpha"},{"channel_id":"group-1","user_id":"beta"},{"channel_id":"group-1","user_id":"self"}]`)
		case "/api/v4/channels/group":
			body, _ := io.ReadAll(request.Body)
			if request.Method != http.MethodPost || string(body) != `["alpha","beta","self"]` {
				response.WriteHeader(http.StatusBadRequest)
				return
			}
			writes.Add(1)
			created.Store(true)
			response.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprintf(response, `{"id":"group-1","create_at":100,"update_at":100,"delete_at":0,"team_id":"","type":"G","name":%q,"display_name":"group"}`, groupName)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	store := openApplyStore(t)
	stage := createResolveStage(t, store, server.URL+"/api/v4", stagestore.ResolveGroupDM, "group", peers)
	service, client := applyService(t, server.URL, store)
	defer client.Close()
	receipt, err := service.Apply(context.Background(), applyClaim(stage, "apply-group"))
	if err != nil || receipt.Outcome != stagestore.OutcomeSucceeded || receipt.Steps[0].State != stagestore.StepValidated || writes.Load() != 1 {
		t.Fatalf("receipt=%+v err=%v writes=%d", receipt, err, writes.Load())
	}
}

func TestApplyResolveDMAbandonsBeforeDispatchOnStaleMembership(t *testing.T) {
	var writes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.RequestURI() {
		case "/api/v4/users/me":
			_, _ = io.WriteString(response, `{"id":"self","username":"arda"}`)
		case "/api/v4/users/self/channels":
			_, _ = io.WriteString(response, `[{"id":"dm-1","team_id":"","type":"D","name":"peer__self","display_name":""}]`)
		case "/api/v4/channels/dm-1/members?page=0&per_page=9":
			_, _ = io.WriteString(response, `[{"channel_id":"dm-1","user_id":"self"}]`)
		case "/api/v4/channels/direct":
			writes.Add(1)
			response.WriteHeader(http.StatusInternalServerError)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	store := openApplyStore(t)
	stage := createResolveStage(t, store, server.URL+"/api/v4", stagestore.ResolveDM, "dm", []string{"peer"})
	service, client := applyService(t, server.URL, store)
	defer client.Close()
	_, err := service.Apply(context.Background(), applyClaim(stage, "apply-stale"))
	detail, showErr := store.Show(context.Background(), stage.Stage.ID)
	if !errors.Is(err, ErrTargetDrift) || showErr != nil || detail.Lifecycle != stagestore.LifecycleOpen || detail.Recovery != stagestore.RecoveryNone || writes.Load() != 0 {
		t.Fatalf("detail=%+v applyErr=%v showErr=%v writes=%d", detail.StageSummary, err, showErr, writes.Load())
	}
}

func TestApplyResolveDMClassifiesRejectedAndUnknown(t *testing.T) {
	for name, status := range map[string]int{"rejected": http.StatusForbidden, "unknown": http.StatusInternalServerError} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				switch request.URL.RequestURI() {
				case "/api/v4/users/me":
					_, _ = io.WriteString(response, `{"id":"self","username":"arda"}`)
				case "/api/v4/users/self/channels":
					_, _ = io.WriteString(response, `[]`)
				case "/api/v4/channels/direct":
					response.WriteHeader(status)
					_, _ = io.WriteString(response, `{}`)
				default:
					http.NotFound(response, request)
				}
			}))
			defer server.Close()
			store := openApplyStore(t)
			stage := createResolveStage(t, store, server.URL+"/api/v4", stagestore.ResolveDM, "dm", []string{"peer"})
			service, client := applyService(t, server.URL, store)
			defer client.Close()
			receipt, err := service.Apply(context.Background(), applyClaim(stage, "apply-"+name))
			wantOutcome, wantRecovery, wantState := stagestore.OutcomeRejected, stagestore.RecoveryNone, stagestore.StepRejected
			if name == "unknown" {
				wantOutcome, wantRecovery, wantState = stagestore.OutcomeUnknown, stagestore.RecoveryUnknown, stagestore.StepUnknown
			}
			if err != nil || receipt.Outcome != wantOutcome || receipt.Recovery != wantRecovery || receipt.Steps[0].State != wantState {
				t.Fatalf("receipt=%+v err=%v", receipt, err)
			}
		})
	}
}

func TestApplyResolveDMAbandonsSkippedClaimWhenJournalWriteFails(t *testing.T) {
	var writes atomic.Int32
	server := existingDirectServer(t, &writes)
	defer server.Close()
	store := openApplyStore(t)
	stage := createResolveStage(t, store, server.URL+"/api/v4", stagestore.ResolveDM, "dm", []string{"peer"})
	faults := &faultStore{Store: store, skipErr: errors.New("disk unavailable")}
	service, client := applyServiceWithStore(t, server.URL, faults)
	defer client.Close()
	_, err := service.Apply(context.Background(), applyClaim(stage, "apply-skip-fault"))
	detail, showErr := store.Show(context.Background(), stage.Stage.ID)
	if !errors.Is(err, ErrJournal) || showErr != nil || detail.Lifecycle != stagestore.LifecycleOpen || detail.Recovery != stagestore.RecoveryNone || writes.Load() != 0 {
		t.Fatalf("detail=%+v applyErr=%v showErr=%v writes=%d", detail.StageSummary, err, showErr, writes.Load())
	}
}

func TestApplyResolveDMFinishesSkippedJournalAfterCallerCancellation(t *testing.T) {
	var writes atomic.Int32
	server := existingDirectServer(t, &writes)
	defer server.Close()
	store := openApplyStore(t)
	stage := createResolveStage(t, store, server.URL+"/api/v4", stagestore.ResolveDM, "dm", []string{"peer"})
	ctx, cancel := context.WithCancel(context.Background())
	faults := &faultStore{Store: store, beforeSkip: cancel}
	service, client := applyServiceWithStore(t, server.URL, faults)
	defer client.Close()
	receipt, err := service.Apply(ctx, applyClaim(stage, "apply-skip-canceled"))
	if err != nil || receipt.Outcome != stagestore.OutcomeAlreadySatisfied || receipt.Recovery != stagestore.RecoveryForbidden || writes.Load() != 0 || ctx.Err() != context.Canceled {
		t.Fatalf("receipt=%+v err=%v writes=%d context=%v", receipt, err, writes.Load(), ctx.Err())
	}
}

func TestApplyResolveDMPreservesUnknownWhenJournalCannotRecordDispatchOutcome(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.RequestURI() {
		case "/api/v4/users/me":
			_, _ = io.WriteString(response, `{"id":"self","username":"arda"}`)
		case "/api/v4/users/self/channels":
			_, _ = io.WriteString(response, `[]`)
		case "/api/v4/channels/direct":
			response.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(response, `{}`)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	store := openApplyStore(t)
	stage := createResolveStage(t, store, server.URL+"/api/v4", stagestore.ResolveDM, "dm", []string{"peer"})
	faults := &faultStore{Store: store, unknownErr: errors.New("disk unavailable")}
	service, client := applyServiceWithStore(t, server.URL, faults)
	defer client.Close()
	_, err := service.Apply(context.Background(), applyClaim(stage, "apply-unknown-fault"))
	var unknown *api.OutcomeUnknownError
	detail, showErr := store.Show(context.Background(), stage.Stage.ID)
	if !errors.As(err, &unknown) || !errors.Is(err, ErrJournal) || showErr != nil || detail.Lifecycle != stagestore.LifecycleApplying || detail.Recovery != stagestore.RecoveryNone {
		t.Fatalf("detail=%+v applyErr=%v showErr=%v", detail.StageSummary, err, showErr)
	}
}

func TestApplyResolveDMPreservesUnknownWhenFinalizationFailsAfterUnvalidatedSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.RequestURI() {
		case "/api/v4/users/me":
			_, _ = io.WriteString(response, `{"id":"self","username":"arda"}`)
		case "/api/v4/users/self/channels":
			_, _ = io.WriteString(response, `[]`)
		case "/api/v4/channels/direct":
			response.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(response, `{"id":"dm-1","create_at":100,"update_at":100,"delete_at":0,"team_id":"","type":"D","name":"peer__self","display_name":""}`)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	store := openApplyStore(t)
	stage := createResolveStage(t, store, server.URL+"/api/v4", stagestore.ResolveDM, "dm", []string{"peer"})
	faults := &faultStore{Store: store, finalizeErr: errors.New("disk unavailable")}
	service, client := applyServiceWithStore(t, server.URL, faults)
	defer client.Close()
	_, err := service.Apply(context.Background(), applyClaim(stage, "apply-finalize-fault"))
	var unknown *api.OutcomeUnknownError
	if !errors.As(err, &unknown) || !errors.Is(err, ErrJournal) {
		t.Fatalf("error=%v", err)
	}
}

func TestApplyResolveDMReportsConfirmedEffectWhenValidatedResultCannotBeJournaled(t *testing.T) {
	var created atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.RequestURI() {
		case "/api/v4/users/me":
			_, _ = io.WriteString(response, `{"id":"self","username":"arda"}`)
		case "/api/v4/users/self/channels":
			if !created.Load() {
				_, _ = io.WriteString(response, `[]`)
				return
			}
			_, _ = io.WriteString(response, `[{"id":"dm-1","team_id":"","type":"D","name":"peer__self","display_name":""}]`)
		case "/api/v4/channels/dm-1/members?page=0&per_page=9":
			_, _ = io.WriteString(response, `[{"channel_id":"dm-1","user_id":"peer"},{"channel_id":"dm-1","user_id":"self"}]`)
		case "/api/v4/channels/direct":
			created.Store(true)
			response.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(response, `{"id":"dm-1","create_at":100,"update_at":100,"delete_at":0,"team_id":"","type":"D","name":"peer__self","display_name":""}`)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	store := openApplyStore(t)
	stage := createResolveStage(t, store, server.URL+"/api/v4", stagestore.ResolveDM, "dm", []string{"peer"})
	faults := &faultStore{Store: store, validatedErr: errors.New("disk unavailable")}
	service, client := applyServiceWithStore(t, server.URL, faults)
	defer client.Close()
	_, err := service.Apply(context.Background(), applyClaim(stage, "apply-validated-fault"))
	var confirmed *ConfirmedEffectError
	if !errors.As(err, &confirmed) {
		t.Fatalf("error=%v", err)
	}
}

func openApplyStore(t *testing.T) *stagestore.Store {
	t.Helper()
	store, err := stagestore.Open(context.Background(), filepath.Join(t.TempDir(), "state", "mattermost-cli", stagestore.DatabaseFilename))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func createResolveStage(t *testing.T, store *stagestore.Store, serverURL string, operation stagestore.Operation, channelType string, peers []string) stagestore.MutationResult {
	t.Helper()
	destination := []byte(`{"kind":"conversation","channelId":null,"channelType":"` + channelType + `","teamId":null,"postId":null,"rootPostId":null,"participantIds":["` + strings.Join(peers, `","`) + `"],"emoji":null,"postState":null,"reactionPresent":null}`)
	plan := []byte(`{"steps":[{"ordinal":1,"type":"resolve_conversation","condition":"if_missing"}]}`)
	result, err := store.Create(context.Background(), stagestore.CreateInput{RequestDigest: sha256.Sum256([]byte("stage")), Operation: operation, ServerURL: serverURL, UserID: "self", Content: stagestore.RevisionContent{Destination: destination, Plan: plan}})
	if err != nil {
		t.Fatal(err)
	}
	return result.MutationResult
}

func applyClaim(stage stagestore.MutationResult, requestID string) stagestore.ApplyClaimInput {
	return stagestore.ApplyClaimInput{StageID: stage.Stage.ID, RequestID: requestID, Revision: stage.Stage.Revision, ExpectedDigest: stage.Stage.SemanticDigest, RequestDigest: sha256.Sum256([]byte(requestID)), RecoveryMode: stagestore.RecoveryModeOrdinary}
}

func applyService(t *testing.T, serverURL string, store *stagestore.Store) (*Service, *api.Client) {
	return applyServiceWithStore(t, serverURL, store)
}

func applyServiceWithStore(t *testing.T, serverURL string, store Store) (*Service, *api.Client) {
	t.Helper()
	client, err := api.New(serverURL, "token")
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(serverURL+"/api/v4", "", store, mattermost.NewUsers(client), mattermost.NewChannels(client), mattermost.NewConversationMutations(client))
	if err != nil {
		client.Close()
		t.Fatal(err)
	}
	return service, client
}

type faultStore struct {
	*stagestore.Store
	skipErr, unknownErr, validatedErr, finalizeErr error
	beforeSkip                                     func()
}

func (s *faultStore) MarkStepSkipped(ctx context.Context, attemptID string, ordinal int, result json.RawMessage) error {
	if s.beforeSkip != nil {
		s.beforeSkip()
	}
	if s.skipErr != nil {
		return s.skipErr
	}
	return s.Store.MarkStepSkipped(ctx, attemptID, ordinal, result)
}

func (s *faultStore) MarkStepUnknown(ctx context.Context, attemptID string, ordinal int) error {
	if s.unknownErr != nil {
		return s.unknownErr
	}
	return s.Store.MarkStepUnknown(ctx, attemptID, ordinal)
}

func (s *faultStore) MarkStepValidated(ctx context.Context, attemptID string, ordinal int, result json.RawMessage) error {
	if s.validatedErr != nil {
		return s.validatedErr
	}
	return s.Store.MarkStepValidated(ctx, attemptID, ordinal, result)
}

func (s *faultStore) FinalizeApply(ctx context.Context, attemptID string) (stagestore.ApplyReceipt, error) {
	if s.finalizeErr != nil {
		return stagestore.ApplyReceipt{}, s.finalizeErr
	}
	return s.Store.FinalizeApply(ctx, attemptID)
}

func existingDirectServer(t *testing.T, writes *atomic.Int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.RequestURI() {
		case "/api/v4/users/me":
			_, _ = io.WriteString(response, `{"id":"self","username":"arda"}`)
		case "/api/v4/users/self/channels":
			_, _ = io.WriteString(response, `[{"id":"dm-1","team_id":"","type":"D","name":"peer__self","display_name":""}]`)
		case "/api/v4/channels/dm-1/members?page=0&per_page=9":
			_, _ = io.WriteString(response, `[{"channel_id":"dm-1","user_id":"self"},{"channel_id":"dm-1","user_id":"peer"}]`)
		case "/api/v4/channels/direct":
			writes.Add(1)
			response.WriteHeader(http.StatusInternalServerError)
		default:
			http.NotFound(response, request)
		}
	}))
}
