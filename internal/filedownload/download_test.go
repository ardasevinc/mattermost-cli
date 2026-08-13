package filedownload

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ardasevinc/mattermost-cli/v2/internal/mattermost"
	"github.com/ardasevinc/mattermost-cli/v2/internal/presentation"
)

func TestDownloadCreatesPrivateTemporaryFileWithExactReceipt(t *testing.T) {
	payload := []byte{0, 1, 2, 0xff, '\n'}
	remote := &fakeRemote{info: validInfo("../report.bin", int64(len(payload))), payload: payload}
	result, err := Download(context.Background(), remote, "file-1", Options{MaxBytes: DefaultMaxBytes})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(result.Path)) })
	data, err := os.ReadFile(result.Path)
	if err != nil || !bytes.Equal(data, payload) || result.Name != "_report.bin" || !result.Temporary || result.SizeBytes != int64(len(payload)) {
		t.Fatalf("result=%+v exact=%v err=%v", result, bytes.Equal(data, payload), err)
	}
	wantHash := sha256.Sum256(payload)
	if result.SHA256 != hex.EncodeToString(wantHash[:]) {
		t.Fatalf("sha256=%q", result.SHA256)
	}
	directoryInfo, _ := os.Stat(filepath.Dir(result.Path))
	fileInfo, _ := os.Stat(result.Path)
	if directoryInfo.Mode().Perm() != 0o700 || fileInfo.Mode().Perm() != 0o600 {
		t.Fatalf("directory=%o file=%o", directoryInfo.Mode().Perm(), fileInfo.Mode().Perm())
	}
}

func TestDownloadUsesExactOutputAndRefusesCollision(t *testing.T) {
	payload := []byte("hello")
	output := filepath.Join(t.TempDir(), "chosen.txt")
	remote := &fakeRemote{info: validInfo("remote.txt", int64(len(payload))), payload: payload}
	result, err := Download(context.Background(), remote, "file-1", Options{Output: output, MaxBytes: 10})
	if err != nil || result.Path != output || result.Temporary {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if _, err = Download(context.Background(), remote, "file-1", Options{Output: output, MaxBytes: 10}); !errors.Is(err, ErrDestinationExists) {
		t.Fatalf("collision error=%v", err)
	}
	if data, readErr := os.ReadFile(output); readErr != nil || string(data) != "hello" {
		t.Fatalf("data=%q err=%v", data, readErr)
	}
}

func TestDownloadRejectsMetadataLimitBeforeCreatingDestination(t *testing.T) {
	output := filepath.Join(t.TempDir(), "must-not-exist")
	remote := &fakeRemote{info: validInfo("large.bin", 11), payload: bytes.Repeat([]byte{'x'}, 11)}
	_, err := Download(context.Background(), remote, "file-1", Options{Output: output, MaxBytes: 10})
	if !errors.Is(err, ErrFileTooLarge) || remote.downloads != 0 {
		t.Fatalf("downloads=%d err=%v", remote.downloads, err)
	}
	if _, statErr := os.Stat(output); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("destination exists: %v", statErr)
	}
}

func TestDownloadCleansFailedAndMismatchedDestinations(t *testing.T) {
	for name, remote := range map[string]*fakeRemote{
		"remote failure": {info: validInfo("a.bin", 3), err: errors.New("failed")},
		"size mismatch":  {info: validInfo("a.bin", 4), payload: []byte("abc")},
	} {
		t.Run(name, func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "result.bin")
			_, err := Download(context.Background(), remote, "file-1", Options{Output: output, MaxBytes: 10})
			if err == nil {
				t.Fatal("download succeeded")
			}
			if _, statErr := os.Stat(output); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("partial destination remains: %v", statErr)
			}
		})
	}
}

func TestDownloadRedactsCredentialFromPersistedFilenameButNotBytes(t *testing.T) {
	token := "active-secret-token"
	payload := []byte("opaque bytes include " + token)
	remote := &fakeRemote{info: validInfo("report-"+token+".txt", int64(len(payload))), payload: payload}
	result, err := Download(context.Background(), remote, "file-1", Options{MaxBytes: 100, Presentation: presentation.Options{Credentials: []string{token}, DisableHeuristics: true}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(result.Path)) })
	data, _ := os.ReadFile(result.Path)
	if strings.Contains(result.Name, token) || strings.Contains(result.Path, token) || !bytes.Equal(data, payload) {
		t.Fatalf("result=%+v bytes=%q", result, data)
	}
}

func TestSafeFilenameBoundsUnicodeAndPreservesExtension(t *testing.T) {
	name := strings.Repeat("界", 100) + ".pdf"
	got := safeFilename(name, "id")
	if len(got) > maxFilenameBytes || !strings.HasSuffix(got, ".pdf") || !strings.Contains(got, "界") {
		t.Fatalf("filename bytes=%d value=%q", len(got), got)
	}
}

type fakeRemote struct {
	info      mattermost.FileInfo
	payload   []byte
	err       error
	downloads int
}

func validInfo(name string, size int64) mattermost.FileInfo {
	return mattermost.FileInfo{ID: "file-1", UserID: "user-1", ChannelID: "channel-1", Name: name, MIMEType: "application/octet-stream", Size: size, CreateAt: 1, UpdateAt: 1}
}

func (r *fakeRemote) Info(context.Context, string) (mattermost.FileInfo, error) { return r.info, nil }

func (r *fakeRemote) Download(_ context.Context, _ string, destination io.Writer, _ int64) (mattermost.FileDownload, error) {
	r.downloads++
	if r.err != nil {
		return mattermost.FileDownload{}, r.err
	}
	written, err := destination.Write(r.payload)
	return mattermost.FileDownload{Bytes: int64(written), ContentType: r.info.MIMEType}, err
}
