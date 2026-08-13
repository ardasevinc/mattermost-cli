package output

import (
	"errors"
	"path/filepath"
	"regexp"
	"strings"
)

var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type FileDownloadEnvelope struct {
	Schema    string `json:"schema"`
	FileID    string `json:"fileId"`
	Name      string `json:"name"`
	MIMEType  string `json:"mimeType"`
	SizeBytes int64  `json:"sizeBytes"`
	SHA256    string `json:"sha256"`
	Path      string `json:"path"`
	Temporary bool   `json:"temporary"`
}

func NewFileDownloadEnvelope(fileID, name, mimeType string, sizeBytes int64, sha256, path string, temporary bool) (FileDownloadEnvelope, error) {
	document := FileDownloadEnvelope{
		Schema: "mm/v2/file-download", FileID: fileID, Name: name, MIMEType: mimeType,
		SizeBytes: sizeBytes, SHA256: sha256, Path: path, Temporary: temporary,
	}
	if err := validateFileDownloadEnvelope(document); err != nil {
		return FileDownloadEnvelope{}, errors.New("invalid file download receipt")
	}
	return document, nil
}

func validateFileDownloadEnvelope(document FileDownloadEnvelope) error {
	if document.Schema != "mm/v2/file-download" || strings.TrimSpace(document.FileID) == "" || strings.TrimSpace(document.Name) == "" ||
		document.SizeBytes < 0 || document.SizeBytes > MaxSafeMachineInteger || !sha256Pattern.MatchString(document.SHA256) ||
		!filepath.IsAbs(document.Path) || strings.ContainsAny(document.Name+document.MIMEType+document.Path, "\x00\r\n") {
		return errors.New("invalid file download receipt")
	}
	return nil
}

func (FileDownloadEnvelope) machineDocument() {}
