package output

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestWriteBoundedJSONCanonicalLiterals(t *testing.T) {
	var output bytes.Buffer
	value := struct {
		Text string `json:"text"`
	}{Text: "<>&\u2028\u2029"}
	if _, err := WriteBoundedJSON(&output, value); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "{\"text\":\"<>&\u2028\u2029\"}\n"; got != want {
		t.Fatalf("bytes=%q want=%q", got, want)
	}
}

func TestWriteBoundedJSONOversizeFailsBeforeWrite(t *testing.T) {
	w := &countingWriter{}
	_, err := WriteBoundedJSON(w, struct {
		Text string `json:"text"`
	}{Text: strings.Repeat("x", MaxMachineDocumentBytes)})
	if err == nil || w.calls != 0 || w.bytes != 0 {
		t.Fatalf("err=%v calls=%d bytes=%d", err, w.calls, w.bytes)
	}
}

func TestWriteBoundedJSONShortWriteIsNotRetried(t *testing.T) {
	w := &countingWriter{short: true}
	_, err := WriteBoundedJSON(w, struct {
		OK bool `json:"ok"`
	}{true})
	if err != io.ErrShortWrite || w.calls != 1 {
		t.Fatalf("err=%v calls=%d", err, w.calls)
	}
}

type countingWriter struct {
	calls, bytes int
	short        bool
}

func (w *countingWriter) Write(value []byte) (int, error) {
	w.calls++
	n := len(value)
	if w.short {
		n--
	}
	w.bytes += n
	return n, nil
}
