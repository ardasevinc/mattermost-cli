package staging

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ardasevinc/mattermost-cli/internal/mattermost"
	"github.com/ardasevinc/mattermost-cli/internal/schema"
	"github.com/ardasevinc/mattermost-cli/internal/stagestore"
)

type fakeUsers struct {
	current      mattermost.User
	peer         mattermost.User
	err          error
	currentCalls atomic.Int64
}

func (f *fakeUsers) Current(context.Context) (mattermost.User, error) {
	f.currentCalls.Add(1)
	return f.current, f.err
}
func (f *fakeUsers) ByUsernameFresh(context.Context, string) (mattermost.User, error) {
	return f.peer, f.err
}

type fakeChannels struct {
	direct    mattermost.Channel
	found     bool
	byID      mattermost.Channel
	byName    mattermost.Channel
	err       error
	memberErr error
}

func (f *fakeChannels) ExistingDirect(context.Context, string, string) (mattermost.Channel, bool, error) {
	return f.direct, f.found, f.err
}
func (f *fakeChannels) ByID(context.Context, string) (mattermost.Channel, error) {
	return f.byID, f.err
}
func (f *fakeChannels) ByName(context.Context, string, string) (mattermost.Channel, error) {
	return f.byName, f.err
}
func (f *fakeChannels) Member(_ context.Context, channelID, userID string) (mattermost.ChannelMember, error) {
	return mattermost.ChannelMember{ChannelID: channelID, UserID: userID}, f.memberErr
}

type emptyTeams struct{}

func (emptyTeams) List(context.Context, string) (mattermost.TeamMembership, error) {
	return mattermost.TeamMembership{}, nil
}

type emptyPosts struct{}

func (emptyPosts) ByID(context.Context, string) (mattermost.Post, error) {
	return mattermost.Post{}, errors.New("unused")
}
func (emptyPosts) ReactionState(context.Context, string, string, string, string) (bool, error) {
	return false, errors.New("unused")
}

type teamTransport struct{ payload string }

func (t teamTransport) Get(_ context.Context, _ string, out any) error {
	return json.Unmarshal([]byte(t.payload), out)
}

type recordingStore struct {
	mu    sync.Mutex
	calls int
	in    stagestore.CreateInput
	err   error
}

func (s *recordingStore) FindCreate(context.Context, string, string, string) (stagestore.CreateRecord, bool, error) {
	return stagestore.CreateRecord{}, false, nil
}
func (s *recordingStore) Create(_ context.Context, in stagestore.CreateInput) (stagestore.CreateRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.in = in
	result := stagestore.MutationResult{Stage: stagestore.StageSummary{Operation: in.Operation, ServerURL: in.ServerURL, ServerID: in.ServerID, UserID: in.UserID}}
	return stagestore.CreateRecord{MutationResult: result, Result: result, RequestDigest: in.RequestDigest, Destination: in.Content.Destination, Plan: in.Content.Plan}, s.err
}

func dmService(t *testing.T, store Store) (*Service, *fakeUsers, *fakeChannels) {
	return dmServiceCredentials(t, store, nil)
}

func dmServiceCredentials(t *testing.T, store Store, credentials []string) (*Service, *fakeUsers, *fakeChannels) {
	t.Helper()
	u := &fakeUsers{current: mattermost.User{ID: "user-1", Username: "arda"}, peer: mattermost.User{ID: "peer", Username: "hakan"}}
	c := &fakeChannels{direct: mattermost.Channel{ID: "dm-1", Type: "D", Name: "user-1__peer"}, found: true}
	s, err := New("https://Mattermost.Example/chat/", "", credentials, u, c, emptyTeams{}, emptyPosts{}, store)
	if err != nil {
		t.Fatal(err)
	}
	return s, u, c
}

func dmTarget() Target { return Target{Conversation: Direct, Selector: ByUsername, Value: "hakan"} }

