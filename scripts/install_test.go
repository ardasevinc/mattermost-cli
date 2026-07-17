package scripts_test

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestInstallerTargetsPinnedArtifactsAndCleansPrivateTemps(t *testing.T) {
	for _, target := range []struct{ os, arch, assetOS, assetArch string }{
		{"Darwin", "arm64", "darwin", "arm64"},
		{"Darwin", "x86_64", "darwin", "amd64"},
		{"Linux", "aarch64", "linux", "arm64"},
		{"Linux", "x86_64", "linux", "amd64"},
	} {
		t.Run(target.assetOS+"-"+target.assetArch, func(t *testing.T) {
			runInstaller(t, target.os, target.arch, 1)
		})
	}
}

func TestInstallerConcurrentReplacementRemainsAtomic(t *testing.T) {
	runInstaller(t, "Linux", "x86_64", 2)
}

func runInstaller(t *testing.T, goos, arch string, concurrency int) {
	t.Helper()
	root := t.TempDir()
	fixtures, fakeBin := filepath.Join(root, "fixtures"), filepath.Join(root, "bin")
	installDir, tempDir := filepath.Join(root, "install"), filepath.Join(root, "tmp")
	for _, path := range []string{fixtures, fakeBin, installDir, tempDir} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	assetOS := strings.ToLower(goos)
	assetArch := map[string]string{"arm64": "arm64", "aarch64": "arm64", "x86_64": "amd64"}[arch]
	asset := fmt.Sprintf("mattermost-cli_9.8.7_%s_%s.tar.gz", assetOS, assetArch)
	payload := []byte("#!/bin/sh\nprintf 'mm version 9.8.7 (fixture)\\n'\n")
	writeInstallerArchive(t, filepath.Join(fixtures, asset), payload)
	archive, err := os.ReadFile(filepath.Join(fixtures, asset))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(archive)
	if err := os.WriteFile(filepath.Join(fixtures, "checksums.txt"), []byte(fmt.Sprintf("%x  %s\n", digest, asset)), 0o600); err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(fakeBin, "uname"), "#!/bin/sh\nif [ \"$1\" = -s ]; then printf '%s\\n' \"$FAKE_OS\"; else printf '%s\\n' \"$FAKE_ARCH\"; fi\n")
	writeExecutable(t, filepath.Join(fakeBin, "curl"), `#!/bin/sh
dest=
url=
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o) dest="$2"; shift 2 ;;
    http*) url="$1"; shift ;;
    *) shift ;;
  esac
done
[ -n "$dest" ] && [ -n "$url" ]
cp "$FIXTURE_DIR/${url##*/}" "$dest"
`)
	environment := append(os.Environ(),
		"PATH="+fakeBin+":"+os.Getenv("PATH"),
		"FAKE_OS="+goos,
		"FAKE_ARCH="+arch,
		"FIXTURE_DIR="+fixtures,
		"MATTERMOST_CLI_VERSION=v9.8.7",
		"MATTERMOST_CLI_INSTALL_DIR="+installDir,
		"TMPDIR="+tempDir,
	)
	var wait sync.WaitGroup
	errors := make(chan error, concurrency)
	for range concurrency {
		wait.Add(1)
		go func() {
			defer wait.Done()
			command := exec.Command("/bin/sh", "install.sh") // #nosec G204 -- fixed test command.
			command.Env = environment
			output, err := command.CombinedOutput()
			if err != nil {
				errors <- fmt.Errorf("installer: %w: %s", err, output)
			}
		}()
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
	installed, err := exec.Command(filepath.Join(installDir, "mm"), "--version").Output() // #nosec G204 -- test-owned path.
	if err != nil || string(installed) != "mm version 9.8.7 (fixture)\n" {
		t.Fatalf("installed=%q err=%v", installed, err)
	}
	for _, directory := range []string{tempDir, installDir} {
		entries, err := os.ReadDir(directory)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), "mattermost-cli-install.") || strings.HasPrefix(entry.Name(), ".mattermost-cli-install.") {
				t.Fatalf("temporary path survived: %s", filepath.Join(directory, entry.Name()))
			}
		}
	}
}

func writeInstallerArchive(t *testing.T, path string, payload []byte) {
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

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
}
