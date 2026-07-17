// Package api provides the bounded, authenticated Mattermost HTTP transport.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ardasevinc/mattermost-cli/internal/presentation"
	"github.com/ardasevinc/mattermost-cli/internal/serverurl"
)

const (
	AttemptTimeout  = 15 * time.Second
	MaxResponseBody = 4 << 20
	maxRetries      = 2
	maxRetryDelay   = 30 * time.Second
)

var (
	ErrNetwork      = errors.New("unable to connect to Mattermost due to a network error")
	ErrTimeout      = errors.New("Mattermost request timed out")
	ErrCanceled     = errors.New("Mattermost request canceled")
	ErrInvalidJSON  = errors.New("Mattermost returned an invalid JSON response")
	ErrBodyTooLarge = errors.New("Mattermost response exceeded the size limit")
	ErrClientClosed = errors.New("Mattermost client is closed")
	ErrMutationUsed = errors.New("prepared Mattermost mutation was already consumed")
)

type APIError struct{ Status int }

func (e *APIError) Error() string {
	return fmt.Sprintf("Mattermost API request failed with status %d", e.Status)
}

type OutcomeUnknownError struct{}

func (*OutcomeUnknownError) Error() string {
	return "Mattermost did not confirm the write; its outcome is unknown, check the destination before retrying"
}

type SleepFunc func(context.Context, time.Duration) error
type Option func(*Client)

func WithRoundTripper(transport http.RoundTripper) Option {
	return func(c *Client) {
		if transport != nil {
			c.transport = transport
		}
	}
}
func WithClock(now func() time.Time) Option {
	return func(c *Client) {
		if now != nil {
			c.now = now
		}
	}
}
func WithSleep(sleep SleepFunc) Option {
	return func(c *Client) {
		if sleep != nil {
			c.sleep = sleep
		}
	}
}
func WithAttemptTimeout(timeout time.Duration) Option {
	return func(c *Client) {
		if timeout > 0 {
			c.timeout = timeout
		}
	}
}
func WithResponseLimit(limit int64) Option {
	return func(c *Client) {
		if limit > 0 {
			c.bodyLimit = limit
		}
	}
}

type Client struct {
	base      *url.URL
	token     string
	http      *http.Client
	transport http.RoundTripper
	now       func() time.Time
	sleep     SleepFunc
	timeout   time.Duration
	bodyLimit int64
	release   func()
	lifecycle sync.RWMutex
	closed    bool
}

// PreparedMutation is an immutable, one-shot JSON mutation. URL resolution and
// body encoding happen before it is returned so callers can durably record
// dispatch intent immediately before Execute. Once Execute starts, every local,
// transport, response, or cancellation failure is conservatively outcome
// unknown; callers must never retry the same effect automatically.
type PreparedMutation struct {
	client         *Client
	method         string
	endpoint       *url.URL
	payload        []byte
	expectedStatus int
	state          *preparedMutationState
}

type preparedMutationState struct {
	mu   sync.Mutex
	used bool
}

type requestContextKey uint8

const mutationRequestKey requestContextKey = 1

func New(baseURL, token string, options ...Option) (*Client, error) {
	normalized, err := serverurl.Normalize(baseURL)
	if err != nil {
		return nil, err
	}
	base, err := url.Parse(normalized)
	if err != nil {
		return nil, errors.New("invalid Mattermost URL")
	}
	c := &Client{
		base: base, token: token, transport: http.DefaultTransport, now: time.Now,
		timeout: AttemptTimeout, bodyLimit: MaxResponseBody,
	}
	c.sleep = sleepContext
	for _, option := range options {
		option(c)
	}
	c.http = &http.Client{Transport: c.transport, CheckRedirect: c.checkRedirect}
	c.release = presentation.ActiveCredentials.Register(token)
	return c, nil
}

func (c *Client) Close() {
	c.lifecycle.Lock()
	defer c.lifecycle.Unlock()
	if c.closed {
		return
	}
	c.closed = true
	if c.release != nil {
		c.release()
	}
}

