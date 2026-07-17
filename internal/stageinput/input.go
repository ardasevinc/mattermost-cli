// Package stageinput validates and binds untrusted attachment inputs before they
// are admitted to the stage store.
package stageinput

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ardasevinc/mattermost-cli/v2/internal/stagestore"
	"golang.org/x/text/unicode/norm"
)

const (
	MaxAttachments      = 5
	MaxSpoolBytes       = int64(512 << 20)
	MinSpoolFreeReserve = int64(64 << 20)
	maxPathBytes        = 4096
	maxFilenameBytes    = 255
	maxMediaTypeBytes   = 255
	maxCredentialCount  = 64
	maxCredentialBytes  = 4096
	maxCredentialsBytes = 64 << 10
)

var (
	ErrInvalid       = errors.New("stage input: invalid attachment")
	ErrCredential    = errors.New("stage input: protected credential present")
	ErrUnsafeFile    = errors.New("stage input: unsafe attachment file")
	ErrFileChanged   = errors.New("stage input: attachment changed while binding")
	ErrUnsupported   = errors.New("stage input: secure attachment binding unsupported on this platform")
	ErrTooMany       = errors.New("stage input: too many attachments")
	ErrTooLarge      = errors.New("stage input: attachment spool budget exceeded")
	ErrNoSpoolSpace  = errors.New("stage input: insufficient private spool space")
	ErrCredentialSet = errors.New("stage input: invalid protected credential set")
)

type Attachment struct {
	Path           string
	RemoteFilename string // empty derives the caller path's safe basename
	MediaType      string // empty detects MIME from the first 512 file bytes
}

type MetadataIntent struct {
	Path           string  `json:"path"`
	RemoteFilename string  `json:"remoteFilename"`
	MediaType      *string `json:"mediaType"`
}

// Preflight validates and canonicalizes caller-supplied attachment metadata
// without opening or reading the referenced files.
func Preflight(inputs []Attachment) ([]MetadataIntent, error) {
	if len(inputs) > MaxAttachments {
		return nil, ErrTooMany
	}
	result := make([]MetadataIntent, 0, len(inputs))
	for _, input := range inputs {
		prepared, err := prepareMetadata(input)
		if err != nil {
			return nil, err
		}
		var mediaType *string
		if input.MediaType != "" {
			value := prepared.mediaType
			mediaType = &value
		}
		result = append(result, MetadataIntent{prepared.canonical, prepared.filename, mediaType})
	}
	return result, nil
}

