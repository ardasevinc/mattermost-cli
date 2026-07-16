package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ardasevinc/mattermost-cli/internal/presentation"
)

func TestReadAuthJSONAndRetryCounts(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer token-value" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.URL.Path; got != "/api/v4/users/me" {
			t.Errorf("path = %q", got)
		}
		if attempts.Add(1) < 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = io.WriteString(w, `{"id":"me"}`)
	}))
	defer server.Close()
	c := newTestClient(t, server.URL, WithSleep(noSleep))
	var result struct {
		ID string `json:"id"`
	}
	if err := c.Get(context.Background(), "/users/me", &result); err != nil {
		t.Fatal(err)
	}
	if result.ID != "me" || attempts.Load() != 3 {
		t.Fatalf("result=%+v attempts=%d", result, attempts.Load())
	}
}

func TestEndpointPreservesQueryAndEscapedPathComponents(t *testing.T) {
	var requestURI string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestURI = r.RequestURI
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()
	c := newTestClient(t, server.URL)
	if err := c.Get(context.Background(), "/users/a%2Fb?page=2&active=true", &struct{}{}); err != nil {
		t.Fatal(err)
	}
	if want := "/api/v4/users/a%2Fb?page=2&active=true"; requestURI != want {
		t.Fatalf("RequestURI = %q, want %q", requestURI, want)
	}
}

func TestEndpointPreservesBasePathEndingInEncodedSlash(t *testing.T) {
	var requestURI string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestURI = r.RequestURI
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()
	c := newTestClient(t, server.URL+"/mattermost%2F")
	if err := c.Get(context.Background(), "/users/me", &struct{}{}); err != nil {
		t.Fatal(err)
	}
	if want := "/mattermost%2F/api/v4/users/me"; requestURI != want {
		t.Fatalf("RequestURI = %q, want %q", requestURI, want)
	}
}

func TestEndpointRejectsAmbiguousRequestTargetsBeforeDispatch(t *testing.T) {
	transport := &countingTransport{err: errors.New("must not be called")}
	c := newTestClient(t, "https://mattermost.example", WithRoundTripper(transport))
	for _, path := range []string{"users", "//evil.example/x", "/../posts", "/users#fragment", "/bad%zz"} {
		if err := c.Get(context.Background(), path, &struct{}{}); err == nil || err.Error() != "invalid Mattermost API path" {
			t.Errorf("Get(%q) error = %v", path, err)
		}
	}
	if transport.count.Load() != 0 {
		t.Fatalf("attempts = %d", transport.count.Load())
	}
}

func TestReadRetriesTransportExactlyTwice(t *testing.T) {
	transport := &countingTransport{err: errors.New("contains token-value")}
	c := newTestClient(t, "https://mattermost.example", WithRoundTripper(transport), WithSleep(noSleep))
	err := c.Get(context.Background(), "/users/me", &struct{}{})
	if !errors.Is(err, ErrNetwork) || strings.Contains(err.Error(), "token-value") {
		t.Fatalf("error = %v", err)
	}
	if transport.count.Load() != 3 {
		t.Fatalf("attempts = %d", transport.count.Load())
	}
}

func TestReadOnlyPostUsesBoundedSafeRetries(t *testing.T) {
	transport := &sequenceTransport{statuses: []int{503, 503, 200}, headers: make([]http.Header, 3)}
	c := newTestClient(t, "https://mattermost.example", WithRoundTripper(transport), WithSleep(noSleep))
	if err := c.PostRead(context.Background(), "/users/search", map[string]string{"term": "arda"}, &struct{}{}); err != nil {
		t.Fatal(err)
	}
	if transport.index != 3 {
		t.Fatalf("attempts = %d", transport.index)
	}
}

