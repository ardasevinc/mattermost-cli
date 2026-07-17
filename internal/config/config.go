package config

import (
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

const maxConfigBytes = 1 << 20

const Template = `# Mattermost CLI Configuration
# https://github.com/ardasevinc/mattermost-cli

url = "https://mattermost.example.com"
token = "your-personal-access-token"
# mention_names = ["Arda", "arda.sevinc"]
# stage_ttl_seconds = 0
# stage_prune_after_seconds = 0
`

const MaxRetentionSeconds = int64(math.MaxInt64) / int64(time.Second)

type Source string

const (
	SourceCLI     Source = "cli"
	SourceEnv     Source = "env"
	SourceFile    Source = "file"
	SourceDefault Source = "default"
	SourceMissing Source = "missing"
)

type Migration string

const (
	MigrationNone           Migration = "none"
	MigrationLegacyFallback Migration = "legacy_fallback"
	MigrationLegacyIgnored  Migration = "legacy_ignored"
)

type FileError string

const (
	FileErrorRead  FileError = "read"
	FileErrorParse FileError = "parse"
)

type UnsafeReason string

const (
	UnsafeType        UnsafeReason = "type"
	UnsafeChanged     UnsafeReason = "changed"
	UnsafeOwnership   UnsafeReason = "ownership"
	UnsafeUnsupported UnsafeReason = "unsupported"
)

type File struct {
	URL                    string
	Token                  string
	Redact                 *bool
	MentionNames           []string
	StageTTLSeconds        int64
	StagePruneAfterSeconds int64
}

type FileState struct {
	Config              File
	SelectedPath        string
	ReadPath            string
	LegacyPath          string
	Exists              bool
	InsecurePermissions bool
	WritableByOthers    bool
	Error               FileError
	Unsafe              UnsafeReason
	Migration           Migration
}

type Options struct {
	URL    string
	Token  string
	Redact *bool
}

type Resolved struct {
	URL                    string
	Token                  string
	Redact                 bool
	URLSource              Source
	TokenSource            Source
	RedactSource           Source
	MentionNames           []string
	StageTTLSeconds        int64
	StagePruneAfterSeconds int64
	File                   FileState
}

func Load(paths Paths) FileState {
	selected := inspect(paths.ConfigPath)
	selected.SelectedPath = paths.ConfigPath
	selected.LegacyPath = paths.LegacyPath
	if paths.ConfigPath == paths.LegacyPath {
		return selected
	}
	if selected.Exists || selected.Error != "" {
		if configPathPresent(paths.LegacyPath) {
			selected.Migration = MigrationLegacyIgnored
		}
		return selected
	}
	legacy := inspect(paths.LegacyPath)
	if !legacy.Exists && legacy.Error == "" {
		return selected
	}
	legacy.SelectedPath = paths.ConfigPath
	legacy.LegacyPath = paths.LegacyPath
	legacy.Migration = MigrationLegacyFallback
	return legacy
}

func configPathPresent(path string) bool {
	file, _, _, err := openConfigFile(path)
	if file != nil {
		_ = file.Close()
	}
	return !errors.Is(err, os.ErrNotExist)
}

func Init(path string) (bool, error) {
	file, err := createConfigFile(path)
	if errors.Is(err, os.ErrExist) {
		existing := inspect(path)
		if existing.Unsafe != "" || existing.Error == FileErrorRead || existing.WritableByOthers || (existing.InsecurePermissions && existing.Config.Token != "") {
			return false, fmt.Errorf("existing config path is unsafe")
		}
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("could not create config file")
	}
	complete := false
	defer func() {
		_ = file.Close()
		if !complete {
			_ = os.Remove(path)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return false, fmt.Errorf("could not secure config file")
	}
	written, err := io.WriteString(file, Template)
	if err != nil {
		return false, fmt.Errorf("could not write config template")
	}
	if written != len(Template) {
		return false, fmt.Errorf("could not write config template")
	}
	if err := file.Sync(); err != nil {
		return false, fmt.Errorf("could not sync config template")
	}
	if err := file.Close(); err != nil {
		return false, fmt.Errorf("could not close config template")
	}
	complete = true
	return true, nil
}

func Resolve(options Options, lookup LookupEnv, file FileState) Resolved {
	url, urlSource := first(options.URL, envNonempty(lookup, "MM_URL"), file.Config.URL)
	token, tokenSource := first(options.Token, envNonempty(lookup, "MM_TOKEN"), file.Config.Token)
	redact, redactSource := resolveRedact(options.Redact, lookup, file.Config.Redact)
	return Resolved{
		URL:                    url,
		Token:                  token,
		Redact:                 redact,
		URLSource:              urlSource,
		TokenSource:            tokenSource,
		RedactSource:           redactSource,
		MentionNames:           append([]string(nil), file.Config.MentionNames...),
		StageTTLSeconds:        file.Config.StageTTLSeconds,
		StagePruneAfterSeconds: file.Config.StagePruneAfterSeconds,
		File:                   file,
	}
}

func inspect(path string) FileState {
	state := FileState{SelectedPath: path, ReadPath: path}
	file, info, unsafe, err := openConfigFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return state
	}
	if err != nil {
		state.Exists = true
		state.Error = FileErrorRead
		state.Unsafe = unsafe
		return state
	}
	state.Exists = true
	defer func() { _ = file.Close() }()
	state.InsecurePermissions = info.Mode().Perm()&0o077 != 0
	state.WritableByOthers = info.Mode().Perm()&0o022 != 0
	data, err := io.ReadAll(io.LimitReader(file, maxConfigBytes+1))
	if err != nil || len(data) > maxConfigBytes {
		state.Error = FileErrorRead
		return state
	}
	parsed, err := parse(data)
	if err != nil {
		state.Error = FileErrorParse
		return state
	}
	state.Config = parsed
	return state
}

func parse(data []byte) (File, error) {
	var raw map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil {
		return File{}, err
	}
	result := File{
		URL:   trimmedString(raw["url"]),
		Token: trimmedString(raw["token"]),
	}
	if value, ok := raw["redact"].(bool); ok {
		result.Redact = &value
	}
	if values, ok := raw["mention_names"].([]any); ok {
		result.MentionNames = make([]string, 0, len(values))
		for _, rawValue := range values {
			if value, ok := rawValue.(string); ok {
				value = strings.TrimSpace(value)
				if value != "" {
					result.MentionNames = append(result.MentionNames, value)
				}
			}
		}
	}
	var err error
	if result.StageTTLSeconds, err = retentionSeconds(raw, "stage_ttl_seconds"); err != nil {
		return File{}, err
	}
	if result.StagePruneAfterSeconds, err = retentionSeconds(raw, "stage_prune_after_seconds"); err != nil {
		return File{}, err
	}
	return result, nil
}

func retentionSeconds(raw map[string]any, key string) (int64, error) {
	value, exists := raw[key]
	if !exists {
		return 0, nil
	}
	seconds, ok := value.(int64)
	if !ok || seconds < 0 || seconds > MaxRetentionSeconds {
		return 0, fmt.Errorf("invalid %s", key)
	}
	return seconds, nil
}

func first(cli string, envValue string, file string) (string, Source) {
	if cli != "" {
		return cli, SourceCLI
	}
	if envValue != "" {
		return envValue, SourceEnv
	}
	if file != "" {
		return file, SourceFile
	}
	return "", SourceMissing
}

func envNonempty(lookup LookupEnv, name string) string {
	value, _ := lookup(name)
	return value
}

func resolveRedact(cli *bool, lookup LookupEnv, file *bool) (bool, Source) {
	if cli != nil {
		return *cli, SourceCLI
	}
	if value, ok := lookup("MM_REDACT"); ok {
		return value != "false", SourceEnv
	}
	if file != nil {
		return *file, SourceFile
	}
	return true, SourceDefault
}

func trimmedString(value any) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(text)
}

func (f FileState) Warning() string {
	switch f.Migration {
	case MigrationLegacyFallback:
		return fmt.Sprintf("selected config %q is absent; reading legacy config %q without modifying it", f.SelectedPath, f.LegacyPath)
	case MigrationLegacyIgnored:
		return fmt.Sprintf("selected config %q is authoritative; ignoring legacy config %q", f.SelectedPath, f.LegacyPath)
	default:
		return ""
	}
}
