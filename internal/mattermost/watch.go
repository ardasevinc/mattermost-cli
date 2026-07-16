package mattermost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ardasevinc/mattermost-cli/internal/presentation"
	"github.com/ardasevinc/mattermost-cli/internal/serverurl"
	"github.com/ardasevinc/mattermost-cli/internal/transport"
)

const MaxWatchFrameBytes = 1 << 20
const MaxSafeSequence int64 = 9007199254740991

var (
	ErrMalformedWatchFrame = errors.New("malformed WebSocket frame")
	ErrOversizedWatchFrame = errors.New("WebSocket frame exceeds 1048576 bytes")
	ErrWatchAuthentication = errors.New("WebSocket authentication failed")
	ErrWatchSink           = errors.New("watch output failed")
	ErrWatchRetries        = errors.New("WebSocket reconnect limit reached")
	ErrInvalidWatchOptions = errors.New("invalid watch options")
)

type WatchPost struct {
	ID, ChannelID, UserID, Message, RootID, ChannelName, SenderName string
	CreateAt                                                        int64
	FileIDs                                                         []string
}
type Sequence struct {
	ConnectionID string
	Number       int64
}
type WatchDiagnostic struct {
	Type, Message, PreviousID, CurrentID string
	Timestamp                            time.Time
	Backfill, Fatal                      bool
	Expected, Received                   *int64
	Attempt                              *int
	Delay                                *time.Duration
}
type WatchSink interface {
	Post(WatchPost, Sequence) error
	Diagnostic(WatchDiagnostic) error
}

type WatchOptions struct {
	URL, Token, ChannelID                                               string
	Sink                                                                WatchSink
	Dial                                                                transport.DialWebSocket
	HandshakeTimeout, HeartbeatInterval, HeartbeatTimeout, CloseTimeout time.Duration
	MaxReconnects                                                       int
	Random                                                              func() float64
	NewTimer                                                            func(time.Duration) WatchTimer
	Now                                                                 func() time.Time
}

type WatchTimer interface {
	C() <-chan time.Time
	Stop() bool
}
type realWatchTimer struct{ timer *time.Timer }

func (timer realWatchTimer) C() <-chan time.Time { return timer.timer.C }
func (timer realWatchTimer) Stop() bool          { return timer.timer.Stop() }

type wireFrame struct {
	Event, Status string
	Data          json.RawMessage
	Seq, Reply    *int64
	Error         json.RawMessage
}

func decodeFrame(data []byte) (wireFrame, error) {
	if len(data) > MaxWatchFrameBytes {
		return wireFrame{}, ErrOversizedWatchFrame
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var raw map[string]json.RawMessage
	if err := decoder.Decode(&raw); err != nil || raw == nil {
		return wireFrame{}, ErrMalformedWatchFrame
	}
	if err := requireEOF(decoder); err != nil {
		return wireFrame{}, ErrMalformedWatchFrame
	}
	var frame wireFrame
	for key, target := range map[string]any{"event": &frame.Event, "status": &frame.Status, "data": &frame.Data, "seq": &frame.Seq, "seq_reply": &frame.Reply, "error": &frame.Error} {
		if value, ok := raw[key]; ok && json.Unmarshal(value, target) != nil {
			return wireFrame{}, ErrMalformedWatchFrame
		}
	}
	if frame.Seq != nil && (*frame.Seq < 0 || *frame.Seq >= MaxSafeSequence) || frame.Reply != nil && (*frame.Reply < 0 || *frame.Reply > MaxSafeSequence) {
		return wireFrame{}, ErrMalformedWatchFrame
	}
	return frame, nil
}

func requireEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}