func (c *Client) Get(ctx context.Context, path string, out any) error {
	return c.request(ctx, http.MethodGet, path, nil, out, false, true)
}
func (c *Client) GetPublic(ctx context.Context, path string, out any) error {
	return c.request(ctx, http.MethodGet, path, nil, out, false, false)
}
func (c *Client) Post(ctx context.Context, path string, body, out any) error {
	return c.request(ctx, http.MethodPost, path, body, out, true, true)
}
func (c *Client) PostRead(ctx context.Context, path string, body, out any) error {
	return c.request(ctx, http.MethodPost, path, body, out, false, true)
}
func (c *Client) Put(ctx context.Context, path string, body, out any) error {
	return c.request(ctx, http.MethodPut, path, body, out, true, true)
}
func (c *Client) Delete(ctx context.Context, path string, out any) error {
	return c.request(ctx, http.MethodDelete, path, nil, out, true, true)
}

func (c *Client) PreparePost(path string, body any) (*PreparedMutation, error) {
	return c.prepareMutation(http.MethodPost, path, body, 0)
}

func (c *Client) PreparePut(path string, body any) (*PreparedMutation, error) {
	return c.prepareMutation(http.MethodPut, path, body, 0)
}

func (c *Client) PrepareDelete(path string) (*PreparedMutation, error) {
	return c.prepareMutation(http.MethodDelete, path, nil, 0)
}

func (c *Client) PreparePostStatus(path string, body any, expectedStatus int) (*PreparedMutation, error) {
	return c.prepareMutation(http.MethodPost, path, body, expectedStatus)
}

func (c *Client) PreparePutStatus(path string, body any, expectedStatus int) (*PreparedMutation, error) {
	return c.prepareMutation(http.MethodPut, path, body, expectedStatus)
}

func (c *Client) PrepareDeleteStatus(path string, expectedStatus int) (*PreparedMutation, error) {
	return c.prepareMutation(http.MethodDelete, path, nil, expectedStatus)
}

func (c *Client) prepareMutation(method, path string, body any, expectedStatus int) (*PreparedMutation, error) {
	c.lifecycle.RLock()
	defer c.lifecycle.RUnlock()
	if c.closed {
		return nil, ErrClientClosed
	}
	if expectedStatus != 0 && (expectedStatus < 200 || expectedStatus >= 300) {
		return nil, errors.New("invalid expected Mattermost status")
	}
	endpoint, err := c.endpoint(path)
	if err != nil {
		return nil, err
	}
	payload, err := encodeBody(body)
	if err != nil {
		return nil, errors.New("unable to encode Mattermost request")
	}
	return &PreparedMutation{client: c, method: method, endpoint: endpoint, payload: bytes.Clone(payload), expectedStatus: expectedStatus, state: &preparedMutationState{}}, nil
}

// Execute consumes the prepared mutation before inspecting cancellation or
// client state. This makes a durable dispatch intent conservative: any failure
// after the caller records it is unknown and the mutation cannot be replayed.
func (p *PreparedMutation) Execute(ctx context.Context, out any) error {
	if p == nil || p.state == nil || p.client == nil || p.endpoint == nil {
		return consumedMutationError()
	}
	p.state.mu.Lock()
	if p.state.used {
		p.state.mu.Unlock()
		return consumedMutationError()
	}
	p.state.used = true
	p.state.mu.Unlock()

	p.client.lifecycle.RLock()
	defer p.client.lifecycle.RUnlock()
	if p.client.closed || ctx == nil {
		return &OutcomeUnknownError{}
	}
	status, _, data, failure := p.client.attempt(ctx, p.method, p.endpoint, p.payload, true, true)
	if failure != nil {
		return &OutcomeUnknownError{}
	}
	if status >= 400 && status < 500 {
		return &APIError{Status: status}
	}
	if status < 200 || status >= 300 {
		return &OutcomeUnknownError{}
	}
	if p.expectedStatus != 0 && status != p.expectedStatus {
		return &OutcomeUnknownError{}
	}
	decodeTarget := out
	if decodeTarget == nil {
		decodeTarget = new(any)
	}
	if err := decodeJSON(data, decodeTarget); err != nil {
		return &OutcomeUnknownError{}
	}
	return nil
}

func consumedMutationError() error {
	return errors.Join(&OutcomeUnknownError{}, ErrMutationUsed)
}

