package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

var (
	versionPattern = regexp.MustCompile(`^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$`)
	commitPattern  = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
)

type target struct{ os, arch string }

func main() {
	var version, commit, output string
	flag.StringVar(&version, "version", "", "release version without the v prefix")
	flag.StringVar(&commit, "commit", "", "source commit SHA")
	flag.StringVar(&output, "output", "dist", "artifact output directory")
	flag.Parse()
	if err := run(version, commit, output); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(version, commit, output string) error {
	version = strings.TrimPrefix(strings.TrimSpace(version), "v")
	commit = strings.TrimSpace(commit)
	if !versionPattern.MatchString(version) {
		return errors.New("release version must be semantic and omit the v prefix")
	}
	if !commitPattern.MatchString(commit) {
		return errors.New("release commit must be a 7-40 character lowercase hex SHA")
	}
	if output == "" {
		return errors.New("release output directory is required")
	}
	if err := prepareOutput(output); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "mattermost-cli-release-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	checksums := make(map[target]string, 4)
	for _, target := range []target{{"darwin", "amd64"}, {"darwin", "arm64"}, {"linux", "amd64"}, {"linux", "arm64"}} {
		binary := filepath.Join(tmp, target.os+"-"+target.arch, "mm")
		if err := os.MkdirAll(filepath.Dir(binary), 0o755); err != nil { // #nosec G301 -- temporary release inputs contain no secrets.
			return err
		}
		ldflags := fmt.Sprintf("-s -w -buildid= -X github.com/ardasevinc/mattermost-cli/v2/internal/buildinfo.Version=%s -X github.com/ardasevinc/mattermost-cli/v2/internal/buildinfo.Commit=%s", version, commit)
		command := exec.Command("go", "build", "-trimpath", "-buildvcs=false", "-ldflags", ldflags, "-o", binary, "./cmd/mm") // #nosec G204 -- values are validated or from the closed target list.
		command.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+target.os, "GOARCH="+target.arch)
		command.Stdout, command.Stderr = os.Stdout, os.Stderr
		if err := command.Run(); err != nil {
			return fmt.Errorf("build %s/%s: %w", target.os, target.arch, err)
		}
		name := fmt.Sprintf("mattermost-cli_%s_%s_%s.tar.gz", version, target.os, target.arch)
		archivePath := filepath.Join(output, name)
		if err := writeArchive(archivePath, binary); err != nil {
			return fmt.Errorf("package %s/%s: %w", target.os, target.arch, err)
		}
		digest, err := fileDigest(archivePath)
		if err != nil {
			return err
		}
		checksums[target] = digest
	}
	formulaPath := filepath.Join(output, "mattermost-cli.rb")
	if err := os.WriteFile(formulaPath, []byte(formula(version, checksums)), 0o644); err != nil { // #nosec G306 -- public Homebrew formula.
		return err
	}
	formulaDigest, err := fileDigest(formulaPath)
	if err != nil {
		return err
	}
	lines := []string{formulaDigest + "  mattermost-cli.rb"}
	for target, digest := range checksums {
		lines = append(lines, digest+"  "+fmt.Sprintf("mattermost-cli_%s_%s_%s.tar.gz", version, target.os, target.arch))
	}
	sort.Strings(lines)
	return os.WriteFile(filepath.Join(output, "checksums.txt"), []byte(strings.Join(lines, "\n")+"\n"), 0o644) // #nosec G306 -- public release checksums.
}

func formula(version string, checksums map[target]string) string {
	digest := func(goos, arch string) string { return checksums[target{goos, arch}] }
	return fmt.Sprintf(`class MattermostCli < Formula
  desc "Mattermost CLI for agents and humans"
  homepage "https://github.com/ardasevinc/mattermost-cli"
  version %q
  license "MIT"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/ardasevinc/mattermost-cli/releases/download/v#{version}/mattermost-cli_#{version}_darwin_arm64.tar.gz"
      sha256 %q
    else
      url "https://github.com/ardasevinc/mattermost-cli/releases/download/v#{version}/mattermost-cli_#{version}_darwin_amd64.tar.gz"
      sha256 %q
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/ardasevinc/mattermost-cli/releases/download/v#{version}/mattermost-cli_#{version}_linux_arm64.tar.gz"
      sha256 %q
    else
      url "https://github.com/ardasevinc/mattermost-cli/releases/download/v#{version}/mattermost-cli_#{version}_linux_amd64.tar.gz"
      sha256 %q
    end
  end

  def install
    bin.install "mm"
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/mm --version")
    assert_match "Mattermost CLI", shell_output("#{bin}/mm --help")
  end
end
`, version, digest("darwin", "arm64"), digest("darwin", "amd64"), digest("linux", "arm64"), digest("linux", "amd64"))
}

func prepareOutput(path string) error {
	if err := os.MkdirAll(path, 0o755); err != nil { // #nosec G301 -- release artifacts are public-readable.
		return err
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return errors.New("release output directory must be empty")
	}
	return nil
}

func writeArchive(path, binary string) (returnErr error) {
	data, err := os.ReadFile(binary) // #nosec G304 -- internally generated binary.
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644) // #nosec G302,G304 -- public release artifact.
	if err != nil {
		return err
	}
	defer func() {
		if err := file.Close(); returnErr == nil && err != nil {
			returnErr = err
		}
	}()
	zipper, err := gzip.NewWriterLevel(file, gzip.BestCompression)
	if err != nil {
		return err
	}
	zipper.ModTime = time.Unix(0, 0).UTC()
	zipper.OS = 255
	archive := tar.NewWriter(zipper)
	header := &tar.Header{Name: "mm", Mode: 0o755, Size: int64(len(data)), ModTime: time.Unix(0, 0).UTC(), Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}
	if err := archive.WriteHeader(header); err != nil {
		return err
	}
	if _, err := archive.Write(data); err != nil {
		return err
	}
	if err := archive.Close(); err != nil {
		return err
	}
	return zipper.Close()
}

func fileDigest(path string) (string, error) {
	file, err := os.Open(path) // #nosec G304 -- internally generated release artifact.
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