// Bind records a durable-at-rest snapshot only. A later apply must securely
// reopen the canonical path, rescan credentials, rehash, and spool before any
// upload; a changed path must conflict with this recorded identity and digest.
// Bind returns no partial result and never returns contaminated values.
func Bind(ctx context.Context, inputs []Attachment, credentials [][]byte) ([]stagestore.Attachment, error) {
	if ctx == nil {
		return nil, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(inputs) > MaxAttachments {
		return nil, ErrTooMany
	}
	scanner, err := newScanner(credentials)
	if err != nil {
		return nil, err
	}
	prepared := make([]preparedAttachment, len(inputs))
	for i, input := range inputs {
		prepared[i], err = prepareMetadata(input)
		if err != nil {
			return nil, err
		}
		if scanner.contains([]byte(input.Path)) || scanner.contains([]byte(input.RemoteFilename)) ||
			scanner.contains([]byte(input.MediaType)) || scanner.contains([]byte(prepared[i].canonical)) ||
			scanner.contains([]byte(prepared[i].filename)) || scanner.contains([]byte(prepared[i].mediaType)) {
			return nil, ErrCredential
		}
	}
	bound := make([]stagestore.Attachment, 0, len(prepared))
	for _, input := range prepared {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		file, before, err := openSecure(input.canonical)
		if err != nil {
			return nil, err
		}
		digest, length, prefix, scanErr := scanFile(ctx, file, scanner.stream())
		after, statErr := fileIdentityOf(file)
		closeErr := file.Close()
		if scanErr != nil {
			return nil, scanErr
		}
		if length == 0 {
			return nil, ErrInvalid
		}
		if statErr != nil || closeErr != nil {
			return nil, ErrUnsafeFile
		}
		if !before.stable(after) || length != before.size {
			return nil, ErrFileChanged
		}
		mediaType := input.mediaType
		if mediaType == "" {
			mediaType = http.DetectContentType(prefix)
			if scanner.contains([]byte(mediaType)) {
				return nil, ErrCredential
			}
		}
		reopened, reopenedID, err := openSecure(input.canonical)
		if err != nil {
			return nil, ErrFileChanged
		}
		reopenCloseErr := reopened.Close()
		if reopenCloseErr != nil || !before.stable(reopenedID) {
			return nil, ErrFileChanged
		}
		bound = append(bound, stagestore.Attachment{SuppliedPath: input.supplied, CanonicalPath: input.canonical,
			RemoteFilename: input.filename, ByteLength: length, MediaType: mediaType, ContentDigest: digest,
			FileIdentity: before.binding()})
	}
	return bound, nil
}

type preparedAttachment struct{ supplied, canonical, filename, mediaType string }

func prepareMetadata(input Attachment) (preparedAttachment, error) {
	if !validText(input.Path, maxPathBytes) || strings.TrimSpace(input.Path) != input.Path {
		return preparedAttachment{}, ErrInvalid
	}
	absolute, err := filepath.Abs(input.Path)
	if err != nil {
		return preparedAttachment{}, ErrInvalid
	}
	canonical := filepath.Clean(absolute)
	if !validText(canonical, maxPathBytes) {
		return preparedAttachment{}, ErrInvalid
	}
	filename := input.RemoteFilename
	if filename == "" {
		filename = filepath.Base(canonical)
	}
	filename = norm.NFC.String(filename)
	if !validText(filename, maxFilenameBytes) || strings.TrimSpace(filename) != filename || filename == "." || filename == ".." || strings.ContainsAny(filename, `/\`) || filepath.Base(filename) != filename {
		return preparedAttachment{}, ErrInvalid
	}
	mediaType := input.MediaType
	if mediaType != "" {
		if !validASCII(mediaType, maxMediaTypeBytes) {
			return preparedAttachment{}, ErrInvalid
		}
		parsed, params, err := mime.ParseMediaType(mediaType)
		if err != nil || parsed == "" {
			return preparedAttachment{}, ErrInvalid
		}
		mediaType = mime.FormatMediaType(parsed, params)
		if !validASCII(mediaType, maxMediaTypeBytes) {
			return preparedAttachment{}, ErrInvalid
		}
	}
	return preparedAttachment{input.Path, canonical, filename, mediaType}, nil
}

func validText(value string, maximum int) bool {
	if value == "" || len(value) > maximum || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func validASCII(value string, maximum int) bool {
	if len(value) > maximum {
		return false
	}
	for i := range len(value) {
		if value[i] < 0x20 || value[i] > 0x7e {
			return false
		}
	}
	return true
}

func scanFile(ctx context.Context, input io.Reader, scanner *streamScanner) ([32]byte, int64, []byte, error) {
	hash := sha256.New()
	buf := make([]byte, 32*1024)
	prefix := make([]byte, 0, 512)
	var length int64
	for {
		if err := ctx.Err(); err != nil {
			return [32]byte{}, 0, nil, err
		}
		n, err := input.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			length += int64(n)
			_, _ = hash.Write(chunk)
			if len(prefix) < cap(prefix) {
				prefix = append(prefix, chunk[:min(len(chunk), cap(prefix)-len(prefix))]...)
			}
			if scanner.write(chunk) {
				return [32]byte{}, 0, nil, ErrCredential
			}
		}
		if err == io.EOF {
			if contextErr := ctx.Err(); contextErr != nil {
				return [32]byte{}, 0, nil, contextErr
			}
			var digest [32]byte
			copy(digest[:], hash.Sum(nil))
			return digest, length, prefix, nil
		}
		if err != nil || n == 0 {
			return [32]byte{}, 0, nil, ErrUnsafeFile
		}
	}
}

type tokenPattern struct {
	value   []byte
	failure []int
}
type tokenScanner struct{ patterns []tokenPattern }
type streamScanner struct {
	patterns []tokenPattern
	states   []int
}

func newScanner(tokens [][]byte) (tokenScanner, error) {
	if len(tokens) > maxCredentialCount {
		return tokenScanner{}, ErrCredentialSet
	}
	s := tokenScanner{patterns: make([]tokenPattern, 0, len(tokens))}
	total := 0
	for _, token := range tokens {
		if len(token) == 0 {
			continue
		}
		total += len(token)
		if len(token) > maxCredentialBytes || total > maxCredentialsBytes {
			return tokenScanner{}, ErrCredentialSet
		}
		value := bytes.Clone(token)
		failure := make([]int, len(value))
		for i, j := 1, 0; i < len(value); i++ {
			for j > 0 && value[i] != value[j] {
				j = failure[j-1]
			}
			if value[i] == value[j] {
				j++
			}
			failure[i] = j
		}
		s.patterns = append(s.patterns, tokenPattern{value, failure})
	}
	return s, nil
}

func (s tokenScanner) contains(value []byte) bool {
	for _, pattern := range s.patterns {
		if bytes.Contains(value, pattern.value) {
			return true
		}
	}
	return false
}
func (s tokenScanner) stream() *streamScanner {
	return &streamScanner{s.patterns, make([]int, len(s.patterns))}
}
func (s *streamScanner) write(chunk []byte) bool {
	for _, b := range chunk {
		for i, pattern := range s.patterns {
			state := s.states[i]
			for state > 0 && b != pattern.value[state] {
				state = pattern.failure[state-1]
			}
			if b == pattern.value[state] {
				state++
			}
			if state == len(pattern.value) {
				return true
			}
			s.states[i] = state
		}
	}
	return false
}
