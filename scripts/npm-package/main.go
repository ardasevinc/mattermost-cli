package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var versionPattern = regexp.MustCompile(`^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$`)

type target struct {
	goos, goarch  string
	npmOS, npmCPU string
}

type packageJSON struct {
	Name                 string            `json:"name"`
	Version              string            `json:"version"`
	Description          string            `json:"description"`
	Author               string            `json:"author"`
	License              string            `json:"license"`
	Repository           repository        `json:"repository"`
	Keywords             []string          `json:"keywords,omitempty"`
	Bin                  map[string]string `json:"bin,omitempty"`
	Files                []string          `json:"files"`
	Engines              map[string]string `json:"engines,omitempty"`
	OS                   []string          `json:"os,omitempty"`
	CPU                  []string          `json:"cpu,omitempty"`
	OptionalDependencies map[string]string `json:"optionalDependencies,omitempty"`
	PublishConfig        map[string]string `json:"publishConfig"`
}

type repository struct {
	Type string `json:"type"`
	URL  string `json:"url"`
}

func main() {
	var version, releaseDirectory, output string
	flag.StringVar(&version, "version", "", "release version without the v prefix")
	flag.StringVar(&releaseDirectory, "release-dir", "dist", "directory containing native release archives")
	flag.StringVar(&output, "output", "npm-dist", "generated npm package directory")
	flag.Parse()
	if err := run(version, releaseDirectory, output); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(version, releaseDirectory, output string) error {
	version = strings.TrimPrefix(strings.TrimSpace(version), "v")
	if !versionPattern.MatchString(version) {
		return errors.New("npm package version must be semantic and omit the v prefix")
	}
	if releaseDirectory == "" || output == "" {
		return errors.New("release and output directories are required")
	}
	if err := prepareOutput(output); err != nil {
		return err
	}
	repositoryRoot, err := findRepositoryRoot()
	if err != nil {
		return err
	}
	checksums, err := loadChecksums(filepath.Join(releaseDirectory, "checksums.txt"))
	if err != nil {
		return err
	}
	targets := []target{
		{"darwin", "amd64", "darwin", "x64"}, {"darwin", "arm64", "darwin", "arm64"},
		{"linux", "amd64", "linux", "x64"}, {"linux", "arm64", "linux", "arm64"},
	}
	dependencies := make(map[string]string, len(targets))
	for _, target := range targets {
		name := platformPackageName(target)
		dependencies[name] = version
		archiveName := fmt.Sprintf("mattermost-cli_%s_%s_%s.tar.gz", version, target.goos, target.goarch)
		archivePath := filepath.Join(releaseDirectory, archiveName)
		if err := requireDigest(archivePath, checksums[archiveName]); err != nil {
			return fmt.Errorf("verify %s: %w", archiveName, err)
		}
		binary, err := extractBinary(archivePath)
		if err != nil {
			return fmt.Errorf("extract %s: %w", archiveName, err)
		}
		directory := filepath.Join(output, strings.TrimPrefix(name, "@ardasevinc/"))
		if err := os.MkdirAll(filepath.Join(directory, "bin"), 0o755); err != nil { // #nosec G301 -- public package tree.
			return err
		}
		if err := os.WriteFile(filepath.Join(directory, "bin", "mm"), binary, 0o755); err != nil { // #nosec G306 -- public executable.
			return err
		}
		manifest := baseManifest(name, version)
		manifest.Description = "Native binary for mattermost-cli on " + target.goos + "/" + target.goarch
		manifest.OS, manifest.CPU = []string{target.npmOS}, []string{target.npmCPU}
		manifest.Files = []string{"bin/mm"}
		if err := writeManifest(filepath.Join(directory, "package.json"), manifest); err != nil {
			return err
		}
	}
	launcher := filepath.Join(output, "mattermost-cli")
	if err := os.MkdirAll(filepath.Join(launcher, "bin"), 0o755); err != nil { // #nosec G301 -- public package tree.
		return err
	}
	for _, source := range []struct{ from, to string }{
		{filepath.Join(repositoryRoot, "npm", "bin", "mm.js"), filepath.Join(launcher, "bin", "mm.js")},
		{filepath.Join(repositoryRoot, "npm", "README.md"), filepath.Join(launcher, "README.md")},
		{filepath.Join(repositoryRoot, "LICENSE"), filepath.Join(launcher, "LICENSE")},
	} {
		data, err := os.ReadFile(source.from) // #nosec G304 -- fixed repository sources.
		if err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if strings.HasSuffix(source.to, ".js") {
			mode = 0o755
		}
		if err := os.WriteFile(source.to, data, mode); err != nil { // #nosec G306 -- public package files.
			return err
		}
	}
	manifest := baseManifest("mattermost-cli", version)
	manifest.Description = "Mattermost CLI for agents and humans"
	manifest.Keywords = []string{"mattermost", "cli", "agents", "messages", "go"}
	manifest.Bin = map[string]string{"mm": "bin/mm.js"}
	manifest.Files = []string{"bin/mm.js", "README.md", "LICENSE"}
	manifest.Engines = map[string]string{"node": ">=18"}
	manifest.OptionalDependencies = dependencies
	return writeManifest(filepath.Join(launcher, "package.json"), manifest)
}

func findRepositoryRoot() (string, error) {
	directory, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if info, err := os.Stat(filepath.Join(directory, "go.mod")); err == nil && info.Mode().IsRegular() {
			return directory, nil
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return "", errors.New("could not locate repository root")
		}
		directory = parent
	}
}