func TestMutationNeverReplaysAndClassifiesOutcomes(t *testing.T) {
	for _, status := range []int{300, 307, 500, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var attempts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { attempts.Add(1); w.WriteHeader(status) }))
			defer server.Close()
			c := newTestClient(t, server.URL, WithSleep(noSleep))
			err := c.Post(context.Background(), "/posts", map[string]string{"message": "hi"}, &struct{}{})
			var unknown *OutcomeUnknownError
			if !errors.As(err, &unknown) || attempts.Load() != 1 {
				t.Fatalf("error=%v attempts=%d", err, attempts.Load())
			}
		})
	}
	for _, status := range []int{400, 401, 429, 499} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
			defer server.Close()
			c := newTestClient(t, server.URL)
			var apiErr *APIError
			if err := c.Post(context.Background(), "/posts", nil, nil); !errors.As(err, &apiErr) || apiErr.Status != status {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestMutationInformationalStatusIsUnknown(t *testing.T) {
	c := newTestClient(t, "https://mattermost.example", WithRoundTripper(&statusTransport{status: 101}))
	var unknown *OutcomeUnknownError
	if err := c.Post(context.Background(), "/posts", nil, nil); !errors.As(err, &unknown) {
		t.Fatalf("error = %v", err)
	}
}

func TestMutationRedirectDoesNotReplay(t *testing.T) {
	var original, redirected atomic.Int32
	server := httptest.NewServer(nil)
	defer server.Close()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/posts", func(w http.ResponseWriter, r *http.Request) {
		original.Add(1)
		http.Redirect(w, r, server.URL+"/redirected", http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("/redirected", func(w http.ResponseWriter, r *http.Request) { redirected.Add(1) })
	server.Config.Handler = mux
	c := newTestClient(t, server.URL)
	var unknown *OutcomeUnknownError
	if err := c.Post(context.Background(), "/posts", map[string]string{"x": "y"}, nil); !errors.As(err, &unknown) {
		t.Fatalf("error = %v", err)
	}
	if original.Load() != 1 || redirected.Load() != 0 {
		t.Fatalf("original=%d redirected=%d", original.Load(), redirected.Load())
	}
}

func TestReadRedirectSameOriginOnlyAndNeverLeaksAuth(t *testing.T) {
	var leaked atomic.Bool
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			leaked.Store(true)
		}
		_, _ = io.WriteString(w, `{}`)
	}))
	defer evil.Close()
	var sameHits atomic.Int32
	var origin *httptest.Server
	origin = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v4/same":
			http.Redirect(w, r, origin.URL+"/api/v4/final", http.StatusFound)
		case "/api/v4/final":
			sameHits.Add(1)
			if r.Header.Get("Authorization") != "Bearer token-value" {
				t.Error("same-origin auth missing")
			}
			_, _ = io.WriteString(w, `{}`)
		default:
			http.Redirect(w, r, evil.URL, http.StatusFound)
		}
	}))
	defer origin.Close()
	c := newTestClient(t, origin.URL)
	if err := c.Get(context.Background(), "/same", &struct{}{}); err != nil {
		t.Fatal(err)
	}
	var apiErr *APIError
	if err := c.Get(context.Background(), "/cross", &struct{}{}); !errors.As(err, &apiErr) || apiErr.Status != 302 {
		t.Fatalf("error = %v", err)
	}
	if leaked.Load() || sameHits.Load() != 1 {
		t.Fatalf("leaked=%v sameHits=%d", leaked.Load(), sameHits.Load())
	}
}

func TestAttemptTimeoutCoversBodyAndCancellationStopsSleep(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	c := newTestClient(t, server.URL, WithAttemptTimeout(20*time.Millisecond), WithSleep(noSleep))
	if err := c.Get(context.Background(), "/slow", &struct{}{}); !errors.Is(err, ErrTimeout) {
		t.Fatalf("error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	c2 := newTestClient(t, "https://mattermost.example", WithRoundTripper(&statusTransport{status: 503}), WithSleep(func(ctx context.Context, _ time.Duration) error { cancel(); <-ctx.Done(); return ctx.Err() }))
	if err := c2.Get(ctx, "/x", &struct{}{}); !errors.Is(err, ErrCanceled) {
		t.Fatalf("error = %v", err)
	}
}

func TestBoundedAndTruncatedBodies(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"oversized": func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, strings.Repeat("x", 9)) },
		"truncated": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "20")
			_, _ = io.WriteString(w, `{"id":`)
		},
	} {
		t.Run(name, func(t *testing.T) {
			var attempts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { attempts.Add(1); handler(w, r) }))
			defer server.Close()
			c := newTestClient(t, server.URL, WithResponseLimit(8), WithSleep(noSleep))
			if err := c.Get(context.Background(), "/x", &struct{}{}); err == nil {
				t.Fatal("expected error")
			}
			if attempts.Load() != 3 {
				t.Fatalf("attempts = %d", attempts.Load())
			}
		})
	}
}