func (c *Client) request(ctx context.Context, method, path string, body, out any, mutation, authenticated bool) error {
	c.lifecycle.RLock()
	defer c.lifecycle.RUnlock()
	if c.closed {
		return ErrClientClosed
	}
	endpoint, err := c.endpoint(path)
	if err != nil {
		return err
	}
	payload, err := encodeBody(body)
	if err != nil {
		return errors.New("unable to encode Mattermost request")
	}
	for attempt := 0; ; attempt++ {
		if ctx.Err() != nil {
			return classifyContext(ctx)
		}
		status, headers, data, failure := c.attempt(ctx, method, endpoint, payload, mutation, authenticated)
		if failure != nil {
			if mutation {
				return &OutcomeUnknownError{}
			}
			if retryableFailure(failure) && attempt < maxRetries {
				if err := c.sleep(ctx, retryDelay(attempt)); err != nil {
					return classifyContext(ctx)
				}
				continue
			}
			return publicReadError(ctx, failure)
		}
		if mutation {
			if status >= 400 && status < 500 {
				return &APIError{Status: status}
			}
			if status < 200 || status >= 300 {
				return &OutcomeUnknownError{}
			}
		} else {
			if (status == 429 || status == 502 || status == 503 || status == 504) && attempt < maxRetries {
				delay := retryDelay(attempt)
				if status == 429 {
					delay = c.rateLimitDelay(headers, attempt)
				}
				if err := c.sleep(ctx, delay); err != nil {
					return classifyContext(ctx)
				}
				continue
			}
			if status < 200 || status >= 300 {
				return &APIError{Status: status}
			}
		}
		decodeTarget := out
		if decodeTarget == nil {
			decodeTarget = new(any)
		}
		if err := decodeJSON(data, decodeTarget); err != nil {
			if mutation {
				return &OutcomeUnknownError{}
			}
			return ErrInvalidJSON
		}
		return nil
	}
}

type attemptFailure struct{ kind string }

func (e *attemptFailure) Error() string { return e.kind }

func (c *Client) attempt(parent context.Context, method string, endpoint *url.URL, payload []byte, mutation, authenticated bool) (int, http.Header, []byte, error) {
	ctx, cancel := context.WithTimeout(parent, c.timeout)
	defer cancel()
	if mutation {
		ctx = context.WithValue(ctx, mutationRequestKey, true)
	}
	var body io.Reader = bytes.NewReader(payload)
	if mutation {
		// bytes.Reader makes requests replayable by populating GetBody, and a
		// nil body becomes http.NoBody. Both permit transparent HTTP/2 retries.
		// A distinct ReadCloser keeps even empty mutations non-replayable.
		body = io.NopCloser(bytes.NewReader(payload))
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint.String(), body)
	if err != nil {
		return 0, nil, nil, &attemptFailure{kind: "transport"}
	}
	if mutation {
		req.ContentLength = int64(len(payload))
		req.GetBody = nil
	}
	if authenticated {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		if parent.Err() != nil {
			return 0, nil, nil, &attemptFailure{kind: "canceled"}
		}
		if ctx.Err() != nil {
			return 0, nil, nil, &attemptFailure{kind: "timeout"}
		}
		return 0, nil, nil, &attemptFailure{kind: "transport"}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, resp.Header.Clone(), nil, nil
	}
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, c.bodyLimit+1))
	if parent.Err() != nil {
		return 0, nil, nil, &attemptFailure{kind: "canceled"}
	}
	if ctx.Err() != nil {
		return 0, nil, nil, &attemptFailure{kind: "timeout"}
	}
	if readErr != nil {
		if mutation && resp.StatusCode >= 400 && resp.StatusCode < 500 {
			return resp.StatusCode, resp.Header.Clone(), nil, nil
		}
		return 0, nil, nil, &attemptFailure{kind: "transport"}
	}
	if int64(len(data)) > c.bodyLimit {
		if mutation && resp.StatusCode >= 400 && resp.StatusCode < 500 {
			return resp.StatusCode, resp.Header.Clone(), nil, nil
		}
		return 0, nil, nil, &attemptFailure{kind: "oversized"}
	}
	return resp.StatusCode, resp.Header.Clone(), data, nil
}

