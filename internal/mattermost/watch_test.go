package mattermost

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ardasevinc/mattermost-cli/internal/presentation"
	"github.com/ardasevinc/mattermost-cli/internal/transport"
)

type recordingSink struct {
	mu          sync.Mutex
	posts       []WatchPost
	sequences   []Sequence
	diagnostics []WatchDiagnostic
	err         error
}
type stoppingDiagnosticSink struct{ recordingSink }

func (s *stoppingDiagnosticSink) Diagnostic(value WatchDiagnostic) error {
	_ = s.recordingSink.Diagnostic(value)
	if value.Type == "sequence_gap" {
		return errors.New("stop")
	}
	return nil
}

func (s *recordingSink) Post(post WatchPost, sequence Sequence) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.posts = append(s.posts, post)
	s.sequences = append(s.sequences, sequence)
	return s.err
}
func (s *recordingSink) Diagnostic(value WatchDiagnostic) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.diagnostics = append(s.diagnostics, value)
	return s.err
}

type fakeSocket struct {
	reads  chan readResult
	writes [][]byte
	mu     sync.Mutex
	closed chan struct{}
	once   sync.Once
}
type instantTimer struct{ channel chan time.Time }

func newInstantTimer() WatchTimer {
	channel := make(chan time.Time, 1)
	channel <- time.Time{}
	return instantTimer{channel}
}
func (timer instantTimer) C() <-chan time.Time { return timer.channel }
func (instantTimer) Stop() bool                { return true }

func newFakeSocket() *fakeSocket {
	return &fakeSocket{reads: make(chan readResult, 16), closed: make(chan struct{})}
}
func (s *fakeSocket) Read(ctx context.Context) (transport.MessageType, []byte, error) {
	select {
	case <-ctx.Done():
		return 0, nil, ctx.Err()
	case value := <-s.reads:
		return value.type_, value.data, value.err
	}
}
func (s *fakeSocket) Write(_ context.Context, _ transport.MessageType, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writes = append(s.writes, append([]byte(nil), data...))
	return nil
}
func (s *fakeSocket) Close(context.Context) error { s.once.Do(func() { close(s.closed) }); return nil }
func (s *fakeSocket) CloseNow() error             { s.once.Do(func() { close(s.closed) }); return nil }
func (*fakeSocket) SetReadLimit(int64)            {}

