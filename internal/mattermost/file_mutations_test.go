package mattermost

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ardasevinc/mattermost-cli/v2/internal/api"
)

func TestUploadMutationSendsExactRawFileAndValidatesIdentity(t *testing.T) {
	payload := []byte{0, 1, 2, 0xff, '\n'}
	var calls atomic.Int32
	client := mutationClient(t, mutationRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		if request.URL.RequestURI() != "/api/v4/files?channel_id=channel-1&filename=report.bin" || request.Header.Get("Content-Type") != "application/octet-stream" || request.ContentLength != int64(len(payload)) || request.GetBody != nil {
			t.Fatalf("request=%s type=%q length=%d replay=%v", request.URL.RequestURI(), request.Header.Get("Content-Type"), request.ContentLength, request.GetBody != nil)
		}
		got, _ := io.ReadAll(request.Body)
		if !bytes.Equal(got, payload) {
			t.Fatalf("body=%x", got)
		}
		response := `{"file_infos":[{"id":"file-1","user_id":"user-1","channel_id":"channel-1","create_at":100,"update_at":100,"delete_at":0,"name":"report.bin","size":5,"mime_type":"application/octet-stream"}]}`
		return mutationResponse(http.StatusCreated, response), nil
	}))
	prepared, err := NewFileMutations(client).PrepareUpload(UploadMutationInput{
		ChannelID: "channel-1", UserID: "user-1", Filename: "report.bin", MediaType: "application/octet-stream", Length: int64(len(payload)), Body: io.NopCloser(bytes.NewReader(payload)),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := prepared.Execute(context.Background())
	if err != nil || result.FileID != "file-1" || calls.Load() != 1 {
		t.Fatalf("result=%+v calls=%d err=%v", result, calls.Load(), err)
	}
}

func TestUploadMutationRejectsUnvalidatedSuccessAndBadInput(t *testing.T) {
	valid := `{"file_infos":[{"id":"file-1","user_id":"user-1","channel_id":"channel-1","create_at":100,"update_at":100,"delete_at":0,"name":"a.txt","size":1}]}`
	for name, response := range map[string]string{
		"wrong user":    strings.Replace(valid, `"user-1"`, `"other"`, 1),
		"wrong channel": strings.Replace(valid, `"channel-1"`, `"other"`, 1),
		"wrong name":    strings.Replace(valid, `"a.txt"`, `"b.txt"`, 1),
		"wrong size":    strings.Replace(valid, `"size":1`, `"size":2`, 1),
		"two files":     strings.Replace(valid, `]}`, `,{}]}`, 1),
		"client id":     strings.Replace(valid, `]}`, `],"client_ids":["x"]}`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			client := mutationClient(t, mutationRoundTripFunc(func(*http.Request) (*http.Response, error) {
				return mutationResponse(http.StatusCreated, response), nil
			}))
			prepared, err := NewFileMutations(client).PrepareUpload(UploadMutationInput{"channel-1", "user-1", "a.txt", "text/plain", 1, io.NopCloser(strings.NewReader("x"))})
			if err != nil {
				t.Fatal(err)
			}
			_, err = prepared.Execute(context.Background())
			var unknown *api.OutcomeUnknownError
			if !errors.As(err, &unknown) {
				t.Fatalf("error=%v", err)
			}
		})
	}
	if _, err := NewFileMutations(nil).PrepareUpload(UploadMutationInput{}); !errors.Is(err, ErrInvalidMutationRequest) {
		t.Fatalf("invalid=%v", err)
	}
}

func TestPreparedUploadCloseDoesNotDispatch(t *testing.T) {
	var calls atomic.Int32
	client := mutationClient(t, mutationRoundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("unexpected")
	}))
	prepared, err := NewFileMutations(client).PrepareUpload(UploadMutationInput{"channel-1", "user-1", "a.txt", "text/plain", 1, io.NopCloser(strings.NewReader("x"))})
	if err != nil {
		t.Fatal(err)
	}
	if err = prepared.Close(); err != nil || calls.Load() != 0 {
		t.Fatalf("calls=%d err=%v", calls.Load(), err)
	}
}

