package config

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestResolvePathsUsesOnlyAbsoluteXDGValues(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{
		"XDG_CONFIG_HOME": filepath.Join(home, "xdg-config"),
		"XDG_STATE_HOME":  "relative-state",
	}
	paths, err := ResolvePaths(home, mapLookup(env))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := paths.ConfigPath, filepath.Join(home, "xdg-config", "mattermost-cli", "config.toml"); got != want {
		t.Fatalf("ConfigPath = %q, want %q", got, want)
	}
	if got, want := paths.StateDir, filepath.Join(home, ".local", "state", "mattermost-cli"); got != want {
		t.Fatalf("StateDir = %q, want %q", got, want)
	}
}

func TestLoadFallsBackToLegacyWithoutMovingIt(t *testing.T) {
	home := t.TempDir()
	selected := filepath.Join(home, "selected", "mattermost-cli", "config.toml")
	legacy := filepath.Join(home, ".config", "mattermost-cli", "config.toml")
	writeConfig(t, legacy, `url = "https://legacy.example"`, 0o600)

	state := Load(Paths{ConfigPath: selected, LegacyPath: legacy})

	if state.Migration != MigrationLegacyFallback || state.ReadPath != legacy || state.SelectedPath != selected {
		t.Fatalf("Load() state = %+v, want legacy fallback", state)
	}
	if state.Config.URL != "https://legacy.example" {
		t.Fatalf("URL = %q, want legacy URL", state.Config.URL)
	}
	if _, err := os.Stat(selected); !os.IsNotExist(err) {
		t.Fatalf("selected config unexpectedly created: %v", err)
	}
}

func TestInitCreatesSecureTemplateWithoutOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.toml")
	created, err := Init(path)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("Init() created = false, want true")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("config mode = %#o, want 0600", got)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != Template {
		t.Fatalf("config template = %q, want exact template", data)
	}
	if err := os.WriteFile(path, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	created, err = Init(path)
	if err != nil || created {
		t.Fatalf("second Init() = (%v, %v), want (false, nil)", created, err)
	}
	data, err = os.ReadFile(path)
	if err != nil || string(data) != "keep me" {
		t.Fatalf("second Init() changed file: data=%q err=%v", data, err)
	}
}

func TestInitRejectsUnsafeExistingPath(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target.toml")
	link := filepath.Join(root, "config.toml")
	writeConfig(t, target, "keep me", 0o600)
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	created, err := Init(link)

	if err == nil || created {
		t.Fatalf("Init() = (%v, %v), want unsafe-path failure", created, err)
	}
	data, readErr := os.ReadFile(target)
	if readErr != nil || string(data) != "keep me" {
		t.Fatalf("Init() changed symlink target: data=%q err=%v", data, readErr)
	}
}

func TestInitRejectsExistingTokenWithInsecurePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	writeConfig(t, path, `token = "fixture-token"`, 0o644)

	created, err := Init(path)

	if err == nil || created {
		t.Fatalf("Init() = (%v, %v), want insecure-token failure", created, err)
	}
}

func TestLoadRejectsSymlinkedAncestorInsideUserDirectory(t *testing.T) {
	root := t.TempDir()
	realDirectory := filepath.Join(root, "real")
	linkDirectory := filepath.Join(root, "linked")
	path := filepath.Join(realDirectory, "config.toml")
	writeConfig(t, path, `token = "fixture-token"`, 0o600)
	if err := os.Symlink(realDirectory, linkDirectory); err != nil {
		t.Fatal(err)
	}

	state := Load(Paths{
		ConfigPath: filepath.Join(linkDirectory, "config.toml"),
		LegacyPath: filepath.Join(linkDirectory, "config.toml"),
	})

	if state.Error != FileErrorRead || state.Unsafe != UnsafeType || state.Config.Token != "" {
		t.Fatalf("Load() ancestor-symlink state = %+v, want fail closed", state)
	}
	if created, err := Init(filepath.Join(linkDirectory, "new.toml")); err == nil || created {
		t.Fatalf("Init() through ancestor symlink = (%v, %v), want failure", created, err)
	}
}

func TestLoadSelectedPathIsAuthoritative(t *testing.T) {
	home := t.TempDir()
	selected := filepath.Join(home, "selected", "config.toml")
	legacy := filepath.Join(home, "legacy", "config.toml")
	writeConfig(t, selected, `url = "https://selected.example"`, 0o600)
	writeConfig(t, legacy, `url = "https://legacy.example"`, 0o600)

	state := Load(Paths{ConfigPath: selected, LegacyPath: legacy})

	if state.Migration != MigrationLegacyIgnored || state.Config.URL != "https://selected.example" {
		t.Fatalf("Load() state = %+v, want selected authoritative", state)
	}
}

