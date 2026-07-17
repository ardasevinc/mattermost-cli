package mattermost

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"unicode/utf8"

	"github.com/ardasevinc/mattermost-cli/internal/api"
)

type FileMutations struct{ client *api.Client }

func NewFileMutations(client *api.Client) *FileMutations { return &FileMutations{client: client} }

// DiscoverMaxUploadBytes returns the public server limit when the client
// configuration exposes a valid MaxFileSize. Absence or retrieval failure is
// intentionally non-fatal because older servers may omit this hint.
func (m *FileMutations) DiscoverMaxUploadBytes(ctx context.Context) (int64, bool) {
	if m == nil || m.client == nil || ctx == nil {
		return 0, false
	}
	config := map[string]string{}
	if m.client.GetPublic(ctx, "/config/client", &config) != nil {
		return 0, false
	}
	raw, ok := config["MaxFileSize"]
	if !ok {
		return 0, false
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		return 0, false
	}
	return value, true
}

type UploadMutationInput struct {
	ChannelID, UserID, Filename, MediaType string
	Length                                 int64
	Body                                   io.ReadCloser
}

type UploadMutationResult struct {
	FileID string `json:"fileId"`
}

type PreparedUpload struct {
	mutation                    *api.PreparedMutation
	channelID, userID, filename string
	length                      int64
}

func (m *FileMutations) PrepareUpload(in UploadMutationInput) (*PreparedUpload, error) {
	if m == nil || m.client == nil || !isSafePostID(in.ChannelID) || !isSafePostID(in.UserID) || !validUploadFilename(in.Filename) ||
		in.MediaType == "" || in.Length <= 0 || in.Body == nil {
		return nil, ErrInvalidMutationRequest
	}
	query := url.Values{}
	query.Set("channel_id", in.ChannelID)
	query.Set("filename", in.Filename)
	prepared, err := m.client.PrepareRawPostStatus("/files?"+query.Encode(), in.MediaType, in.Body, in.Length, http.StatusCreated)
	if err != nil {
		return nil, err
	}
	return &PreparedUpload{prepared, in.ChannelID, in.UserID, in.Filename, in.Length}, nil
}

func (p *PreparedUpload) Execute(ctx context.Context) (UploadMutationResult, error) {
	if p == nil || p.mutation == nil {
		return UploadMutationResult{}, &api.OutcomeUnknownError{}
	}
	var response uploadResponse
	if err := p.mutation.Execute(ctx, &response); err != nil {
		return UploadMutationResult{}, err
	}
	if len(response.FileInfos) != 1 || len(response.ClientIDs) != 0 {
		return UploadMutationResult{}, &api.OutcomeUnknownError{}
	}
	file := response.FileInfos[0]
	if !isSafePostID(file.ID) || file.UserID != p.userID || file.ChannelID != p.channelID || file.PostID != "" || file.Name != p.filename ||
		file.Size != p.length || file.CreateAt <= 0 || file.CreateAt > maxDateMilliseconds || file.UpdateAt < file.CreateAt || file.UpdateAt > maxDateMilliseconds || file.DeleteAt != 0 {
		return UploadMutationResult{}, &api.OutcomeUnknownError{}
	}
	return UploadMutationResult{file.ID}, nil
}

func (p *PreparedUpload) Close() error {
	if p == nil || p.mutation == nil {
		return nil
	}
	return p.mutation.Close()
}

type uploadResponse struct {
	FileInfos []uploadFileInfo
	ClientIDs []string
}

type uploadFileInfo struct {
	ID, UserID, PostID, ChannelID, Name string
	CreateAt, UpdateAt, DeleteAt, Size  int64
}

func (r *uploadResponse) UnmarshalJSON(data []byte) error {
	_, ok := uniqueJSONObject(data)
	if !ok {
		return ErrInvalidPostResponse
	}
	var envelope struct {
		FileInfos json.RawMessage `json:"file_infos"`
		ClientIDs json.RawMessage `json:"client_ids"`
	}
	if json.Unmarshal(data, &envelope) != nil || envelope.FileInfos == nil {
		return ErrInvalidPostResponse
	}
	var infos []json.RawMessage
	if json.Unmarshal(envelope.FileInfos, &infos) != nil || len(infos) != 1 {
		return ErrInvalidPostResponse
	}
	infoRaw, ok := uniqueJSONObject(infos[0])
	if !ok {
		return ErrInvalidPostResponse
	}
	file := uploadFileInfo{}
	var fieldsOK bool
	file.ID, fieldsOK = safePostID(infoRaw["id"])
	if !fieldsOK {
		return ErrInvalidPostResponse
	}
	file.UserID, fieldsOK = safePostID(infoRaw["user_id"])
	if !fieldsOK {
		return ErrInvalidPostResponse
	}
	file.ChannelID, fieldsOK = safePostID(infoRaw["channel_id"])
	if !fieldsOK {
		return ErrInvalidPostResponse
	}
	if rawPostID, present := infoRaw["post_id"]; present {
		file.PostID, fieldsOK = strictString(rawPostID)
		if !fieldsOK {
			return ErrInvalidPostResponse
		}
	}
	file.Name, fieldsOK = strictString(infoRaw["name"])
	if !fieldsOK {
		return ErrInvalidPostResponse
	}
	file.CreateAt, fieldsOK = nonnegativeInteger(infoRaw["create_at"])
	if !fieldsOK {
		return ErrInvalidPostResponse
	}
	file.UpdateAt, fieldsOK = nonnegativeInteger(infoRaw["update_at"])
	if !fieldsOK {
		return ErrInvalidPostResponse
	}
	file.DeleteAt, fieldsOK = nonnegativeInteger(infoRaw["delete_at"])
	if !fieldsOK {
		return ErrInvalidPostResponse
	}
	file.Size, fieldsOK = nonnegativeInteger(infoRaw["size"])
	if !fieldsOK {
		return ErrInvalidPostResponse
	}
	clientIDs := []string(nil)
	if envelope.ClientIDs != nil && string(envelope.ClientIDs) != "null" && json.Unmarshal(envelope.ClientIDs, &clientIDs) != nil {
		return ErrInvalidPostResponse
	}
	*r = uploadResponse{[]uploadFileInfo{file}, clientIDs}
	return nil
}

func validUploadFilename(value string) bool {
	if value == "" || len(value) > 255 || !utf8.ValidString(value) || value == "." || value == ".." {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f || r == '/' || r == '\\' {
			return false
		}
	}
	return true
}

var _ json.Unmarshaler = (*uploadResponse)(nil)