func TestValidateUploadReadsExactRemoteBindingWithoutMutation(t *testing.T) {
	var calls atomic.Int32
	client := mutationClient(t, mutationRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		if request.Method != http.MethodGet || request.URL.RequestURI() != "/api/v4/files/file-1/info" || request.ContentLength != 0 {
			t.Fatalf("method=%s uri=%s body=%v", request.Method, request.URL.RequestURI(), request.Body)
		}
		return mutationResponse(http.StatusOK, `{"id":"file-1","user_id":"user-1","channel_id":"channel-1","post_id":"","create_at":100,"update_at":100,"delete_at":0,"name":"a.txt","size":1}`), nil
	}))
	err := NewFileMutations(client).ValidateUpload(context.Background(), UploadMutationInput{ChannelID: "channel-1", UserID: "user-1", Filename: "a.txt", Length: 1}, "file-1")
	if err != nil || calls.Load() != 1 {
		t.Fatalf("calls=%d err=%v", calls.Load(), err)
	}
}

func TestValidateUploadRejectsChangedOrUnparseableRemoteBinding(t *testing.T) {
	valid := `{"id":"file-1","user_id":"user-1","channel_id":"channel-1","post_id":"","create_at":100,"update_at":100,"delete_at":0,"name":"a.txt","size":1}`
	for name, response := range map[string]string{
		"wrong id":      strings.Replace(valid, `"file-1"`, `"file-2"`, 1),
		"wrong user":    strings.Replace(valid, `"user-1"`, `"user-2"`, 1),
		"wrong channel": strings.Replace(valid, `"channel-1"`, `"channel-2"`, 1),
		"attached":      strings.Replace(valid, `"post_id":""`, `"post_id":"post-1"`, 1),
		"wrong name":    strings.Replace(valid, `"a.txt"`, `"b.txt"`, 1),
		"wrong size":    strings.Replace(valid, `"size":1`, `"size":2`, 1),
		"deleted":       strings.Replace(valid, `"delete_at":0`, `"delete_at":101`, 1),
		"stale update":  strings.Replace(valid, `"update_at":100`, `"update_at":99`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			client := mutationClient(t, mutationRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				if request.Method != http.MethodGet {
					t.Fatalf("method=%s", request.Method)
				}
				return mutationResponse(http.StatusOK, response), nil
			}))
			err := NewFileMutations(client).ValidateUpload(context.Background(), UploadMutationInput{ChannelID: "channel-1", UserID: "user-1", Filename: "a.txt", Length: 1}, "file-1")
			if !errors.Is(err, ErrUploadBinding) {
				t.Fatalf("error=%v", err)
			}
		})
	}
	for name, response := range map[string]string{
		"malformed": `{`,
		"duplicate": strings.Replace(valid, `"id":"file-1"`, `"id":"file-1","id":"file-1"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			client := mutationClient(t, mutationRoundTripFunc(func(*http.Request) (*http.Response, error) {
				return mutationResponse(http.StatusOK, response), nil
			}))
			if err := NewFileMutations(client).ValidateUpload(context.Background(), UploadMutationInput{ChannelID: "channel-1", UserID: "user-1", Filename: "a.txt", Length: 1}, "file-1"); !errors.Is(err, api.ErrInvalidJSON) {
				t.Fatalf("error=%v", err)
			}
		})
	}
	client := mutationClient(t, mutationRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return mutationResponse(http.StatusNotFound, `{}`), nil
	}))
	if err := NewFileMutations(client).ValidateUpload(context.Background(), UploadMutationInput{ChannelID: "channel-1", UserID: "user-1", Filename: "a.txt", Length: 1}, "file-1"); err == nil || errors.Is(err, ErrUploadBinding) {
		t.Fatalf("404 error=%v", err)
	}
}

func TestDiscoverMaxUploadBytesUsesOnlyValidPublicHint(t *testing.T) {
	for name, response := range map[string]string{
		"valid":   `{"MaxFileSize":"12345"}`,
		"missing": `{"Version":"10.0"}`,
		"invalid": `{"MaxFileSize":"unbounded"}`,
	} {
		t.Run(name, func(t *testing.T) {
			client := mutationClient(t, mutationRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				if request.URL.RequestURI() != "/api/v4/config/client" || request.Header.Get("Authorization") != "" {
					t.Fatalf("uri=%s authorization=%q", request.URL.RequestURI(), request.Header.Get("Authorization"))
				}
				return mutationResponse(http.StatusOK, response), nil
			}))
			value, found := NewFileMutations(client).DiscoverMaxUploadBytes(context.Background())
			if name == "valid" && (!found || value != 12345) || name != "valid" && (found || value != 0) {
				t.Fatalf("value=%d found=%v", value, found)
			}
		})
	}
}
