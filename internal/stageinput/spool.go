package stageinput

import (
	"bytes"
	"context"
	"crypto/sha256"
	"io"
	"os"
	"path/filepath"

	"github.com/ardasevinc/mattermost-cli/internal/stagestore"
)

// Spool is an unlinked private snapshot. It survives only while its descriptor
// is open, so neither success nor process failure can leave plaintext residue.
type Spool struct {
	file           *os.File
	Length         int64
	RemoteFilename string
	MediaType      string
}

func (s *Spool) Read(p []byte) (int, error) {
	if s == nil || s.file == nil {
		return 0, os.ErrClosed
	}
	return s.file.Read(p)
}

func (s *Spool) Close() error {
	if s == nil || s.file == nil {
		return nil
	}
	err := s.file.Close()
	s.file = nil
	return err
}

// ValidateSpoolBudget proves that the complete immutable snapshot set fits
// both the local v2 cap and the currently available private state filesystem.
// A positive serverMax additionally enforces a discovered per-file limit.
func ValidateSpoolBudget(attachments []stagestore.Attachment, directory string, serverMax int64) error {
	if len(attachments) == 0 || len(attachments) > MaxAttachments || !filepath.IsAbs(directory) || filepath.Clean(directory) != directory || serverMax < 0 {
		return ErrInvalid
	}
	var total int64
	for _, attachment := range attachments {
		if attachment.ByteLength <= 0 || serverMax > 0 && attachment.ByteLength > serverMax || attachment.ByteLength > MaxSpoolBytes-total {
			return ErrTooLarge
		}
		total += attachment.ByteLength
	}
	available, err := availableSpoolBytes(directory)
	if err != nil {
		return ErrNoSpoolSpace
	}
	if uint64(total+MinSpoolFreeReserve) > available {
		return ErrNoSpoolSpace
	}
	return nil
}

// Snapshot securely reopens, rescans, rehashes, and copies one stored binding
// into a private immutable-by-convention descriptor for a single upload.
func Snapshot(ctx context.Context, bound stagestore.Attachment, credentials [][]byte, directory string) (*Spool, error) {
	if ctx == nil || !filepath.IsAbs(directory) || filepath.Clean(directory) != directory || bound.FileIdentity == ([32]byte{}) || bound.ByteLength <= 0 {
		return nil, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	scanner, err := newScanner(credentials)
	if err != nil {
		return nil, err
	}
	metadata, err := prepareMetadata(Attachment{Path: bound.CanonicalPath, RemoteFilename: bound.RemoteFilename, MediaType: bound.MediaType})
	if err != nil || metadata.canonical != bound.CanonicalPath || scanner.contains([]byte(bound.SuppliedPath)) || scanner.contains([]byte(bound.CanonicalPath)) ||
		scanner.contains([]byte(bound.RemoteFilename)) || scanner.contains([]byte(bound.MediaType)) {
		return nil, ErrCredential
	}
	source, before, err := openSecure(bound.CanonicalPath)
	if err != nil {
		return nil, ErrFileChanged
	}
	if before.binding() != bound.FileIdentity {
		_ = source.Close()
		return nil, ErrFileChanged
	}

	spool, err := os.CreateTemp(directory, ".mm-spool-")
	if err != nil {
		_ = source.Close()
		return nil, ErrUnsafeFile
	}
	name := spool.Name()
	cleanup := func() {
		_ = source.Close()
		_ = spool.Close()
		_ = os.Remove(name)
	}
	if err = spool.Chmod(0o600); err != nil || os.Remove(name) != nil {
		cleanup()
		return nil, ErrUnsafeFile
	}

	hash := sha256.New()
	stream := scanner.stream()
	buffer := make([]byte, 32*1024)
	var length int64
	for {
		if err = ctx.Err(); err != nil {
			cleanup()
			return nil, err
		}
		n, readErr := source.Read(buffer)
		if n > 0 {
			chunk := buffer[:n]
			length += int64(n)
			_, _ = hash.Write(chunk)
			if stream.write(chunk) {
				cleanup()
				return nil, ErrCredential
			}
			written, writeErr := spool.Write(chunk)
			if writeErr != nil || written != n {
				cleanup()
				return nil, ErrUnsafeFile
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil || n == 0 {
			cleanup()
			return nil, ErrUnsafeFile
		}
	}
	after, statErr := fileIdentityOf(source)
	closeErr := source.Close()
	if statErr != nil || closeErr != nil || !before.stable(after) || length != bound.ByteLength || !bytes.Equal(hash.Sum(nil), bound.ContentDigest[:]) {
		_ = spool.Close()
		return nil, ErrFileChanged
	}
	if err = spool.Sync(); err != nil {
		_ = spool.Close()
		return nil, ErrUnsafeFile
	}
	if _, err = spool.Seek(0, io.SeekStart); err != nil {
		_ = spool.Close()
		return nil, ErrUnsafeFile
	}
	return &Spool{file: spool, Length: length, RemoteFilename: bound.RemoteFilename, MediaType: bound.MediaType}, nil
}

var _ io.ReadCloser = (*Spool)(nil)
