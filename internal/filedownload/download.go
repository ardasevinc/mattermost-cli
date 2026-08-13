// Package filedownload safely materializes explicitly requested Mattermost files.
package filedownload

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ardasevinc/mattermost-cli/v2/internal/api"
	"github.com/ardasevinc/mattermost-cli/v2/internal/mattermost"
	"github.com/ardasevinc/mattermost-cli/v2/internal/presentation"
)

const (
	DefaultMaxBytes  = int64(512 << 20)
	maxFilenameBytes = 240
)

var (
	ErrDestinationExists = errors.New("download destination already exists")
	ErrFileTooLarge      = errors.New("Mattermost file exceeds the download size limit")
	ErrSizeMismatch      = errors.New("downloaded bytes do not match Mattermost file metadata")
	ErrLocalFile         = errors.New("could not safely create the download destination")
)

type Remote interface {
	Info(context.Context, string) (mattermost.FileInfo, error)
	Download(context.Context, string, io.Writer, int64) (mattermost.FileDownload, error)
}

type Options struct {
	Output       string
	MaxBytes     int64
	Presentation presentation.Options
}

type Result struct {
	FileID, Name, MIMEType, SHA256, Path string
	SizeBytes                            int64
	Temporary                            bool
}

func Download(ctx context.Context, remote Remote, fileID string, options Options) (Result, error) {
	if ctx == nil || remote == nil || options.MaxBytes <= 0 {
		return Result{}, mattermost.ErrInvalidFileRequest
	}
	info, err := remote.Info(ctx, fileID)
	if err != nil {
		return Result{}, err
	}
	if info.Size > options.MaxBytes {
		return Result{}, ErrFileTooLarge
	}
	presented := presentation.PreprocessWithOptions(info.Name, options.Presentation).Text
	name := safeFilename(presented, fileID)
	path, file, temporary, finalize, cleanup, err := createDestination(options.Output, name)
	if err != nil {
		return Result{}, err
	}
	succeeded := false
	defer func() {
		if !succeeded {
			cleanup()
		}
	}()
	hash := sha256.New()
	download, downloadErr := remote.Download(ctx, fileID, io.MultiWriter(file, hash), options.MaxBytes)
	closeErr := file.Close()
	if downloadErr != nil {
		if errors.Is(downloadErr, api.ErrBodyTooLarge) {
			return Result{}, ErrFileTooLarge
		}
		if errors.Is(downloadErr, api.ErrResponseWrite) {
			return Result{}, ErrLocalFile
		}
		return Result{}, downloadErr
	}
	if closeErr != nil {
		return Result{}, ErrLocalFile
	}
	if download.Bytes != info.Size {
		return Result{}, ErrSizeMismatch
	}
	if err := finalize(); err != nil {
		return Result{}, err
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return Result{}, ErrLocalFile
	}
	succeeded = true
	return Result{
		FileID: fileID, Name: name, MIMEType: info.MIMEType, SizeBytes: download.Bytes,
		SHA256: hex.EncodeToString(hash.Sum(nil)), Path: absPath, Temporary: temporary,
	}, nil
}

func createDestination(output, name string) (path string, file *os.File, temporary bool, finalize, cleanup func() error, err error) {
	if output == "" {
		directory, mkdirErr := os.MkdirTemp("", "mm-download-")
		if mkdirErr != nil || os.Chmod(directory, 0o700) != nil {
			if directory != "" {
				_ = os.RemoveAll(directory)
			}
			return "", nil, false, nil, nil, ErrLocalFile
		}
		path = filepath.Join(directory, name)
		file, err = os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			_ = os.RemoveAll(directory)
			return "", nil, false, nil, nil, ErrLocalFile
		}
		return path, file, true, func() error { return nil }, func() error { return os.RemoveAll(directory) }, nil
	}
	absPath, absErr := filepath.Abs(output)
	if absErr != nil {
		return "", nil, false, nil, nil, ErrLocalFile
	}
	if _, statErr := os.Lstat(absPath); statErr == nil {
		return "", nil, false, nil, nil, ErrDestinationExists
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", nil, false, nil, nil, ErrLocalFile
	}
	partial, createErr := os.CreateTemp(filepath.Dir(absPath), ".mm-download-*")
	if createErr != nil || os.Chmod(partial.Name(), 0o600) != nil {
		if partial != nil {
			_ = partial.Close()
			_ = os.Remove(partial.Name())
		}
		return "", nil, false, nil, nil, ErrLocalFile
	}
	partialPath := partial.Name()
	finalize = func() error {
		if linkErr := os.Link(partialPath, absPath); linkErr != nil {
			if errors.Is(linkErr, os.ErrExist) {
				return ErrDestinationExists
			}
			return ErrLocalFile
		}
		if removeErr := os.Remove(partialPath); removeErr != nil {
			_ = os.Remove(absPath)
			return ErrLocalFile
		}
		return nil
	}
	return absPath, partial, false, finalize, func() error { return os.Remove(partialPath) }, nil
}

func safeFilename(name, fileID string) string {
	var safe strings.Builder
	for _, character := range name {
		switch {
		case character == '/' || character == '\\' || character == ':', unicode.IsControl(character), character == utf8.RuneError:
			safe.WriteByte('_')
		default:
			safe.WriteRune(character)
		}
	}
	value := strings.Trim(safe.String(), " .")
	if value == "" {
		value = "download-" + fileID
	}
	if len(value) <= maxFilenameBytes {
		return value
	}
	extension := ""
	if dot := strings.LastIndexByte(value, '.'); dot > 0 && len(value)-dot <= 32 {
		extension = value[dot:]
		value = value[:dot]
	}
	return truncateUTF8(value, maxFilenameBytes-len(extension)) + extension
}

func truncateUTF8(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	for limit > 0 && !utf8.RuneStart(value[limit]) {
		limit--
	}
	return value[:limit]
}
