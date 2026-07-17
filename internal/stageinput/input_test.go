package stageinput

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ardasevinc/mattermost-cli/v2/internal/stagestore"
)

func TestTokenScannerEveryChunkBoundary(t *testing.T) {
	token := []byte("active-token")
	content := append(append([]byte("prefix:"), token...), []byte(":suffix")...)
	for split := 0; split <= len(content); split++ {
		scanner := mustScanner(t, [][]byte{token, []byte("another")}).stream()
		if found := scanner.write(content[:split]) || scanner.write(content[split:]); !found {
			t.Fatalf("split %d was not detected", split)
		}
	}
}

func TestScanFileBinaryAndCredential(t *testing.T) {
	binary := append([]byte{0, 0xff, 1, 2}, bytes.Repeat([]byte{0x81, 0}, 20_000)...)
	digest, length, _, err := scanFile(context.Background(), &oneByteReader{data: binary}, mustScanner(t, [][]byte{[]byte("absent")}).stream())
	if err != nil || length != int64(len(binary)) || digest != sha256.Sum256(binary) {
		t.Fatalf("digest=%x length=%d err=%v", digest, length, err)
	}
	contaminated := append(bytes.Clone(binary), []byte("secret")...)
	if _, _, _, err := scanFile(context.Background(), &oneByteReader{data: contaminated}, mustScanner(t, [][]byte{[]byte("secret")}).stream()); !errors.Is(err, ErrCredential) {
		t.Fatalf("error=%v", err)
	}
}

func TestBindCapturesMetadataAndRejectsMetadataCredential(t *testing.T) {
	path := filepath.Join(localTempDir(t), "payload.bin")
	content := []byte{0, 1, 2, 0xff}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Bind(context.Background(), []Attachment{{Path: path, RemoteFilename: "payload.bin", MediaType: "application/octet-stream"}}, [][]byte{[]byte("active-token")})
	if err != nil || len(got) != 1 {
		t.Fatalf("attachments=%+v err=%v", got, err)
	}
	if got[0].SuppliedPath != path || got[0].CanonicalPath != filepath.Clean(path) || got[0].RemoteFilename != "payload.bin" || got[0].MediaType != "application/octet-stream" || got[0].ByteLength != 4 || got[0].ContentDigest != sha256.Sum256(content) {
		t.Fatalf("attachment=%+v", got[0])
	}
	for _, input := range []Attachment{
		{Path: path, RemoteFilename: "active-token.bin"},
		{Path: path, RemoteFilename: "x", MediaType: "x/active-token"},
	} {
		if result, err := Bind(context.Background(), []Attachment{input}, [][]byte{[]byte("active-token")}); !errors.Is(err, ErrCredential) || result != nil {
			t.Fatalf("result=%v error=%v", result, err)
		}
	}
}

func TestBindRejectsEmptyAndMoreThanFiveAttachments(t *testing.T) {
	dir := localTempDir(t)
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if result, err := Bind(context.Background(), []Attachment{{Path: empty}}, nil); !errors.Is(err, ErrInvalid) || result != nil {
		t.Fatalf("empty result=%v err=%v", result, err)
	}
	inputs := make([]Attachment, MaxAttachments+1)
	for i := range inputs {
		inputs[i] = Attachment{Path: empty}
	}
	if result, err := Bind(context.Background(), inputs, nil); !errors.Is(err, ErrTooMany) || result != nil {
		t.Fatalf("too many result=%v err=%v", result, err)
	}
}