func TestCreatePostPersistsOneCanonicalExactStage(t *testing.T) {
	store := &recordingStore{}
	s, _, _ := dmService(t, store)
	body := []byte("hello\n")
	result, err := s.CreatePost(context.Background(), CreatePostInput{RequestID: "req-1", Target: dmTarget(), Body: bytes.NewReader(body)})
	if err != nil {
		t.Fatal(err)
	}
	if store.calls != 1 {
		t.Fatalf("Create calls = %d", store.calls)
	}
	if !bytes.Equal(store.in.Content.Body, body) {
		t.Fatalf("body = %q", store.in.Content.Body)
	}
	if store.in.ServerURL != "https://mattermost.example/chat/api/v4" || store.in.ServerID != "" || store.in.UserID != "user-1" {
		t.Fatalf("binding = %#v", store.in)
	}
	const exactDestination = `{"kind":"conversation","channelId":"dm-1","channelType":"dm","teamId":null,"postId":null,"rootPostId":null,"participantIds":["peer"],"emoji":null,"postState":null,"reactionPresent":null}`
	if string(store.in.Content.Destination) != exactDestination {
		t.Fatalf("destination = %s", store.in.Content.Destination)
	}
	if result.Preview.Destination.ChannelID != "dm-1" {
		t.Fatalf("preview = %#v", result.Preview)
	}
}

func TestAttachmentPlanIsExplicitAndOrdered(t *testing.T) {
	store := &recordingStore{}
	s, _, _ := dmService(t, store)
	digest := [32]byte{1}
	s = s.WithAttachmentBinder(func(context.Context, []Attachment, [][]byte) ([]stagestore.Attachment, error) {
		return []stagestore.Attachment{
			{SuppliedPath: "a", CanonicalPath: "/a", RemoteFilename: "a", ByteLength: 1, ContentDigest: digest},
			{SuppliedPath: "b", CanonicalPath: "/b", RemoteFilename: "b", ByteLength: 1, ContentDigest: digest},
		}, nil
	})
	result, err := s.CreatePost(context.Background(), CreatePostInput{RequestID: "request-plan", Target: dmTarget(), Body: bytes.NewReader([]byte("hello")), Attachments: []Attachment{{Path: "a"}, {Path: "b"}}})
	if err != nil {
		t.Fatal(err)
	}
	const exactPlan = `{"steps":[{"ordinal":1,"type":"upload_attachment","condition":"always"},{"ordinal":2,"type":"upload_attachment","condition":"always"},{"ordinal":3,"type":"create_post","condition":"always"}]}`
	if string(store.in.Content.Plan) != exactPlan {
		t.Fatalf("plan = %s", store.in.Content.Plan)
	}
	got, _ := json.Marshal(result.Preview.Plan)
	if string(got) != exactPlan {
		t.Fatalf("preview plan = %s", got)
	}
}

func TestCredentialTargetRejectedBeforeRemoteAndSnapshotIsImmutable(t *testing.T) {
	credentials := []string{"target-secret"}
	store := &recordingStore{}
	s, users, _ := dmServiceCredentials(t, store, credentials)
	credentials[0] = "changed-after-construction"
	_, err := s.DryRunCreatePost(context.Background(), DryRunInput{Target{Conversation: Direct, Selector: ByUsername, Value: "target-secret"}})
	if !errors.Is(err, ErrCredential) || users.currentCalls.Load() != 0 {
		t.Fatalf("error/current calls = %v/%d", err, users.currentCalls.Load())
	}
	_, err = s.CreatePost(context.Background(), CreatePostInput{RequestID: "request", Target: dmTarget(), Body: bytes.NewReader([]byte("target-secret"))})
	if !errors.Is(err, ErrCredential) || store.calls != 0 {
		t.Fatalf("immutable credential error/calls = %v/%d", err, store.calls)
	}
}