func TestMutationMalformedSuccessAndTruncationAreUnknown(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"malformed": func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, `{bad`) },
		"truncated": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "20")
			_, _ = io.WriteString(w, `{`)
		},
	} {
		t.Run(name, func(t *testing.T) {
			var attempts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { attempts.Add(1); handler(w, r) }))
			defer server.Close()
			c := newTestClient(t, server.URL)
			var unknown *OutcomeUnknownError
			if err := c.Post(context.Background(), "/posts", nil, &struct{}{}); !errors.As(err, &unknown) {
				t.Fatalf("error = %v", err)
			}
			if attempts.Load() != 1 {
				t.Fatalf("attempts = %d", attempts.Load())
			}
		})
	}
}

func TestMutationWithIgnoredOutputStillValidatesSuccessJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{bad`)
	}))
	defer server.Close()
	c := newTestClient(t, server.URL)
	var unknown *OutcomeUnknownError
	if err := c.Delete(context.Background(), "/posts/id", nil); !errors.As(err, &unknown) {
		t.Fatalf("error = %v", err)
	}
}

func TestJSONDoesNotEscapeHTMLAndCredentialLifecycle(t *testing.T) {
	presentation.ActiveCredentials.Clear()
	var body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		body = string(data)
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()
	c, err := New(server.URL, "owned-token")
	if err != nil {
		t.Fatal(err)
	}
	if got := presentation.ActiveCredentials.Values(); len(got) != 1 || got[0] != "owned-token" {
		t.Fatalf("credentials = %v", got)
	}
	if err := c.Post(context.Background(), "/posts", map[string]string{"message": "<tag>&"}, &struct{}{}); err != nil {
		t.Fatal(err)
	}
	if body != `{"message":"<tag>&"}` {
		t.Fatalf("body = %q", body)
	}
	c.Close()
	c.Close()
	if got := presentation.ActiveCredentials.Values(); len(got) != 0 {
		t.Fatalf("credentials after close = %v", got)
	}
}

func TestJSONMatchesJavaScriptLineSeparatorEncoding(t *testing.T) {
	encoded, err := encodeBody(map[string]string{"message": "before\u2028middle\u2029after literal \\u2028"})
	if err != nil {
		t.Fatal(err)
	}
	want := "{\"message\":\"before\u2028middle\u2029after literal \\\\u2028\"}"
	if string(encoded) != want {
		t.Fatalf("encoded = %q, want %q", encoded, want)
	}
}

func TestContentTypeIsPresentWithoutRequestBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q", got)
		}
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()
	c := newTestClient(t, server.URL)
	if err := c.Get(context.Background(), "/users/me", &struct{}{}); err != nil {
		t.Fatal(err)
	}
}

func TestPublicReadNeverSendsAuthorization(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("Authorization = %q", got)
		}
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()
	c := newTestClient(t, server.URL)
	if err := c.GetPublic(context.Background(), "/system/ping?get_server_status=true", &struct{}{}); err != nil {
		t.Fatal(err)
	}
}

func TestCloseWaitsForInFlightRequestBeforeReleasingCredential(t *testing.T) {
	presentation.ActiveCredentials.Clear()
	started := make(chan struct{})
	proceed := make(chan struct{})
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		close(started)
		<-proceed
		return &http.Response{
			StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`)),
		}, nil
	})
	c, err := New("https://mattermost.example", "in-flight-token", WithRoundTripper(transport))
	if err != nil {
		t.Fatal(err)
	}
	requestDone := make(chan error, 1)
	go func() { requestDone <- c.Get(context.Background(), "/users/me", &struct{}{}) }()
	<-started
	closeDone := make(chan struct{})
	go func() { c.Close(); close(closeDone) }()
	select {
	case <-closeDone:
		t.Fatal("Close returned while request still used the credential")
	case <-time.After(20 * time.Millisecond):
	}
	if got := presentation.ActiveCredentials.Values(); len(got) != 1 || got[0] != "in-flight-token" {
		t.Fatalf("credentials during request = %v", got)
	}
	close(proceed)
	if err := <-requestDone; err != nil {
		t.Fatal(err)
	}
	<-closeDone
	if got := presentation.ActiveCredentials.Values(); len(got) != 0 {
		t.Fatalf("credentials after close = %v", got)
	}
	if err := c.Get(context.Background(), "/users/me", &struct{}{}); !errors.Is(err, ErrClientClosed) {
		t.Fatalf("request after close error = %v", err)
	}
}