func parsePost(frame wireFrame) (WatchPost, bool) {
	if frame.Event != "posted" {
		return WatchPost{}, false
	}
	var data map[string]json.RawMessage
	if json.Unmarshal(frame.Data, &data) != nil {
		return WatchPost{}, false
	}
	var encoded, channelName, senderName string
	if json.Unmarshal(data["post"], &encoded) != nil || json.Unmarshal(data["channel_name"], &channelName) != nil || json.Unmarshal(data["sender_name"], &senderName) != nil {
		return WatchPost{}, false
	}
	decoder := json.NewDecoder(strings.NewReader(encoded))
	decoder.UseNumber()
	var raw map[string]json.RawMessage
	if decoder.Decode(&raw) != nil || raw == nil || requireEOF(decoder) != nil {
		return WatchPost{}, false
	}
	var post WatchPost
	fields := map[string]*string{"id": &post.ID, "channel_id": &post.ChannelID, "user_id": &post.UserID, "message": &post.Message, "root_id": &post.RootID}
	for key, target := range fields {
		value, ok := raw[key]
		if !ok || json.Unmarshal(value, target) != nil {
			return WatchPost{}, false
		}
	}
	var timestamp json.Number
	if value, ok := raw["create_at"]; !ok || json.Unmarshal(value, &timestamp) != nil {
		return WatchPost{}, false
	}
	parsed, err := strconv.ParseInt(timestamp.String(), 10, 64)
	if err != nil || parsed < 0 || parsed > 253402300799999 {
		return WatchPost{}, false
	}
	post.CreateAt = parsed
	if value, ok := raw["file_ids"]; !ok || json.Unmarshal(value, &post.FileIDs) != nil || post.FileIDs == nil {
		return WatchPost{}, false
	}
	for _, id := range post.FileIDs {
		if id == "" {
			return WatchPost{}, false
		}
	}
	if post.ID == "" || post.ChannelID == "" || post.UserID == "" {
		return WatchPost{}, false
	}
	post.ChannelName, post.SenderName = channelName, senderName
	return post, true
}

type watchState struct {
	connectionID           string
	nextServer, nextAction int64
	authenticated, hello   bool
}
type readResult struct {
	type_ transport.MessageType
	data  []byte
	err   error
}

func Watch(ctx context.Context, options WatchOptions) error {
	if err := validateWatchOptions(&options); err != nil {
		return err
	}
	release := presentation.ActiveCredentials.Register(options.Token)
	defer release()
	state := watchState{nextAction: 1}
	dedupe := NewPostDeduplicator(1000)
	outages := 0
	for {
		if outages > options.MaxReconnects {
			return ErrWatchRetries
		}
		if outages > 0 {
			random := options.Random()
			if math.IsNaN(random) || random < 0 || random > 1 {
				return ErrInvalidWatchOptions
			}
			delay := jitterBackoff(outages, random)
			attempt := outages
			if err := emitDiagnostic(options, WatchDiagnostic{Type: "reconnect", Message: "WebSocket disconnected; reconnecting without REST backfill.", Attempt: &attempt, Delay: &delay}); err != nil {
				return err
			}
			timer := options.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C():
			}
		}
		stable, err := runConnection(ctx, options, &state, dedupe)
		if errors.Is(err, ErrWatchAuthentication) || errors.Is(err, ErrWatchSink) || ctx.Err() != nil {
			return err
		}
		if stable {
			outages = 1
		} else {
			outages++
		}
		if err := emitDiagnostic(options, WatchDiagnostic{Type: "disconnected", Message: "WebSocket disconnected; live events may be missing.", Fatal: false}); err != nil {
			return err
		}
	}
}