func TestMalformedTargetSyntaxIsZeroNetwork(t *testing.T) {
	for _, target := range []Target{
		{Conversation: Direct, Selector: ByID, Value: "peer"},
		{Conversation: Group, Selector: ByID, Value: "group", Team: &TeamSelector{By: ByID, Value: "team"}},
		{Conversation: Channel, Selector: ByName, Value: "channel"},
		{Conversation: Channel, Selector: ByName, Value: "channel", Team: &TeamSelector{By: ByUsername, Value: "team"}},
		{Conversation: Channel, Selector: ByID, Value: "bad internal space"},
		{Conversation: Channel, Selector: ByID, Value: "bad\u061c"},
		{Conversation: Channel, Selector: ByID, Value: "bad\u200e"},
		{Conversation: Channel, Selector: ByID, Value: "bad\u200f"},
		{Conversation: Channel, Selector: ByID, Value: "bad\u200b"},
		{Conversation: Channel, Selector: ByID, Value: "bad\u200c"},
		{Conversation: Channel, Selector: ByID, Value: "bad\u200d"},
		{Conversation: Channel, Selector: ByID, Value: "bad\ufeff"},
		{Conversation: Channel, Selector: ByID, Value: "bad\u00a0space"},
	} {
		store := &recordingStore{}
		s, users, _ := dmService(t, store)
		_, err := s.DryRunCreatePost(context.Background(), DryRunInput{Target: target})
		if !errors.Is(err, ErrInvalid) || users.currentCalls.Load() != 0 || store.calls != 0 {
			t.Fatalf("target/error/remote/store calls = %#v/%v/%d/%d", target, err, users.currentCalls.Load(), store.calls)
		}
	}
}

func TestConstructorAndResolvedIdentityValidation(t *testing.T) {
	users := &fakeUsers{current: mattermost.User{ID: "user-1", Username: "arda"}}
	channels := &fakeChannels{}
	if _, err := New("https://mattermost.example", " bad ", nil, users, channels, emptyTeams{}, emptyPosts{}, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("server ID error = %v", err)
	}
	for _, unsafe := range []string{"\u200b", "\u200c", "\u200d", "\ufeff"} {
		store := &recordingStore{}
		s, resolvedUsers, _ := dmService(t, store)
		resolvedUsers.current.ID = "bad" + unsafe + "identity"
		reader := &panicReader{}
		_, err := s.CreatePost(context.Background(), CreatePostInput{RequestID: "request", Target: dmTarget(), Body: reader})
		if !errors.Is(err, ErrTarget) || reader.read || store.calls != 0 {
			t.Fatalf("rune/error/read/calls = %q/%v/%v/%d", unsafe, err, reader.read, store.calls)
		}
	}
}