func TestLoadParsesSupportedFieldsAndIgnoresWrongTypes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	writeConfig(t, path, `
url = "  https://mattermost.example/chat/  "
token = "  fixture-token  "
redact = false
mention_names = [" Arda ", "", 42, "arda.sevinc"]
unknown = "ignored"
`, 0o600)

	state := Load(Paths{ConfigPath: path, LegacyPath: path})

	if state.Error != "" {
		t.Fatalf("Load() error = %q", state.Error)
	}
	if state.Config.URL != "https://mattermost.example/chat/" || state.Config.Token != "fixture-token" {
		t.Fatalf("Load() config = %+v", state.Config)
	}
	if state.Config.Redact == nil || *state.Config.Redact {
		t.Fatalf("Redact = %v, want false", state.Config.Redact)
	}
	if !slices.Equal(state.Config.MentionNames, []string{"Arda", "arda.sevinc"}) {
		t.Fatalf("MentionNames = %q", state.Config.MentionNames)
	}
}

func TestLoadCharacterizesMissingReadParseAndPermissions(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "missing.toml")
	if state := Load(Paths{ConfigPath: missing, LegacyPath: missing}); state.Exists || state.Error != "" {
		t.Fatalf("missing state = %+v", state)
	}
	if state := Load(Paths{ConfigPath: root, LegacyPath: root}); !state.Exists || state.Error != FileErrorRead {
		t.Fatalf("directory state = %+v", state)
	}
	malformed := filepath.Join(root, "malformed.toml")
	writeConfig(t, malformed, `not valid = [toml`, 0o600)
	if state := Load(Paths{ConfigPath: malformed, LegacyPath: malformed}); state.Error != FileErrorParse {
		t.Fatalf("malformed state = %+v", state)
	}
	insecure := filepath.Join(root, "insecure.toml")
	writeConfig(t, insecure, `token = "secret"`, 0o640)
	if state := Load(Paths{ConfigPath: insecure, LegacyPath: insecure}); !state.InsecurePermissions {
		t.Fatalf("insecure state = %+v", state)
	}
}

func TestLoadIgnoresUnsupportedTopLevelTypes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	writeConfig(t, path, `url = 42
token = false
redact = "false"
mention_names = "Arda"
unknown = "ignored"
`, 0o600)

	state := Load(Paths{ConfigPath: path, LegacyPath: path})

	if state.Error != "" || state.Config.URL != "" || state.Config.Token != "" || state.Config.Redact != nil || state.Config.MentionNames != nil {
		t.Fatalf("Load() state = %+v, want unsupported fields ignored", state)
	}
}

func TestLoadRejectsSymlinkedConfig(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target.toml")
	link := filepath.Join(root, "config.toml")
	writeConfig(t, target, `token = "fixture-token"`, 0o600)
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	state := Load(Paths{ConfigPath: link, LegacyPath: link})

	if !state.Exists || state.Error != FileErrorRead || state.Unsafe != UnsafeType || state.Config.Token != "" {
		t.Fatalf("Load() symlink state = %+v, want fail-closed read error", state)
	}
}

func TestResolvePreservesPrecedenceAndRedactSemantics(t *testing.T) {
	fileRedact := false
	file := FileState{Config: File{
		URL: "https://file.example", Token: "file-token", Redact: &fileRedact,
		MentionNames: []string{"Arda"},
	}}
	env := map[string]string{
		"MM_URL": "https://env.example", "MM_TOKEN": "env-token", "MM_REDACT": "",
	}
	cliRedact := false
	resolved := Resolve(Options{URL: "https://cli.example", Redact: &cliRedact}, mapLookup(env), file)

	if resolved.URLSource != SourceCLI || resolved.TokenSource != SourceEnv || resolved.RedactSource != SourceCLI {
		t.Fatalf("Resolve() sources = %s/%s/%s", resolved.URLSource, resolved.TokenSource, resolved.RedactSource)
	}
	if resolved.URL != "https://cli.example" || resolved.Token != "env-token" || resolved.Redact {
		t.Fatalf("Resolve() = %+v", resolved)
	}

	resolved = Resolve(Options{}, mapLookup(env), file)
	if !resolved.Redact || resolved.RedactSource != SourceEnv {
		t.Fatalf("empty MM_REDACT = (%v, %s), want (true, env)", resolved.Redact, resolved.RedactSource)
	}
	env["MM_REDACT"] = "false"
	resolved = Resolve(Options{}, mapLookup(env), file)
	if resolved.Redact {
		t.Fatal("MM_REDACT=false did not disable redaction")
	}
}

func TestResolveEmptyURLAndTokenOverridesFallThrough(t *testing.T) {
	file := FileState{Config: File{URL: "https://file.example", Token: "file-token"}}
	env := map[string]string{"MM_URL": "", "MM_TOKEN": ""}

	resolved := Resolve(Options{}, mapLookup(env), file)

	if resolved.URLSource != SourceFile || resolved.TokenSource != SourceFile || resolved.URL != file.Config.URL || resolved.Token != file.Config.Token {
		t.Fatalf("Resolve() = %+v, want file fallbacks", resolved)
	}
}

func mapLookup(values map[string]string) LookupEnv {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}

func writeConfig(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}
