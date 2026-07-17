package conformance

import (
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
)

const maxRequestBody = 16 << 20

type sequentialServer struct {
	mu        sync.Mutex
	exchanges []HTTPExchange
	protected []string
	next      int
	errors    []string
}

func newSequentialServer(exchanges []HTTPExchange, protected ...string) *sequentialServer {
	nonempty := make([]string, 0, len(protected))
	for _, value := range protected {
		if value != "" {
			nonempty = append(nonempty, value)
		}
	}
	return &sequentialServer{exchanges: exchanges, protected: nonempty}
}

func (s *sequentialServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.next >= len(s.exchanges) {
		s.fail(w, "unexpected request method/path")
		return
	}
	exchange := s.exchanges[s.next]
	s.next++

	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody+1))
	if err != nil {
		s.fail(w, "request %d body read failed", s.next)
		return
	}
	if len(body) > maxRequestBody {
		s.fail(w, "request %d body exceeded harness limit", s.next)
		return
	}

	want := exchange.Request
	if r.Method != want.Method {
		s.errors = append(s.errors, fmt.Sprintf("request %d method = %q, want %q", s.next, r.Method, want.Method))
	}
	if got := r.URL.RequestURI(); got != want.URI {
		s.errors = append(s.errors, fmt.Sprintf("request %d uri mismatched", s.next))
	}
	expectedHeaders := make(map[string]string, len(want.Headers))
	for name, expected := range want.Headers {
		expectedHeaders[http.CanonicalHeaderKey(name)] = expected
	}
	ignoredHeaders := make(map[string]bool, len(want.IgnoreHeaders))
	for _, name := range want.IgnoreHeaders {
		ignoredHeaders[http.CanonicalHeaderKey(name)] = true
	}
	s.scanProtectedCredential(r, body, expectedHeaders)
	for name, expected := range expectedHeaders {
		if got := r.Header.Values(name); !slices.Equal(got, []string{expected}) {
			s.errors = append(s.errors, fmt.Sprintf("request %d header %q mismatched", s.next, name))
		}
	}
	for name := range r.Header {
		canonical := http.CanonicalHeaderKey(name)
		if _, expected := expectedHeaders[canonical]; !expected && !ignoredHeaders[canonical] {
			s.errors = append(s.errors, fmt.Sprintf("request %d had unexpected header %q", s.next, canonical))
		}
	}
	if got := string(body); got != want.Body {
		s.errors = append(s.errors, fmt.Sprintf("request %d body mismatched", s.next))
	}
	if len(s.errors) > 0 {
		http.Error(w, "scenario request mismatch", http.StatusInternalServerError)
		return
	}

	for name, value := range exchange.Response.Headers {
		w.Header().Set(name, value)
	}
	w.WriteHeader(exchange.Response.Status)
	_, _ = io.WriteString(w, exchange.Response.Body)
}

func (s *sequentialServer) scanProtectedCredential(r *http.Request, body []byte, expectedHeaders map[string]string) {
	for _, protected := range s.protected {
		if strings.Contains(r.URL.RequestURI(), protected) || strings.Contains(string(body), protected) {
			s.errors = append(s.errors, fmt.Sprintf("request %d exposed protected credential outside authorization", s.next))
		}
		for name, values := range r.Header {
			canonical := http.CanonicalHeaderKey(name)
			contains := false
			for _, value := range values {
				if strings.Contains(value, protected) {
					contains = true
					break
				}
			}
			if !contains {
				continue
			}
			allowed := canonical == "Authorization" && slices.Equal(values, []string{expectedHeaders[canonical]})
			if !allowed {
				s.errors = append(s.errors, fmt.Sprintf("request %d exposed protected credential outside expected authorization", s.next))
			}
		}
	}
}

func (s *sequentialServer) fail(w http.ResponseWriter, format string, args ...any) {
	s.errors = append(s.errors, fmt.Sprintf(format, args...))
	http.Error(w, "unexpected scenario request", http.StatusInternalServerError)
}

func (s *sequentialServer) verify() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.next != len(s.exchanges) {
		s.errors = append(s.errors, fmt.Sprintf("observed %d requests, want %d", s.next, len(s.exchanges)))
	}
	if len(s.errors) == 0 {
		return nil
	}
	return fmt.Errorf("http conformance failed: %s", strings.Join(s.errors, "; "))
}