func TestConstructorRejectsTypedNilDependencies(t *testing.T) {
	u := &fakeUsers{}
	c := &fakeChannels{}
	p := &fakePosts{calls: &[]string{}}
	for name, dependencies := range map[string][]any{
		"users":    {(*fakeUsers)(nil), c, emptyTeams{}, p},
		"channels": {u, (*fakeChannels)(nil), emptyTeams{}, p},
		"teams":    {u, c, (*emptyTeams)(nil), p},
		"posts":    {u, c, emptyTeams{}, (*fakePosts)(nil)},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := New("https://mattermost.example", "", nil, dependencies[0].(Users), dependencies[1].(Channels), dependencies[2].(Teams), dependencies[3].(Posts), nil)
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestBoundAttachmentRejectsAdditionalBidiControls(t *testing.T) {
	for _, unsafe := range []string{"\u061c", "\u200e", "\u200f"} {
		store := &recordingStore{}
		s, _, _ := dmService(t, store)
		digest := [32]byte{1}
		s = s.WithAttachmentBinder(func(context.Context, []Attachment, [][]byte) ([]stagestore.Attachment, error) {
			return []stagestore.Attachment{{SuppliedPath: "safe", CanonicalPath: "/safe", RemoteFilename: "bad" + unsafe, ByteLength: 1, ContentDigest: digest}}, nil
		})
		_, err := s.CreatePost(context.Background(), CreatePostInput{RequestID: "request", Target: dmTarget(), Body: bytes.NewReader([]byte("hello")), Attachments: []Attachment{{Path: "safe"}}})
		if !errors.Is(err, ErrInput) || store.calls != 0 {
			t.Fatalf("rune/error/calls = %q/%v/%d", unsafe, err, store.calls)
		}
	}
}

func TestCreateRequiresRequestIDAndMapsConflict(t *testing.T) {
	store := &recordingStore{}
	s, _, _ := dmService(t, store)
	_, err := s.CreatePost(context.Background(), CreatePostInput{Target: dmTarget(), Body: bytes.NewReader([]byte("hello"))})
	if !errors.Is(err, ErrInvalid) || store.calls != 0 {
		t.Fatalf("empty request error/calls = %v/%d", err, store.calls)
	}
	store.err = stagestore.ErrConflict
	_, err = s.CreatePost(context.Background(), CreatePostInput{RequestID: "request", Target: dmTarget(), Body: bytes.NewReader([]byte("hello"))})
	if !errors.Is(err, ErrConflict) || errors.Is(err, ErrStore) {
		t.Fatalf("conflict error = %v", err)
	}
}

func TestRealStoreRequestReplayAndConflict(t *testing.T) {
	dir, err := os.MkdirTemp(".", ".staging-store-test-")
	if err != nil {
		t.Fatal(err)
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	store, err := stagestore.Open(context.Background(), filepath.Join(dir, stagestore.DatabaseFilename))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	s, _, channels := dmService(t, store)
	input := func(body string) CreatePostInput {
		return CreatePostInput{RequestID: "same-request", Target: dmTarget(), Body: bytes.NewReader([]byte(body))}
	}
	first, err := s.CreatePost(context.Background(), input("hello"))
	if err != nil || first.Stored.Replay {
		t.Fatalf("first/error = %#v/%v", first.Stored, err)
	}
	channels.err = errors.New("remote target disappeared")
	replay, err := s.CreatePost(context.Background(), input("hello"))
	if err != nil || !replay.Stored.Replay || replay.Stored.Stage.ID != first.Stored.Stage.ID {
		t.Fatalf("replay/error = %#v/%v", replay.Stored, err)
	}
	if _, err = s.CreatePost(context.Background(), input("different")); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflict = %v", err)
	}
}

func TestCreateReplayRejectsCredentialInRawAttachmentMetadataBeforeOpen(t *testing.T) {
	const token = "active-attachment-token"
	store := realStageStore(t)
	s, users, _ := dmServiceCredentials(t, store, []string{token})
	binderCalls := 0
	s = s.WithAttachmentBinder(func(context.Context, []Attachment, [][]byte) ([]stagestore.Attachment, error) {
		binderCalls++
		return []stagestore.Attachment{{
			SuppliedPath:   "safe.txt",
			CanonicalPath:  "/tmp/safe.txt",
			RemoteFilename: "safe.txt",
			ByteLength:     1,
			MediaType:      "text/plain",
			ContentDigest:  sha256.Sum256([]byte("x")),
		}}, nil
	})
	first := CreatePostInput{
		RequestID:   "raw-attachment-credential",
		Target:      dmTarget(),
		Body:        bytes.NewBufferString("hello"),
		Attachments: []Attachment{{Path: "safe.txt"}},
	}
	if _, err := s.CreatePost(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	currentCalls := users.currentCalls.Load()
	replay := first
	replay.Body = bytes.NewBufferString("hello")
	replay.Attachments = []Attachment{{Path: token + "/../safe.txt"}}
	if _, err := s.CreatePost(context.Background(), replay); !errors.Is(err, ErrCredential) {
		t.Fatalf("replay error = %v", err)
	}
	if binderCalls != 1 || users.currentCalls.Load() != currentCalls {
		t.Fatalf("binder/current calls = %d/%d, want 1/%d", binderCalls, users.currentCalls.Load(), currentCalls)
	}
}

type racingChannels struct{ sequence atomic.Int64 }

func (*racingChannels) ExistingDirect(context.Context, string, string) (mattermost.Channel, bool, error) {
	return mattermost.Channel{}, false, errors.New("unused")
}
func (*racingChannels) ByID(context.Context, string) (mattermost.Channel, error) {
	return mattermost.Channel{}, errors.New("unused")
}
func (r *racingChannels) ByName(context.Context, string, string) (mattermost.Channel, error) {
	suffix := "a"
	if r.sequence.Add(1)%2 == 0 {
		suffix = "b"
	}
	return mattermost.Channel{ID: "channel-" + suffix, TeamID: "team-1", Type: "O", Name: "town"}, nil
}
func (*racingChannels) Member(_ context.Context, channelID, userID string) (mattermost.ChannelMember, error) {
	return mattermost.ChannelMember{ChannelID: channelID, UserID: userID}, nil
}

func TestConcurrentIdenticalServiceRequestsReturnWinnerProjection(t *testing.T) {
	store := realStageStore(t)
	c := &racingChannels{}
	u := &fakeUsers{current: mattermost.User{ID: "user-1", Username: "arda"}}
	teams := mattermost.NewTeams(teamTransport{payload: `[{"id":"team-1","name":"team","display_name":"Team","type":"O"}]`})
	service, err := New("https://mattermost.example", "", nil, u, c, teams, emptyPosts{}, store)
	if err != nil {
		t.Fatal(err)
	}
	target := Target{Conversation: Channel, Selector: ByName, Value: "town", Team: &TeamSelector{By: ByID, Value: "team-1"}}
	const workers = 20
	results := make(chan CreatePostResult, workers)
	failures := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := service.CreatePost(context.Background(), CreatePostInput{RequestID: "racing-request", Target: target, Body: bytes.NewBufferString("body")})
			if err != nil {
				failures <- err
				return
			}
			results <- result
		}()
	}
	wg.Wait()
	close(results)
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	var id, channel string
	firsts, count := 0, 0
	for result := range results {
		count++
		if !result.Stored.Replay {
			firsts++
		}
		if id == "" {
			id, channel = result.Stored.Stage.ID, result.Preview.Destination.ChannelID
		}
		if result.Stored.Stage.ID != id || result.Preview.Destination.ChannelID != channel {
			t.Fatalf("result diverged = %#v", result)
		}
	}
	if count != workers || firsts != 1 {
		t.Fatalf("count/firsts = %d/%d", count, firsts)
	}
}

type panicReader struct{ read bool }

func (r *panicReader) Read([]byte) (int, error) { r.read = true; return 0, errors.New("forbidden") }

func TestDryRunStopsAfterResolution(t *testing.T) {
	store := &recordingStore{}
	s, _, _ := dmService(t, store)
	binderCalls := 0
	s = s.WithAttachmentBinder(func(context.Context, []Attachment, [][]byte) ([]stagestore.Attachment, error) {
		binderCalls++
		return nil, nil
	})
	preview, err := s.DryRunCreatePost(context.Background(), DryRunInput{Target: dmTarget()})
	if err != nil || preview.Destination.ChannelID != "dm-1" {
		t.Fatalf("preview/error = %#v/%v", preview, err)
	}
	if store.calls != 0 || binderCalls != 0 {
		t.Fatalf("store/binder calls = %d/%d", store.calls, binderCalls)
	}
}

func TestDryRunAndPersistResolveIdenticalDestination(t *testing.T) {
	store := &recordingStore{}
	s, _, _ := dmService(t, store)
	dry, err := s.DryRunCreatePost(context.Background(), DryRunInput{Target: dmTarget()})
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := s.CreatePost(context.Background(), CreatePostInput{RequestID: "parity-request", Target: dmTarget(), Body: bytes.NewReader([]byte("hello"))})
	if err != nil {
		t.Fatal(err)
	}
	dryDestination, _ := json.Marshal(dry.Destination)
	persistedDestination, _ := json.Marshal(persisted.Preview.Destination)
	if !bytes.Equal(dryDestination, persistedDestination) {
		t.Fatalf("dry/persist destinations = %s/%s", dryDestination, persistedDestination)
	}
}

func TestCreatePostPreviewDestinationAndPlanMatchPublicSchema(t *testing.T) {
	store := &recordingStore{}
	service, _, _ := dmService(t, store)
	result, err := service.CreatePost(context.Background(), CreatePostInput{
		RequestID: "schema-1", Target: dmTarget(), Body: bytes.NewReader([]byte("hello")),
	})
	if err != nil {
		t.Fatal(err)
	}
	document := struct {
		Schema           string      `json:"schema"`
		Persist          bool        `json:"persist"`
		Operation        string      `json:"operation"`
		Binding          any         `json:"binding"`
		Destination      Destination `json:"destination"`
		Plan             Plan        `json:"plan"`
		ContentValidated bool        `json:"contentValidated"`
	}{
		Schema: "mm/v2/stage-preview", Persist: false, Operation: "create_post",
		Binding: struct {
			ServerURL string  `json:"serverUrl"`
			ServerID  *string `json:"serverId"`
			UserID    string  `json:"userId"`
		}{ServerURL: result.Preview.ServerURL, UserID: result.Preview.UserID},
		Destination: result.Preview.Destination, Plan: result.Preview.Plan,
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := schema.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Validate("mm/v2/stage-preview", bytes.NewReader(encoded)); err != nil {
		t.Fatalf("producer preview rejected: %v\n%s", err, encoded)
	}
}

func TestDMResolutionFailsClosed(t *testing.T) {
	for _, test := range []struct {
		name  string
		alter func(*fakeUsers, *fakeChannels)
	}{
		{"none", func(_ *fakeUsers, c *fakeChannels) { c.found = false }},
		{"duplicate", func(_ *fakeUsers, c *fakeChannels) { c.err = mattermost.ErrInvalidChannelsResponse }},
		{"self", func(u *fakeUsers, _ *fakeChannels) { u.peer.ID = u.current.ID }},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &recordingStore{}
			s, u, c := dmService(t, store)
			test.alter(u, c)
			_, err := s.DryRunCreatePost(context.Background(), DryRunInput{dmTarget()})
			if !errors.Is(err, ErrTarget) || store.calls != 0 {
				t.Fatalf("err/calls = %v/%d", err, store.calls)
			}
		})
	}
}