func baseManifest(name, version string) packageJSON {
	return packageJSON{
		Name: name, Version: version, Author: "Arda Sevinc <arda@ardasevinc.com>", License: "MIT",
		Repository:    repository{Type: "git", URL: "https://github.com/ardasevinc/mattermost-cli"},
		PublishConfig: map[string]string{"access": "public"},
	}
}

func platformPackageName(target target) string {
	return "@ardasevinc/mattermost-cli-" + target.goos + "-" + target.goarch
}

func prepareOutput(path string) error {
	if err := os.MkdirAll(path, 0o755); err != nil { // #nosec G301 -- generated public package tree.
		return err
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return errors.New("npm package output directory must be empty")
	}
	return nil
}

func loadChecksums(path string) (map[string]string, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- selected local release directory.
	if err != nil {
		return nil, err
	}
	result := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		parts := strings.Split(line, "  ")
		if len(parts) != 2 || len(parts[0]) != 64 || strings.ContainsAny(parts[1], `/\\`) {
			return nil, errors.New("invalid checksums manifest")
		}
		if _, err := hex.DecodeString(parts[0]); err != nil {
			return nil, errors.New("invalid checksums manifest")
		}
		if _, duplicate := result[parts[1]]; duplicate {
			return nil, errors.New("duplicate checksum entry")
		}
		result[parts[1]] = parts[0]
	}
	return result, nil
}

func requireDigest(path, expected string) error {
	if expected == "" {
		return errors.New("archive checksum is missing")
	}
	file, err := os.Open(path) // #nosec G304 -- selected release artifact.
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return err
	}
	if got := hex.EncodeToString(hash.Sum(nil)); got != expected {
		return errors.New("archive checksum mismatch")
	}
	return nil
}

func extractBinary(path string) ([]byte, error) {
	file, err := os.Open(path) // #nosec G304 -- checksum-verified release artifact.
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	zipper, err := gzip.NewReader(file)
	if err != nil {
		return nil, err
	}
	defer func() { _ = zipper.Close() }()
	archive := tar.NewReader(zipper)
	header, err := archive.Next()
	if err != nil {
		return nil, err
	}
	if header.Name != "mm" || header.Typeflag != tar.TypeReg || header.Mode != 0o755 || header.Size < 1 || header.Size > 100<<20 {
		return nil, errors.New("archive has invalid mm member")
	}
	binary, err := io.ReadAll(io.LimitReader(archive, header.Size+1))
	if err != nil || int64(len(binary)) != header.Size {
		return nil, errors.New("archive has invalid mm payload")
	}
	if _, err := archive.Next(); err != io.EOF {
		return nil, errors.New("archive contains extra members")
	}
	return binary, nil
}

func writeManifest(path string, manifest packageJSON) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644) // #nosec G306 -- public npm manifest.
}
