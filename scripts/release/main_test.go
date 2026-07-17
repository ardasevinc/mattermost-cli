package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriteArchiveIsCanonicalAndDeterministic(t *testing.T) {
	directory := t.TempDir()
	binary := filepath.Join(directory, "input")
	if err := os.WriteFile(binary, []byte("mm-test"), 0o755); err != nil {
		t.Fatal(err)
	}
	first, second := filepath.Join(directory, "first.tar.gz"), filepath.Join(directory, "second.tar.gz")
	if err := writeArchive(first, binary); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(binary, time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := writeArchive(second, binary); err != nil {
		t.Fatal(err)
	}
	one, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	two, err := os.ReadFile(second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(one, two) {
		t.Fatal("archive bytes changed with source metadata")
	}
	zipper, err := gzip.NewReader(bytes.NewReader(one))
	if err != nil {
		t.Fatal(err)
	}
	archive := tar.NewReader(zipper)
	header, err := archive.Next()
	if err != nil {
		t.Fatal(err)
	}
	if header.Name != "mm" || header.Mode != 0o755 || !header.ModTime.Equal(time.Unix(0, 0)) || header.Uid != 0 || header.Gid != 0 {
		t.Fatalf("noncanonical header: %+v", header)
	}
	payload, err := io.ReadAll(archive)
	if err != nil || string(payload) != "mm-test" {
		t.Fatalf("payload=%q err=%v", payload, err)
	}
	if _, err := archive.Next(); err != io.EOF {
		t.Fatalf("archive has unexpected second member: %v", err)
	}
}

func TestReleaseArgumentsAndOutputAreClosed(t *testing.T) {
	for _, test := range []struct{ version, commit, want string }{
		{"", "abcdef0", "release version"},
		{"latest", "abcdef0", "release version"},
		{"1.2.3", "dev", "release commit"},
		{"1.2.3", "ABCDEF0", "release commit"},
	} {
		err := run(test.version, test.commit, t.TempDir())
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("version=%q commit=%q err=%v", test.version, test.commit, err)
		}
	}
	nonempty := t.TempDir()
	if err := os.WriteFile(filepath.Join(nonempty, "stale"), []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run("1.2.3", "abcdef0", nonempty); err == nil || !strings.Contains(err.Error(), "must be empty") {
		t.Fatalf("nonempty output err=%v", err)
	}
}

func TestFormulaBindsEveryExactArchiveDigest(t *testing.T) {
	checksums := map[target]string{
		{"darwin", "arm64"}: strings.Repeat("a", 64),
		{"darwin", "amd64"}: strings.Repeat("b", 64),
		{"linux", "arm64"}:  strings.Repeat("c", 64),
		{"linux", "amd64"}:  strings.Repeat("d", 64),
	}
	got := formula("2.0.0", checksums)
	for _, value := range []string{"version \"2.0.0\"", "mattermost-cli_#{version}_darwin_arm64.tar.gz", "mattermost-cli_#{version}_linux_amd64.tar.gz", strings.Repeat("a", 64), strings.Repeat("d", 64), `bin.install "mm"`} {
		if !strings.Contains(got, value) {
			t.Fatalf("formula missing %q:\n%s", value, got)
		}
	}
}