func runConnection(ctx context.Context, options WatchOptions, state *watchState, dedupe *PostDeduplicator) (bool, error) {
	target, err := watchURL(options.URL, state.connectionID, state.nextServer)
	if err != nil {
		return false, err
	}
	dialCtx, cancelDial := context.WithTimeout(ctx, options.HandshakeTimeout)
	conn, err := options.Dial(dialCtx, target)
	cancelDial()
	if err != nil {
		return false, errors.New("WebSocket connection failed")
	}
	conn.SetReadLimit(MaxWatchFrameBytes)
	readCtx, cancelRead := context.WithCancel(ctx)
	reads := make(chan readResult)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			type_, data, err := conn.Read(readCtx)
			select {
			case reads <- readResult{type_, data, err}:
			case <-readCtx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	defer func() {
		cancelRead()
		closeCtx, cancel := context.WithTimeout(context.Background(), options.CloseTimeout)
		_ = conn.Close(closeCtx)
		cancel()
		_ = conn.CloseNow()
		<-readerDone
	}()
	if state.nextAction > MaxSafeSequence {
		return false, errors.New("WebSocket action sequence exhausted")
	}
	authSeq := state.nextAction
	state.nextAction++
	auth, _ := json.Marshal(map[string]any{"seq": authSeq, "action": "authentication_challenge", "data": map[string]string{"token": options.Token}})
	writeCtx, cancelWrite := context.WithTimeout(ctx, options.HandshakeTimeout)
	err = conn.Write(writeCtx, transport.MessageText, auth)
	cancelWrite()
	if err != nil {
		return false, errors.New("WebSocket handshake failed")
	}
	state.authenticated, state.hello = false, false
	handshakeDeadline := options.Now().Add(options.HandshakeTimeout)
	heartbeatAt := time.Time{}
	pongDeadline := time.Time{}
	var pendingPing int64 = -1
	stable := false
	for {
		deadline := handshakeDeadline
		if state.authenticated && state.hello {
			if !pongDeadline.IsZero() {
				deadline = pongDeadline
			} else {
				deadline = heartbeatAt
			}
		}
		wait := deadline.Sub(options.Now())
		if wait < 0 {
			wait = 0
		}
		timer := options.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return stable, ctx.Err()
		case <-timer.C():
			if !state.authenticated || !state.hello {
				return false, errors.New("WebSocket handshake failed")
			}
			if !pongDeadline.IsZero() {
				return stable, errors.New("WebSocket heartbeat failed")
			}
			if state.nextAction > MaxSafeSequence {
				return stable, errors.New("WebSocket action sequence exhausted")
			}
			pendingPing = state.nextAction
			state.nextAction++
			ping, _ := json.Marshal(map[string]any{"seq": pendingPing, "action": "ping", "data": map[string]any{}})
			pingCtx, cancel := context.WithTimeout(ctx, options.HeartbeatTimeout)
			err := conn.Write(pingCtx, transport.MessageText, ping)
			cancel()
			if err != nil {
				return stable, errors.New("WebSocket heartbeat failed")
			}
			pongDeadline = options.Now().Add(options.HeartbeatTimeout)
		case result := <-reads:
			timer.Stop()
			if result.err != nil {
				return stable, result.err
			}
			if result.type_ != transport.MessageText {
				if err := emitDiagnostic(options, WatchDiagnostic{Type: "malformed", Message: "Binary WebSocket frame skipped."}); err != nil {
					return false, err
				}
				continue
			}
			frame, err := decodeFrame(result.data)
			if err != nil {
				message := "Malformed WebSocket frame skipped."
				if errors.Is(err, ErrOversizedWatchFrame) {
					message = ErrOversizedWatchFrame.Error()
				}
				if sinkErr := emitDiagnostic(options, WatchDiagnostic{Type: "malformed", Message: message}); sinkErr != nil {
					return false, sinkErr
				}
				continue
			}
			if frame.Status == "FAIL" {
				if !state.authenticated && frame.Reply != nil && *frame.Reply == authSeq {
					return false, ErrWatchAuthentication
				}
				return stable, errors.New("WebSocket request failed")
			}
			if frame.Status == "OK" && frame.Reply != nil {
				if *frame.Reply == authSeq {
					state.authenticated = true
				}
				if *frame.Reply == pendingPing && !pongDeadline.IsZero() {
					pongDeadline = time.Time{}
					stable = true
					heartbeatAt = options.Now().Add(options.HeartbeatInterval)
				}
			}
			if frame.Event == "" {
				continue
			}
			if frame.Seq == nil {
				if err := emitDiagnostic(options, WatchDiagnostic{Type: "malformed", Message: "WebSocket event without sequence skipped."}); err != nil {
					return false, err
				}
				continue
			}
			if frame.Event == "hello" {
				id, ok := connectionID(frame.Data)
				if !ok {
					if err := emitDiagnostic(options, WatchDiagnostic{Type: "malformed", Message: "Malformed WebSocket hello skipped."}); err != nil {
						return false, err
					}
					continue
				}
				if state.connectionID != "" && id != state.connectionID {
					expected, received := state.nextServer, *frame.Seq
					if err := emitDiagnostic(options, WatchDiagnostic{Type: "connection_changed", Message: "WebSocket connection changed; live events may be missing; no REST backfill was attempted.", PreviousID: state.connectionID, CurrentID: id, Expected: &expected, Received: &received}); err != nil {
						return false, err
					}
					state.connectionID, state.nextServer = id, 0
					if *frame.Seq != 0 {
						return false, errors.New("WebSocket sequence mismatch")
					}
				}
				if *frame.Seq != state.nextServer {
					expected, received := state.nextServer, *frame.Seq
					if err := emitDiagnostic(options, WatchDiagnostic{Type: "sequence_gap", Message: "WebSocket sequence mismatch; live events may be missing; no REST backfill was attempted.", Expected: &expected, Received: &received}); err != nil {
						return false, err
					}
					return false, errors.New("WebSocket sequence mismatch")
				}
				state.connectionID, state.nextServer, state.hello = id, *frame.Seq+1, true
			} else {
				if *frame.Seq != state.nextServer {
					expected, received := state.nextServer, *frame.Seq
					if err := emitDiagnostic(options, WatchDiagnostic{Type: "sequence_gap", Message: "WebSocket sequence mismatch; frame suppressed; no REST backfill was attempted.", Expected: &expected, Received: &received}); err != nil {
						return false, err
					}
					return stable, errors.New("WebSocket sequence mismatch")
				}
				state.nextServer++
			}
			if state.authenticated && state.hello && heartbeatAt.IsZero() {
				heartbeatAt = options.Now().Add(options.HeartbeatInterval)
			}
			if !state.authenticated || !state.hello || frame.Event != "posted" {
				continue
			}
			post, ok := parsePost(frame)
			if !ok {
				if err := emitDiagnostic(options, WatchDiagnostic{Type: "malformed", Message: "Malformed WebSocket post payload skipped."}); err != nil {
					return false, err
				}
				continue
			}
			if options.ChannelID != "" && post.ChannelID != options.ChannelID || !dedupe.Add(post.ID) {
				continue
			}
			if err := options.Sink.Post(post, Sequence{ConnectionID: state.connectionID, Number: *frame.Seq}); err != nil {
				return false, ErrWatchSink
			}
		}
	}
}