func TestValidateSpoolBudgetChecksServerAggregateAndFreeSpace(t *testing.T) {
	directory := localTempDir(t)
	attachment := func(length int64) stagestore.Attachment {
		return stagestore.Attachment{ByteLength: length}
	}
	if err := ValidateSpoolBudget([]stagestore.Attachment{attachment(1), attachment(2)}, directory, 2); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSpoolBudget([]stagestore.Attachment{attachment(3)}, directory, 2); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("server limit error=%v", err)
	}
	if err := ValidateSpoolBudget([]stagestore.Attachment{attachment(MaxSpoolBytes), attachment(1)}, directory, 0); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("aggregate error=%v", err)
	}
	if err := ValidateSpoolBudget([]stagestore.Attachment{attachment(1)}, filepath.Join(directory, "missing"), 0); !errors.Is(err, ErrNoSpoolSpace) {
		t.Fatalf("space error=%v", err)
	}
}

func TestBindDerivesSafeFilenameAndMediaType(t *testing.T) {
	path := filepath.Join(localTempDir(t), "note.txt")
	if err := os.WriteFile(path, []byte("plain text\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Bind(context.Background(), []Attachment{{Path: path}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].RemoteFilename != "note.txt" || got[0].MediaType != "text/plain; charset=utf-8" {
		t.Fatalf("derived metadata=%+v", got[0])
	}
}

func TestBindValidatesAllMetadataBeforeFilesystemIO(t *testing.T) {
	missing := filepath.Join(localTempDir(t), "missing")
	for name, input := range map[string]Attachment{
		"path control":        {Path: missing + "\n"},
		"path whitespace":     {Path: " " + missing},
		"path length":         {Path: strings.Repeat("a", maxPathBytes+1)},
		"filename traversal":  {Path: missing, RemoteFilename: "../x"},
		"filename control":    {Path: missing, RemoteFilename: "x\n"},
		"filename whitespace": {Path: missing, RemoteFilename: " x"},
		"filename length":     {Path: missing, RemoteFilename: strings.Repeat("a", maxFilenameBytes+1)},
		"media invalid":       {Path: missing, MediaType: "not a mime"},
		"media control":       {Path: missing, MediaType: "text/plain\n"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Bind(context.Background(), []Attachment{input}, nil); !errors.Is(err, ErrInvalid) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestBindCanonicalizesMediaType(t *testing.T) {
	path := filepath.Join(localTempDir(t), "file")
	if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Bind(context.Background(), []Attachment{{Path: path, MediaType: `Text/Plain; Charset="UTF-8"`}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].MediaType != "text/plain; charset=UTF-8" {
		t.Fatalf("media type=%q", got[0].MediaType)
	}
}

func TestSnapshotRevalidatesExactBindingAndLeavesNoResidue(t *testing.T) {
	dir := localTempDir(t)
	path := filepath.Join(dir, "payload.bin")
	content := append(bytes.Repeat([]byte("chunk-"), 9000), []byte("tail")...)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	bound, err := Bind(context.Background(), []Attachment{{Path: path}}, [][]byte{[]byte("absent-token")})
	if err != nil || bound[0].FileIdentity == ([32]byte{}) {
		t.Fatalf("bound=%+v err=%v", bound, err)
	}
	spool, err := Snapshot(context.Background(), bound[0], [][]byte{[]byte("absent-token")}, dir)
	if err != nil {
		t.Fatal(err)
	}
	got, readErr := io.ReadAll(spool)
	if readErr != nil || !bytes.Equal(got, content) || spool.Length != int64(len(content)) || spool.RemoteFilename != "payload.bin" {
		t.Fatalf("length=%d name=%q read=%v exact=%v", spool.Length, spool.RemoteFilename, readErr, bytes.Equal(got, content))
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "payload.bin" {
		t.Fatalf("spool residue=%v err=%v", entries, err)
	}
	if err = spool.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotRejectsReplacementDriftAndCredentialBytes(t *testing.T) {
	for name, mutate := range map[string]func(string) error{
		"replacement same bytes": func(path string) error {
			replacement := path + ".new"
			if err := os.WriteFile(replacement, []byte("original"), 0o600); err != nil {
				return err
			}
			return os.Rename(replacement, path)
		},
		"changed bytes": func(path string) error { return os.WriteFile(path, []byte("different"), 0o600) },
	} {
		t.Run(name, func(t *testing.T) {
			dir := localTempDir(t)
			path := filepath.Join(dir, "payload")
			if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
				t.Fatal(err)
			}
			bound, err := Bind(context.Background(), []Attachment{{Path: path}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err = mutate(path); err != nil {
				t.Fatal(err)
			}
			if spool, snapshotErr := Snapshot(context.Background(), bound[0], nil, dir); !errors.Is(snapshotErr, ErrFileChanged) || spool != nil {
				t.Fatalf("spool=%v err=%v", spool, snapshotErr)
			}
		})
	}

	dir := localTempDir(t)
	path := filepath.Join(dir, "credential")
	if err := os.WriteFile(path, []byte("safe-original"), 0o600); err != nil {
		t.Fatal(err)
	}
	bound, err := Bind(context.Background(), []Attachment{{Path: path}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	credential := []byte("safe-original")
	if spool, snapshotErr := Snapshot(context.Background(), bound[0], [][]byte{credential}, dir); !errors.Is(snapshotErr, ErrCredential) || spool != nil {
		t.Fatalf("spool=%v err=%v", spool, snapshotErr)
	}
}

func TestPreflightReturnsNormalizedIntentWithoutOpeningFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.txt")
	intent, err := Preflight([]Attachment{{Path: missing, RemoteFilename: "Report.TXT", MediaType: `Text/Plain; Charset="UTF-8"`}})
	if err != nil || len(intent) != 1 || intent[0].Path != filepath.Clean(missing) || intent[0].RemoteFilename != "Report.TXT" || intent[0].MediaType == nil || *intent[0].MediaType != "text/plain; charset=UTF-8" {
		t.Fatalf("intent/error = %#v/%v", intent, err)
	}
	auto, err := Preflight([]Attachment{{Path: missing}})
	if err != nil || auto[0].RemoteFilename != "missing.txt" || auto[0].MediaType != nil {
		t.Fatalf("auto intent/error = %#v/%v", auto, err)
	}
}

func TestBindScansOriginalMetadataBeforeCanonicalization(t *testing.T) {
	path := filepath.Join(localTempDir(t), "file")
	if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	input := Attachment{Path: path, RemoteFilename: "Report.TXT", MediaType: "TEXT/PLAIN"}
	for _, token := range [][]byte{[]byte("Report.TXT"), []byte("TEXT/PLAIN")} {
		if got, err := Bind(context.Background(), []Attachment{input}, [][]byte{token}); !errors.Is(err, ErrCredential) || got != nil {
			t.Fatalf("token=%q result=%v err=%v", token, got, err)
		}
	}
}

func TestBindRejectsHardlinkedFile(t *testing.T) {
	dir := localTempDir(t)
	path := filepath.Join(dir, "file")
	link := filepath.Join(dir, "hardlink")
	if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(path, link); err != nil {
		t.Fatal(err)
	}
	if got, err := Bind(context.Background(), []Attachment{{Path: path}}, nil); !errors.Is(err, ErrUnsafeFile) || got != nil {
		t.Fatalf("result=%v err=%v", got, err)
	}
}

func TestRecordedSnapshotDoesNotChangeWithLaterPath(t *testing.T) {
	dir := localTempDir(t)
	path := filepath.Join(dir, "file")
	first := []byte("first snapshot")
	if err := os.WriteFile(path, first, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Bind(context.Background(), []Attachment{{Path: path}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(dir, "replacement")
	if err := os.WriteFile(replacement, []byte("later content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	if got[0].ContentDigest != sha256.Sum256(first) || got[0].ByteLength != int64(len(first)) {
		t.Fatalf("recorded snapshot changed: %+v", got[0])
	}
}

func TestCredentialSetBounds(t *testing.T) {
	path := filepath.Join(localTempDir(t), "file")
	if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	tooMany := make([][]byte, maxCredentialCount+1)
	for i := range tooMany {
		tooMany[i] = []byte("x")
	}
	for name, credentials := range map[string][][]byte{
		"count": tooMany,
		"token": {bytes.Repeat([]byte("x"), maxCredentialBytes+1)},
		"total": {bytes.Repeat([]byte("x"), maxCredentialsBytes), []byte("y")},
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := Bind(context.Background(), []Attachment{{Path: path}}, credentials); !errors.Is(err, ErrCredentialSet) || got != nil {
				t.Fatalf("result=%v err=%v", got, err)
			}
		})
	}
}

func TestStreamScannerDoesNotAllocatePerChunk(t *testing.T) {
	scanner := mustScanner(t, [][]byte{[]byte("active-token"), []byte("second-token")}).stream()
	chunk := bytes.Repeat([]byte("ordinary binary payload\x00"), 128)
	if allocations := testing.AllocsPerRun(100, func() {
		if scanner.write(chunk) {
			t.Fatal("unexpected match")
		}
	}); allocations != 0 {
		t.Fatalf("streaming allocations=%v", allocations)
	}
}

func TestBindCountBoundsAndNoPartialResult(t *testing.T) {
	if got, err := Bind(context.Background(), nil, nil); err != nil || len(got) != 0 {
		t.Fatalf("zero=%v err=%v", got, err)
	}
	dir := localTempDir(t)
	path := filepath.Join(dir, "file")
	if err := os.WriteFile(path, []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	inputs := make([]Attachment, MaxAttachments)
	for i := range inputs {
		inputs[i] = Attachment{Path: path, RemoteFilename: "file"}
	}
	if got, err := Bind(context.Background(), inputs, nil); err != nil || len(got) != MaxAttachments {
		t.Fatalf("hundred=%d err=%v", len(got), err)
	}
	inputs = append(inputs, Attachment{Path: path, RemoteFilename: "file"})
	if got, err := Bind(context.Background(), inputs, nil); !errors.Is(err, ErrTooMany) || got != nil {
		t.Fatalf("101=%v err=%v", got, err)
	}
	two := []Attachment{{Path: path, RemoteFilename: "ok"}, {Path: path, RemoteFilename: "active-token"}}
	if got, err := Bind(context.Background(), two, [][]byte{[]byte("active-token")}); !errors.Is(err, ErrCredential) || got != nil {
		t.Fatalf("partial=%v err=%v", got, err)
	}
}

func TestBindRejectsNonRegularAndSymlinks(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("secure descriptor walk is supported on darwin and linux")
	}
	dir := localTempDir(t)
	regular := filepath.Join(dir, "regular")
	if err := os.WriteFile(regular, []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	leaf := filepath.Join(dir, "leaf")
	if err := os.Symlink(regular, leaf); err != nil {
		t.Fatal(err)
	}
	ancestor := filepath.Join(dir, "ancestor")
	if err := os.Symlink(dir, ancestor); err != nil {
		t.Fatal(err)
	}
	paths := []string{dir, leaf, filepath.Join(ancestor, "regular")}
	if runtime.GOOS != "windows" {
		fifo := filepath.Join(dir, "fifo")
		if err := makeFIFO(fifo); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, fifo)
	}
	for _, path := range paths {
		if got, err := Bind(context.Background(), []Attachment{{Path: path, RemoteFilename: "x"}}, nil); !errors.Is(err, ErrUnsafeFile) || got != nil {
			t.Errorf("path=%s result=%v err=%v", path, got, err)
		}
	}
}

func TestBindRejectsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := Bind(ctx, nil, nil); !errors.Is(err, context.Canceled) || got != nil {
		t.Fatalf("result=%v err=%v", got, err)
	}
}

func TestScanFileObservesCancellationBetweenReads(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	reader := &cancelingReader{cancel: cancel}
	_, _, _, err := scanFile(ctx, reader, mustScanner(t, nil).stream())
	if !errors.Is(err, context.Canceled) || reader.reads != 1 {
		t.Fatalf("reads=%d err=%v", reader.reads, err)
	}
}

func TestBindRejectsConcurrentMutation(t *testing.T) {
	for name, mutate := range map[string]func(*os.File, int64){
		"in-place": func(file *os.File, _ int64) {
			_, _ = file.WriteAt([]byte("b"), 0)
			_, _ = file.WriteAt([]byte("a"), 0)
		},
		"append": func(file *os.File, size int64) {
			_, _ = file.WriteAt([]byte("b"), size)
			_ = file.Truncate(size)
		},
		"truncate": func(file *os.File, size int64) {
			_ = file.Truncate(size - 1)
			_ = file.Truncate(size)
		},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(localTempDir(t), "large")
			const size = 16 << 20
			if err := os.WriteFile(path, bytes.Repeat([]byte("a"), size), 0o600); err != nil {
				t.Fatal(err)
			}
			stop := make(chan struct{})
			done := make(chan struct{})
			go func() {
				defer close(done)
				file, err := os.OpenFile(path, os.O_WRONLY, 0)
				if err != nil {
					return
				}
				defer file.Close()
				for {
					select {
					case <-stop:
						return
					default:
						mutate(file, size)
					}
				}
			}()
			time.Sleep(time.Millisecond)
			got, err := Bind(context.Background(), []Attachment{{Path: path, RemoteFilename: "large"}}, nil)
			close(stop)
			<-done
			if (!errors.Is(err, ErrFileChanged) && !errors.Is(err, ErrUnsafeFile)) || got != nil {
				t.Fatalf("result=%v err=%v", got, err)
			}
		})
	}
}

func TestBindRejectsReplacementDuringScan(t *testing.T) {
	dir := localTempDir(t)
	path := filepath.Join(dir, "large")
	replacement := filepath.Join(dir, "replacement")
	content := bytes.Repeat([]byte("a"), 32<<20)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(replacement, content, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		time.Sleep(time.Millisecond)
		done <- os.Rename(replacement, path)
	}()
	got, err := Bind(context.Background(), []Attachment{{Path: path, RemoteFilename: "large"}}, nil)
	if renameErr := <-done; renameErr != nil {
		t.Fatal(renameErr)
	}
	if (!errors.Is(err, ErrFileChanged) && !errors.Is(err, ErrUnsafeFile)) || got != nil {
		t.Fatalf("result=%v err=%v", got, err)
	}
}

func FuzzTokenScannerNeverMissesSplitCredential(f *testing.F) {
	f.Add([]byte("credential"), []byte("prefix"), []byte("suffix"), uint8(3))
	f.Fuzz(func(t *testing.T, token, prefix, suffix []byte, splitByte uint8) {
		if len(token) == 0 || len(token) > 256 || len(prefix)+len(suffix) > 1024 {
			t.Skip()
		}
		value := append(append(bytes.Clone(prefix), token...), suffix...)
		split := int(splitByte) % (len(value) + 1)
		scanner, err := newScanner([][]byte{token})
		if err != nil {
			t.Fatal(err)
		}
		stream := scanner.stream()
		found := stream.write(value[:split]) || stream.write(value[split:])
		if !found {
			t.Fatal("missed exact credential")
		}
	})
}

type oneByteReader struct{ data []byte }

type cancelingReader struct {
	cancel context.CancelFunc
	reads  int
}

func (r *cancelingReader) Read(p []byte) (int, error) {
	r.reads++
	p[0] = 'x'
	r.cancel()
	return 1, nil
}

func mustScanner(t *testing.T, tokens [][]byte) tokenScanner {
	t.Helper()
	scanner, err := newScanner(tokens)
	if err != nil {
		t.Fatal(err)
	}
	return scanner
}

func localTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp(".", ".stageinput-test-")
	if err != nil {
		t.Fatal(err)
	}
	absolute, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(absolute) })
	return absolute
}

func (r *oneByteReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	p[0] = r.data[0]
	r.data = r.data[1:]
	return 1, nil
}