func TestDecodeFrameRejectsMalformedTrailingOversizedNegativeAndScalar(t *testing.T) {
	for _, data := range [][]byte{[]byte("{"), []byte(`{} {}`), []byte(`null`), []byte(`{"seq":-1}`)} {
		if _, err := decodeFrame(data); !errors.Is(err, ErrMalformedWatchFrame) {
			t.Fatalf("%q: %v", data, err)
		}
	}
	if _, err := decodeFrame([]byte(strings.Repeat("x", MaxWatchFrameBytes+1))); !errors.Is(err, ErrOversizedWatchFrame) {
		t.Fatal(err)
	}
	if _, err := decodeFrame([]byte(fmt.Sprintf(`{"seq":%d}`, MaxSafeSequence-1))); err != nil {
		t.Fatal(err)
	}
	if _, err := decodeFrame([]byte(fmt.Sprintf(`{"seq":%d}`, MaxSafeSequence))); !errors.Is(err, ErrMalformedWatchFrame) {
		t.Fatalf("headroom boundary=%v", err)
	}
	if _, err := decodeFrame([]byte(fmt.Sprintf(`{"seq_reply":%d}`, MaxSafeSequence))); err != nil {
		t.Fatal(err)
	}
}
func TestParsePostPresenceTrailerAndTimestamp(t *testing.T) {
	good := wireFrame{Event: "posted", Data: []byte(`{"post":"{\"id\":\"p\",\"channel_id\":\"c\",\"user_id\":\"u\",\"message\":\"m\",\"root_id\":\"\",\"create_at\":1,\"file_ids\":[]}","channel_name":"town","sender_name":"arda"}`)}
	if post, ok := parsePost(good); !ok || post.ID != "p" {
		t.Fatalf("%#v %v", post, ok)
	}
	for _, encoded := range []string{`{}`, `{"id":"p","channel_id":"c","user_id":"u","message":"m","root_id":"","create_at":1e3,"file_ids":[]}`, `{"id":"p","channel_id":"c","user_id":"u","message":"m","root_id":"","create_at":253402300800000,"file_ids":[]}{}`} {
		bad := good
		bad.Data = []byte(`{"post":` + strconvQuote(encoded) + `,"channel_name":"x","sender_name":"y"}`)
		if _, ok := parsePost(bad); ok {
			t.Fatalf("accepted %s", encoded)
		}
	}
}
func strconvQuote(value string) string { return `"` + strings.ReplaceAll(value, `"`, `\"`) + `"` }
func TestWatchAuthWireInterleavingDuplicateSequenceAndCancellation(t *testing.T) {
	socket := newFakeSocket()
	sink := &recordingSink{}
	socket.reads <- readResult{type_: transport.MessageText, data: []byte(`{"event":"hello","seq":0,"data":{"connection_id":"one"}}`)}
	socket.reads <- readResult{type_: transport.MessageText, data: []byte(`{"event":"posted","seq":1,"data":{"post":"{\"id\":\"early\",\"channel_id\":\"c\",\"user_id\":\"u\",\"message\":\"m\",\"root_id\":\"\",\"create_at\":1,\"file_ids\":[]}","channel_name":"town","sender_name":"arda"}}`)}
	socket.reads <- readResult{type_: transport.MessageText, data: []byte(`{"status":"OK","seq_reply":1}`)}
	socket.reads <- readResult{type_: transport.MessageText, data: []byte(`{"event":"posted","seq":2,"data":{"post":"{\"id\":\"p\",\"channel_id\":\"c\",\"user_id\":\"u\",\"message\":\"m\",\"root_id\":\"\",\"create_at\":1,\"file_ids\":[]}","channel_name":"town","sender_name":"arda"}}`)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Watch(ctx, WatchOptions{URL: "https://mm.example.com", Token: "secret", Sink: sink, Dial: func(context.Context, string) (transport.WebSocket, error) { return socket, nil }})
	}()
	time.Sleep(10 * time.Millisecond)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if len(sink.posts) != 1 || sink.posts[0].ID != "p" || sink.sequences[0].Number != 2 {
		t.Fatalf("posts=%#v", sink.posts)
	}
	socket.mu.Lock()
	wire := string(socket.writes[0])
	socket.mu.Unlock()
	if wire != `{"action":"authentication_challenge","data":{"token":"secret"},"seq":1}` {
		t.Fatalf("auth=%s", wire)
	}
}
func TestWatchHelloBeforeAuthSchedulesFullHeartbeatInterval(t *testing.T) {
	socket := newFakeSocket()
	socket.reads <- readResult{type_: transport.MessageText, data: []byte(`{"event":"hello","seq":0,"data":{"connection_id":"one"}}`)}
	socket.reads <- readResult{type_: transport.MessageText, data: []byte(`{"status":"OK","seq_reply":1}`)}
	now := time.Unix(100, 0).UTC()
	durations := make(chan time.Duration, 4)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Watch(ctx, WatchOptions{
			URL: "https://mm.example.com", Token: "secret", Sink: &recordingSink{},
			HandshakeTimeout: 30 * time.Second, HeartbeatInterval: 10 * time.Second,
			Now: func() time.Time { return now },
			NewTimer: func(duration time.Duration) WatchTimer {
				durations <- duration
				return instantTimer{make(chan time.Time)}
			},
			Dial: func(context.Context, string) (transport.WebSocket, error) { return socket, nil },
		})
	}()
	want := []time.Duration{30 * time.Second, 30 * time.Second, 10 * time.Second}
	for i, expected := range want {
		select {
		case got := <-durations:
			if got != expected {
				t.Fatalf("timer %d duration=%s, want %s", i, got, expected)
			}
		case <-time.After(time.Second):
			t.Fatalf("timer %d was not scheduled", i)
		}
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
func TestWatchSinkErrorIsTerminalWithoutReconnect(t *testing.T) {
	socket := newFakeSocket()
	sink := &recordingSink{err: errors.New("hostile")}
	socket.reads <- readResult{type_: transport.MessageBinary, data: []byte("x")}
	dials := 0
	err := Watch(context.Background(), WatchOptions{URL: "https://mm.example.com", Token: "secret", Sink: sink, Dial: func(context.Context, string) (transport.WebSocket, error) { dials++; return socket, nil }})
	if !errors.Is(err, ErrWatchSink) || dials != 1 || strings.Contains(err.Error(), "hostile") {
		t.Fatalf("%v %d", err, dials)
	}
}
func TestWatchAuthenticationFailureIsFatalAndNeverReflected(t *testing.T) {
	socket := newFakeSocket()
	sink := &recordingSink{}
	credential := "secret-token"
	socket.reads <- readResult{type_: transport.MessageText, data: []byte(`{"status":"FAIL","seq_reply":1,"error":{"message":"secret-token invalid token"}}`)}
	dials := 0
	err := Watch(context.Background(), WatchOptions{URL: "https://mm.example.com", Token: credential, Sink: sink, Dial: func(context.Context, string) (transport.WebSocket, error) { dials++; return socket, nil }})
	if !errors.Is(err, ErrWatchAuthentication) || dials != 1 || strings.Contains(err.Error(), credential) {
		t.Fatalf("%v %d", err, dials)
	}
}
func TestWatchValidationPrecedesCredentialRegistrationAndDial(t *testing.T) {
	before := presentation.ActiveCredentials.Values()
	dials := 0
	err := Watch(context.Background(), WatchOptions{URL: "https://not-loopback.invalid", Token: "secret", Sink: &recordingSink{}, HandshakeTimeout: -1, Dial: func(context.Context, string) (transport.WebSocket, error) { dials++; return nil, nil }})
	if !errors.Is(err, ErrInvalidWatchOptions) || dials != 0 || len(presentation.ActiveCredentials.Values()) != len(before) {
		t.Fatalf("%v dials=%d", err, dials)
	}
}
func TestWatchURLResumeAndJitter(t *testing.T) {
	got, err := watchURL("https://mm.example.com/base", "connection", 7)
	if err != nil || got != "wss://mm.example.com/base/api/v4/websocket?connection_id=connection&sequence_number=7" {
		t.Fatalf("%q %v", got, err)
	}
	if jitterBackoff(1, 0) != 800*time.Millisecond || jitterBackoff(99, 1) != 36*time.Second {
		t.Fatal("jitter bounds")
	}
}
func TestWatchForwardAndStaleSequencesAreSuppressed(t *testing.T) {
	for _, sequence := range []int{0, 2} {
		t.Run(strconv.Itoa(sequence), func(t *testing.T) {
			socket := newFakeSocket()
			sink := &stoppingDiagnosticSink{}
			socket.reads <- readResult{type_: transport.MessageText, data: []byte(`{"event":"hello","seq":0,"data":{"connection_id":"one"}}`)}
			socket.reads <- readResult{type_: transport.MessageText, data: []byte(`{"status":"OK","seq_reply":1}`)}
			posted := fmt.Sprintf(`{"event":"posted","seq":%d,"data":{"post":"{\"id\":\"p\",\"channel_id\":\"c\",\"user_id\":\"u\",\"message\":\"m\",\"root_id\":\"\",\"create_at\":1,\"file_ids\":[]}","channel_name":"town","sender_name":"arda"}}`, sequence)
			socket.reads <- readResult{type_: transport.MessageText, data: []byte(posted)}
			err := Watch(context.Background(), WatchOptions{URL: "https://mm.example.com", Token: "secret", Sink: sink, Dial: func(context.Context, string) (transport.WebSocket, error) { return socket, nil }})
			if !errors.Is(err, ErrWatchSink) || len(sink.posts) != 0 || len(sink.diagnostics) != 1 || sink.diagnostics[0].Expected == nil || *sink.diagnostics[0].Expected != 1 {
				t.Fatalf("err=%v posts=%v diagnostics=%#v", err, sink.posts, sink.diagnostics)
			}
		})
	}
}
func TestWatchRetryCapUsesInjectedBackoff(t *testing.T) {
	sink := &recordingSink{}
	dials := 0
	waits := []time.Duration{}
	err := Watch(context.Background(), WatchOptions{URL: "https://mm.example.com", Token: "secret", Sink: sink, MaxReconnects: 2, Random: func() float64 { return .5 }, NewTimer: func(d time.Duration) WatchTimer { waits = append(waits, d); return newInstantTimer() }, Dial: func(context.Context, string) (transport.WebSocket, error) {
		dials++
		return nil, errors.New("hostile remote")
	}})
	if !errors.Is(err, ErrWatchRetries) || dials != 3 || len(waits) != 2 || waits[0] != time.Second || waits[1] != 2*time.Second {
		t.Fatalf("err=%v dials=%d waits=%v", err, dials, waits)
	}
}
func TestRapidAuthenticatedDropsRemainBoundedUntilPongStability(t *testing.T) {
	sink := &recordingSink{}
	dials := 0
	err := Watch(context.Background(), WatchOptions{URL: "https://mm.example.com", Token: "secret", Sink: sink, MaxReconnects: 2, Random: func() float64 { return .5 }, NewTimer: func(duration time.Duration) WatchTimer {
		if duration < 10*time.Second {
			return newInstantTimer()
		}
		return instantTimer{make(chan time.Time)}
	}, Dial: func(context.Context, string) (transport.WebSocket, error) {
		socket := newFakeSocket()
		sequence := dials
		auth := dials + 1
		dials++
		socket.reads <- readResult{type_: transport.MessageText, data: []byte(fmt.Sprintf(`{"event":"hello","seq":%d,"data":{"connection_id":"one"}}`, sequence))}
		socket.reads <- readResult{type_: transport.MessageText, data: []byte(fmt.Sprintf(`{"status":"OK","seq_reply":%d}`, auth))}
		socket.reads <- readResult{err: errors.New("drop")}
		return socket, nil
	}})
	if !errors.Is(err, ErrWatchRetries) || dials != 3 {
		t.Fatalf("err=%v dials=%d", err, dials)
	}
}

func TestActionHeartbeatExactWireWrongReplyAndTimeout(t *testing.T) {
	socket := newFakeSocket()
	sink := &recordingSink{}
	socket.reads <- readResult{type_: transport.MessageText, data: []byte(`{"event":"hello","seq":0,"data":{"connection_id":"one"}}`)}
	socket.reads <- readResult{type_: transport.MessageText, data: []byte(`{"status":"OK","seq_reply":1}`)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Watch(ctx, WatchOptions{URL: "https://mm.example.com", Token: "secret", Sink: sink, HeartbeatInterval: 2 * time.Millisecond, HeartbeatTimeout: 3 * time.Millisecond, Dial: func(context.Context, string) (transport.WebSocket, error) { return socket, nil }})
	}()
	deadline := time.Now().Add(time.Second)
	for {
		socket.mu.Lock()
		count := len(socket.writes)
		socket.mu.Unlock()
		if count >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("ping not written")
		}
		time.Sleep(time.Millisecond)
	}
	socket.mu.Lock()
	ping := string(socket.writes[1])
	socket.mu.Unlock()
	if ping != `{"action":"ping","data":{},"seq":2}` {
		t.Fatalf("ping=%s", ping)
	}
	socket.reads <- readResult{type_: transport.MessageText, data: []byte(`{"status":"OK","seq_reply":999}`)}
	for {
		sink.mu.Lock()
		found := false
		for _, diagnostic := range sink.diagnostics {
			if diagnostic.Type == "disconnected" {
				found = true
			}
		}
		sink.mu.Unlock()
		if found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("wrong pong prevented timeout")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