func (c *Client) endpoint(path string) (*url.URL, error) {
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || strings.Contains(path, "#") {
		return nil, errors.New("invalid Mattermost API path")
	}
	relative, err := url.ParseRequestURI(path)
	if err != nil || relative.IsAbs() || relative.Host != "" || relative.User != nil || relative.Fragment != "" {
		return nil, errors.New("invalid Mattermost API path")
	}
	for _, segment := range strings.Split(relative.Path, "/") {
		if segment == "." || segment == ".." {
			return nil, errors.New("invalid Mattermost API path")
		}
	}
	u := *c.base
	u.Path = c.base.Path + "/api/v4" + relative.Path
	u.RawPath = c.base.EscapedPath() + "/api/v4" + relative.EscapedPath()
	u.RawQuery = relative.RawQuery
	u.Fragment = ""
	return &u, nil
}

func (c *Client) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) == 0 {
		return http.ErrUseLastResponse
	}
	first := via[0]
	if mutation, _ := first.Context().Value(mutationRequestKey).(bool); mutation {
		return http.ErrUseLastResponse
	}
	if !sameOrigin(first.URL, req.URL) {
		return http.ErrUseLastResponse
	}
	req.Header.Set("Authorization", first.Header.Get("Authorization"))
	return nil
}

func sameOrigin(a, b *url.URL) bool {
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Hostname(), b.Hostname()) && effectivePort(a) == effectivePort(b)
}
func effectivePort(u *url.URL) string {
	if u.Port() != "" {
		return u.Port()
	}
	if strings.EqualFold(u.Scheme, "https") {
		return "443"
	}
	if strings.EqualFold(u.Scheme, "http") {
		return "80"
	}
	return ""
}

func encodeBody(body any) ([]byte, error) {
	if body == nil {
		return nil, nil
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(body); err != nil {
		return nil, err
	}
	data := bytes.TrimSuffix(buffer.Bytes(), []byte{'\n'})
	return restoreJSONLineSeparators(data), nil
}

func restoreJSONLineSeparators(data []byte) []byte {
	var output bytes.Buffer
	output.Grow(len(data))
	for index := 0; index < len(data); {
		if index+6 <= len(data) && data[index] == '\\' &&
			(bytes.Equal(data[index:index+6], []byte(`\u2028`)) || bytes.Equal(data[index:index+6], []byte(`\u2029`))) {
			precedingSlashes := 0
			for cursor := index - 1; cursor >= 0 && data[cursor] == '\\'; cursor-- {
				precedingSlashes++
			}
			if precedingSlashes%2 == 0 {
				if data[index+5] == '8' {
					output.WriteString("\u2028")
				} else {
					output.WriteString("\u2029")
				}
				index += 6
				continue
			}
		}
		output.WriteByte(data[index])
		index++
	}
	return output.Bytes()
}

func decodeJSON(data []byte, out any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(out); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("trailing JSON data")
	}
	return nil
}

func retryableFailure(err error) bool {
	var failure *attemptFailure
	return errors.As(err, &failure) && (failure.kind == "transport" || failure.kind == "timeout" || failure.kind == "oversized")
}
func publicReadError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return classifyContext(ctx)
	}
	var failure *attemptFailure
	if errors.As(err, &failure) {
		switch failure.kind {
		case "timeout":
			return ErrTimeout
		case "canceled":
			return ErrCanceled
		case "oversized":
			return ErrBodyTooLarge
		}
	}
	return ErrNetwork
}
func classifyContext(ctx context.Context) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return ErrTimeout
	}
	return ErrCanceled
}
func retryDelay(attempt int) time.Duration { return time.Second << attempt }
func (c *Client) rateLimitDelay(headers http.Header, attempt int) time.Duration {
	if value := headers.Get("Retry-After"); value != "" {
		if seconds, err := strconv.ParseFloat(value, 64); err == nil {
			return clamp(time.Duration(seconds * float64(time.Second)))
		}
		if when, err := http.ParseTime(value); err == nil {
			return clamp(when.Sub(c.now()))
		}
	}
	if value := headers.Get("X-RateLimit-Reset"); value != "" {
		if seconds, err := strconv.ParseFloat(value, 64); err == nil && seconds > 0 {
			return clamp(time.Duration(seconds * float64(time.Second)))
		}
	}
	return retryDelay(attempt)
}
func clamp(delay time.Duration) time.Duration {
	if delay < 0 {
		return 0
	}
	if delay > maxRetryDelay {
		return maxRetryDelay
	}
	return delay
}
func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