func TestChannelAndGroupRequireTypeAndMembership(t *testing.T) {
	store := &recordingStore{}
	s, _, channels := dmService(t, store)
	channels.byID = mattermost.Channel{ID: "g", Type: "O", TeamID: "t", Name: "x"}
	_, err := s.DryRunCreatePost(context.Background(), DryRunInput{Target{Conversation: Group, Selector: ByID, Value: "g"}})
	if !errors.Is(err, ErrTarget) {
		t.Fatalf("wrong group type error = %v", err)
	}
	channels.byID = mattermost.Channel{ID: "c", Type: "P", TeamID: "t", Name: "x"}
	channels.memberErr = errors.New("not a member")
	_, err = s.DryRunCreatePost(context.Background(), DryRunInput{Target{Conversation: Channel, Selector: ByID, Value: "c"}})
	if !errors.Is(err, ErrTarget) {
		t.Fatalf("membership error = %v", err)
	}
}

func TestNameChannelRequiresUniqueExactTeam(t *testing.T) {
	store := &recordingStore{}
	s, _, channels := dmService(t, store)
	channels.byName = mattermost.Channel{ID: "c", Type: "O", TeamID: "t1", Name: "town-square"}
	s.teams = mattermost.NewTeams(teamTransport{payload: `[{"id":"t1","name":"alpha","display_name":"Alpha","type":"O"}]`})
	if membership, listErr := s.teams.List(context.Background(), "user-1"); listErr != nil || len(membership.Items()) != 1 {
		t.Fatalf("team fixture = %#v/%v", membership.Items(), listErr)
	}
	preview, err := s.DryRunCreatePost(context.Background(), DryRunInput{Target{Conversation: Channel, Selector: ByName, Value: "town-square", Team: &TeamSelector{By: ByName, Value: "alpha"}}})
	if err != nil || preview.Destination.TeamID == nil || *preview.Destination.TeamID != "t1" {
		t.Fatalf("preview/error = %#v/%v", preview, err)
	}
	s.teams = mattermost.NewTeams(teamTransport{payload: `[{"id":"t1","name":"alpha","display_name":"Same","type":"O"},{"id":"t2","name":"beta","display_name":"Same","type":"O"}]`})
	_, err = s.DryRunCreatePost(context.Background(), DryRunInput{Target{Conversation: Channel, Selector: ByName, Value: "town-square", Team: &TeamSelector{By: ByName, Value: "Same"}}})
	if !errors.Is(err, ErrTarget) {
		t.Fatalf("ambiguous team error = %v", err)
	}
}