func validateWatchOptions(options *WatchOptions) error {
	if _, err := serverurl.Normalize(options.URL); err != nil || options.Token == "" || options.Sink == nil {
		return ErrInvalidWatchOptions
	}
	if options.Dial == nil {
		options.Dial = transport.Dial
	}
	if options.HandshakeTimeout == 0 {
		options.HandshakeTimeout = 15 * time.Second
	}
	if options.HeartbeatInterval == 0 {
		options.HeartbeatInterval = 30 * time.Second
	}
	if options.HeartbeatTimeout == 0 {
		options.HeartbeatTimeout = 10 * time.Second
	}
	if options.CloseTimeout == 0 {
		options.CloseTimeout = time.Second
	}
	if options.MaxReconnects == 0 {
		options.MaxReconnects = 8
	}
	if options.Random == nil {
		options.Random = func() float64 { return .5 }
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.NewTimer == nil {
		options.NewTimer = func(duration time.Duration) WatchTimer { return realWatchTimer{time.NewTimer(duration)} }
	}
	if options.HandshakeTimeout <= 0 || options.HeartbeatInterval <= 0 || options.HeartbeatTimeout <= 0 || options.CloseTimeout <= 0 || options.MaxReconnects < 0 {
		return ErrInvalidWatchOptions
	}
	random := options.Random()
	if math.IsNaN(random) || random < 0 || random > 1 {
		return ErrInvalidWatchOptions
	}
	return nil
}

func watchURL(raw, connectionID string, next int64) (string, error) {
	normalized, err := serverurl.Normalize(raw)
	if err != nil {
		return "", err
	}
	parsed, _ := url.Parse(normalized)
	if parsed.Scheme == "https" {
		parsed.Scheme = "wss"
	} else {
		parsed.Scheme = "ws"
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/api/v4/websocket"
	if connectionID != "" {
		query := parsed.Query()
		query.Set("connection_id", connectionID)
		query.Set("sequence_number", fmt.Sprint(next))
		parsed.RawQuery = query.Encode()
	}
	return parsed.String(), nil
}
func connectionID(data json.RawMessage) (string, bool) {
	var value struct {
		ConnectionID string `json:"connection_id"`
	}
	err := json.Unmarshal(data, &value)
	return value.ConnectionID, err == nil && value.ConnectionID != ""
}
func emitDiagnostic(options WatchOptions, diagnostic WatchDiagnostic) error {
	diagnostic.Timestamp = options.Now().UTC()
	diagnostic.Backfill = false
	if err := options.Sink.Diagnostic(diagnostic); err != nil {
		return ErrWatchSink
	}
	return nil
}
func jitterBackoff(attempt int, random float64) time.Duration {
	base := time.Second * time.Duration(1<<min(attempt-1, 5))
	if base > 30*time.Second {
		base = 30 * time.Second
	}
	return time.Duration(float64(base) * (0.8 + 0.4*random))
}

type PostDeduplicator struct {
	capacity int
	order    []string
	seen     map[string]struct{}
}

func NewPostDeduplicator(capacity int) *PostDeduplicator {
	return &PostDeduplicator{capacity: capacity, seen: make(map[string]struct{}, capacity)}
}
func (set *PostDeduplicator) Add(id string) bool {
	if _, ok := set.seen[id]; ok {
		return false
	}
	if len(set.order) == set.capacity {
		delete(set.seen, set.order[0])
		copy(set.order, set.order[1:])
		set.order = set.order[:len(set.order)-1]
	}
	set.order = append(set.order, id)
	set.seen[id] = struct{}{}
	return true
}
