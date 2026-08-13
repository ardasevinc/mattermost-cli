package mattermost

import (
	"context"
	"errors"
	"io"
	"net/url"

	"github.com/ardasevinc/mattermost-cli/v2/internal/api"
)

var (
	ErrInvalidFileRequest = errors.New("invalid Mattermost file request")
	ErrInvalidFileBinding = errors.New("Mattermost returned inconsistent file metadata")
)

type Files struct{ client *api.Client }

type FileDownload struct {
	Bytes       int64
	ContentType string
}

func NewFiles(client *api.Client) *Files { return &Files{client: client} }

func (f *Files) Info(ctx context.Context, fileID string) (FileInfo, error) {
	if f == nil || f.client == nil || ctx == nil || !isSafePostID(fileID) {
		return FileInfo{}, ErrInvalidFileRequest
	}
	var response fileInfoResponse
	if err := f.client.Get(ctx, "/files/"+url.PathEscape(fileID)+"/info", &response); err != nil {
		return FileInfo{}, err
	}
	file := FileInfo(response)
	if file.ID != fileID || file.Name == "" || file.CreateAt <= 0 || file.CreateAt > maxDateMilliseconds ||
		file.UpdateAt < file.CreateAt || file.UpdateAt > maxDateMilliseconds || file.DeleteAt != 0 {
		return FileInfo{}, ErrInvalidFileBinding
	}
	return file, nil
}

func (f *Files) Download(ctx context.Context, fileID string, destination io.Writer, limit int64) (FileDownload, error) {
	if f == nil || f.client == nil || ctx == nil || !isSafePostID(fileID) || destination == nil || limit <= 0 {
		return FileDownload{}, ErrInvalidFileRequest
	}
	result, err := f.client.Download(ctx, "/files/"+url.PathEscape(fileID), destination, limit)
	return FileDownload{Bytes: result.Bytes, ContentType: result.ContentType}, err
}