func TestBodyByteLimitFailsBeforePersistence(t *testing.T) {
	store := &recordingStore{}
	s, _, _ := dmService(t, store)
	_, err := s.CreatePost(context.Background(), CreatePostInput{RequestID: "r", Target: dmTarget(), Body: bytes.NewReader(bytes.Repeat([]byte("x"), 65_536))})
	if !errors.Is(err, ErrInput) || store.calls != 0 {
		t.Fatalf("error/calls = %v/%d", err, store.calls)
	}
}

func TestBodyValidationAndCredentialFailuresNeverPersist(t *testing.T) {
	const token = "active-secret-credential"
	for _, test := range []struct {
		name    string
		request string
		body    []byte
		target  Target
	}{
		{"empty", "r", []byte(" \n"), dmTarget()},
		{"invalid utf8", "r", []byte{0xff}, dmTarget()},
		{"token body", "r", []byte("hello " + token), dmTarget()},
		{"token request", "r-" + token, []byte("hello"), dmTarget()},
		{"token target", "r", []byte("hello"), Target{Conversation: Direct, Selector: ByUsername, Value: token}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &recordingStore{}
			s, _, _ := dmServiceCredentials(t, store, []string{token})
			_, err := s.CreatePost(context.Background(), CreatePostInput{RequestID: test.request, Target: test.target, Body: bytes.NewReader(test.body)})
			if err == nil || store.calls != 0 || bytes.Contains([]byte(err.Error()), []byte(token)) {
				t.Fatalf("err/calls = %v/%d", err, store.calls)
			}
		})
	}
}