func TestCanceledMutationBeforeDispatchIsDefinitive(t *testing.T) {
	transport := &countingTransport{err: errors.New("must not be called")}
	c := newTestClient(t, "https://mattermost.example", WithRoundTripper(transport))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.Post(ctx, "/posts", map[string]string{"message": "hi"}, &struct{}{}); !errors.Is(err, ErrCanceled) {
		t.Fatalf("error = %v", err)
	}
	if transport.count.Load() != 0 {
		t.Fatalf("attempts = %d", transport.count.Load())
	}
}

func TestErrorResponsesAreNotConsumed(t *testing.T) {
	body := &trackingBody{}
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusBadRequest, Header: make(http.Header), Body: body}, nil
	})
	c := newTestClient(t, "https://mattermost.example", WithRoundTripper(transport))
	var apiErr *APIError
	if err := c.Post(context.Background(), "/posts", nil, nil); !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest {
		t.Fatalf("error = %v", err)
	}
	if body.reads.Load() != 0 || !body.closed.Load() {
		t.Fatalf("reads=%d closed=%v", body.reads.Load(), body.closed.Load())
	}
}

func TestRateLimitDelayBoundsHeaders(t *testing.T) {
	var delays []time.Duration
	retryAfter := make(http.Header)
	retryAfter.Set("Retry-After", "Thu, 16 Jul 2026 12:00:03 GMT")
	rateReset := make(http.Header)
	rateReset.Set("X-RateLimit-Reset", "3600")
	transport := &sequenceTransport{statuses: []int{429, 429, 200}, headers: []http.Header{retryAfter, rateReset, nil}}
	c := newTestClient(t, "https://mattermost.example", WithRoundTripper(transport), WithClock(func() time.Time { return time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC) }), WithSleep(func(_ context.Context, d time.Duration) error { delays = append(delays, d); return nil }))
	if err := c.Get(context.Background(), "/x", &struct{}{}); err != nil {
		t.Fatal(err)
	}
	if len(delays) != 2 || delays[0] != 3*time.Second || delays[1] != 30*time.Second {
		t.Fatalf("delays = %v", delays)
	}
}

func newTestClient(t *testing.T, base string, options ...Option) *Client {
	t.Helper()
	c, err := New(base, "token-value", options...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}
func noSleep(context.Context, time.Duration) error { return nil }

type countingTransport struct {
	count atomic.Int32
	err   error
}

func (t *countingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	t.count.Add(1)
	return nil, t.err
}

type statusTransport struct{ status int }

func (t *statusTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: t.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`))}, nil
}

type sequenceTransport struct {
	index    int
	statuses []int
	headers  []http.Header
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

type trackingBody struct {
	reads  atomic.Int32
	closed atomic.Bool
}

func (b *trackingBody) Read([]byte) (int, error) {
	b.reads.Add(1)
	return 0, io.EOF
}

func (b *trackingBody) Close() error {
	b.closed.Store(true)
	return nil
}

func (t *sequenceTransport) RoundTrip(*http.Request) (*http.Response, error) {
	i := t.index
	t.index++
	return &http.Response{StatusCode: t.statuses[i], Header: t.headers[i], Body: io.NopCloser(strings.NewReader(`{}`))}, nil
}
