package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunBuildsLauncherAndChecksumBoundPlatformPackages(t *testing.T) {
	releaseDirectory := t.TempDir()
	checksums := make([]string, 0, 4)
	for _, target := range []target{
		{"darwin", "amd64", "darwin", "x64"}, {"darwin", "arm64", "darwin", "arm64"},
		{"linux", "amd64", "linux", "x64"}, {"linux", "arm64", "linux", "arm64"},
	} {
		name := "mattermost-cli_2.0.0_" + target.goos + "_" + target.goarch + ".tar.gz"
		path := filepath.Join(releaseDirectory, name)
		writeArchive(t, path, []byte(target.goos+"/"+target.goarch))
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(data)
		checksums = append(checksums, hex.EncodeToString(digest[:])+"  "+name)
	}
	if err := os.WriteFile(filepath.Join(releaseDirectory, "checksums.txt"), []byte(strings.Join(checksums, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "packages")
	if err := run("2.0.0", releaseDirectory, output); err != nil {
		t.Fatal(err)
	}
	launcher := readManifest(t, filepath.Join(output, "mattermost-cli", "package.json"))
	if launcher.Name != "mattermost-cli" || launcher.Version != "2.0.0" || launcher.Bin["mm"] != "bin/mm.js" || len(launcher.OptionalDependencies) != 4 {
		t.Fatalf("launcher manifest = %+v", launcher)
	}
	for _, target := range []target{
		{"darwin", "amd64", "darwin", "x64"}, {"darwin", "arm64", "darwin", "arm64"},
		{"linux", "amd64", "linux", "x64"}, {"linux", "arm64", "linux", "arm64"},
	} {
		shortName := strings.TrimPrefix(platformPackageName(target), "@ardasevinc/")
		directory := filepath.Join(output, shortName)
		manifest := readManifest(t, filepath.Join(directory, "package.json"))
		if manifest.Name != platformPackageName(target) || manifest.Version != "2.0.0" || len(manifest.OS) != 1 || manifest.OS[0] != target.npmOS || len(manifest.CPU) != 1 || manifest.CPU[0] != target.npmCPU {
			t.Fatalf("%s manifest = %+v", shortName, manifest)
		}
		binary, err := os.ReadFile(filepath.Join(directory, "bin", "mm"))
		if err != nil || string(binary) != target.goos+"/"+target.goarch {
			t.Fatalf("%s binary=%q err=%v", shortName, binary, err)
		}
	}
}

func TestRunRejectsMissingOrChangedArchive(t *testing.T) {
	releaseDirectory := t.TempDir()
	if err := os.WriteFile(filepath.Join(releaseDirectory, "checksums.txt"), []byte(strings.Repeat("0", 64)+"  mattermost-cli_2.0.0_darwin_amd64.tar.gz\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(releaseDirectory, "mattermost-cli_2.0.0_darwin_amd64.tar.gz"), []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := run("2.0.0", releaseDirectory, filepath.Join(t.TempDir(), "out"))
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("run() error = %v", err)
	}
}

func readManifest(t *testing.T, path string) packageJSON {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var manifest packageJSON
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	return manifest
}

func writeArchive(t *testing.T, path string, payload []byte) {
	t.Helper()
	file, err := os.Create(path) // #nosec G304 -- test-owned path.
	if err != nil {
		t.Fatal(err)
	}
	zipper := gzip.NewWriter(file)
	archive := tar.NewWriter(zipper)
	if err := archive.WriteHeader(&tar.Header{Name: "mm", Mode: 0o755, Size: int64(len(payload)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := archive.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zipper.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}