func TestAttachmentCredentialAcrossScanBoundaryNeverPersists(t *testing.T) {
	const token = "boundary-credential"
	dir, err := os.MkdirTemp(".", ".staging-test-")
	if err != nil {
		t.Fatal(err)
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "safe.txt")
	data := append(bytes.Repeat([]byte("x"), 32*1024-len(token)/2), []byte(token)...)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	store := &recordingStore{}
	s, _, _ := dmServiceCredentials(t, store, []string{token})
	_, err = s.CreatePost(context.Background(), CreatePostInput{RequestID: "r", Target: dmTarget(), Body: bytes.NewReader([]byte("hello")), Attachments: []Attachment{{Path: path}}})
	if !errors.Is(err, ErrCredential) || store.calls != 0 {
		t.Fatalf("err/calls = %v/%d", err, store.calls)
	}
}

func TestStoreCalledExactlyOnceUnderConcurrentRequests(t *testing.T) {
	store := &recordingStore{}
	s, _, _ := dmService(t, store)
	const n = 12
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _ = s.CreatePost(context.Background(), CreatePostInput{RequestID: fmt.Sprintf("request-%d", i), Target: dmTarget(), Body: bytes.NewReader([]byte("hello"))})
		}(i)
	}
	wg.Wait()
	if store.calls != n {
		t.Fatalf("Create calls = %d", store.calls)
	}
}

func FuzzDryRunInvalidTargetsNeverPersist(f *testing.F) {
	f.Add("", uint8(0), uint8(0))
	f.Add(" x ", uint8(1), uint8(1))
	f.Fuzz(func(t *testing.T, value string, conversation, selector uint8) {
		store := &recordingStore{}
		s, _, _ := dmService(t, store)
		_, _ = s.DryRunCreatePost(context.Background(), DryRunInput{Target{Conversation: ConversationType(conversation), Selector: SelectorType(selector), Value: value}})
		if store.calls != 0 {
			t.Fatalf("dry-run persisted")
		}
	})
}

var _ io.Reader = (*panicReader)(nil)
