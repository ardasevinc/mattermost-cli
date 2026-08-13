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

func TestFilesReadMetadataAndExactBytes(t *testing.T) {
	payload := []byte{0, 1, 2, 0xff, '\n'}
	var calls atomic.Int32
	client := mutationClient(t, mutationRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		switch request.URL.RequestURI() {
		case "/api/v4/files/file-1/info":
			return mutationResponse(http.StatusOK, `{"id":"file-1","user_id":"user-1","channel_id":"channel-1","post_id":"post-1","create_at":100,"update_at":101,"delete_at":0,"name":"report.bin","size":5,"mime_type":"application/octet-stream"}`), nil
		case "/api/v4/files/file-1":
			response := mutationResponse(http.StatusOK, string(payload))
			response.Header.Set("Content-Type", "application/octet-stream")
			return response, nil
		default:
			t.Fatalf("unexpected request %s", request.URL.RequestURI())
			return nil, nil
		}
	}))
	files := NewFiles(client)
	info, err := files.Info(context.Background(), "file-1")
	if err != nil || info.Name != "report.bin" || info.MIMEType != "application/octet-stream" || info.Size != int64(len(payload)) || info.PostID != "post-1" {
		t.Fatalf("info=%+v err=%v", info, err)
	}
	var destination bytes.Buffer
	result, err := files.Download(context.Background(), "file-1", &destination, 10)
	if err != nil || result.Bytes != int64(len(payload)) || result.ContentType != "application/octet-stream" || !bytes.Equal(destination.Bytes(), payload) || calls.Load() != 2 {
		t.Fatalf("result=%+v exact=%v calls=%d err=%v", result, bytes.Equal(destination.Bytes(), payload), calls.Load(), err)
	}
}

func TestFilesRejectInvalidInputAndMetadataBinding(t *testing.T) {
	client := mutationClient(t, mutationRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return mutationResponse(http.StatusOK, `{"id":"different","user_id":"user-1","channel_id":"channel-1","create_at":100,"update_at":100,"delete_at":0,"name":"a.txt","size":1}`), nil
	}))
	files := NewFiles(client)
	if _, err := files.Info(context.Background(), "file-1"); !errors.Is(err, ErrInvalidFileBinding) {
		t.Fatalf("binding error=%v", err)
	}
	for _, fileID := range []string{"", "../file", strings.Repeat("x", maxPostIDLength+1)} {
		if _, err := files.Info(context.Background(), fileID); !errors.Is(err, ErrInvalidFileRequest) {
			t.Fatalf("Info(%q) error=%v", fileID, err)
		}
	}
	if _, err := NewFiles(nil).Download(context.Background(), "file-1", io.Discard, 1); !errors.Is(err, ErrInvalidFileRequest) {
		t.Fatalf("download error=%v", err)
	}
}

func TestFilesPreserveSafeRemoteFailures(t *testing.T) {
	client := mutationClient(t, mutationRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return mutationResponse(http.StatusForbidden, "remote token"), nil
	}))
	_, err := NewFiles(client).Info(context.Background(), "file-1")
	var remote *api.APIError
	if !errors.As(err, &remote) || remote.Status != http.StatusForbidden || strings.Contains(err.Error(), "remote token") {
		t.Fatalf("error=%v", err)
	}
}
