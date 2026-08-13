package output

import (
	"bytes"
	"testing"
)

func TestFileDownloadEnvelopeIsStrictAndMachineWritable(t *testing.T) {
	document, err := NewFileDownloadEnvelope("file-1", "report.pdf", "application/pdf", 42, "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", "/tmp/mm-download/report.pdf", true)
	if err != nil {
		t.Fatal(err)
	}
	var wire bytes.Buffer
	if _, err = WriteMachineJSON(&wire, document); err != nil {
		t.Fatal(err)
	}
	want := `{"schema":"mm/v2/file-download","fileId":"file-1","name":"report.pdf","mimeType":"application/pdf","sizeBytes":42,"sha256":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef","path":"/tmp/mm-download/report.pdf","temporary":true}` + "\n"
	if wire.String() != want {
		t.Fatalf("wire=%q", wire.String())
	}
}

func TestFileDownloadEnvelopeRejectsContradictions(t *testing.T) {
	validHash := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	for name, mutate := range map[string]func(*FileDownloadEnvelope){
		"relative path": func(value *FileDownloadEnvelope) { value.Path = "relative" },
		"bad hash":      func(value *FileDownloadEnvelope) { value.SHA256 = "nope" },
		"negative size": func(value *FileDownloadEnvelope) { value.SizeBytes = -1 },
		"empty name":    func(value *FileDownloadEnvelope) { value.Name = "" },
	} {
		t.Run(name, func(t *testing.T) {
			value := FileDownloadEnvelope{FileID: "file-1", Name: "a", MIMEType: "", SizeBytes: 1, SHA256: validHash, Path: "/tmp/a"}
			mutate(&value)
			if _, err := NewFileDownloadEnvelope(value.FileID, value.Name, value.MIMEType, value.SizeBytes, value.SHA256, value.Path, value.Temporary); err == nil {
				t.Fatal("invalid receipt accepted")
			}
		})
	}
}
