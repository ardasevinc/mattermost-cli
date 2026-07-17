package config

import (
	"fmt"
	"path/filepath"
)

type LookupEnv func(string) (string, bool)

type Paths struct {
	ConfigPath string
	LegacyPath string
	StateDir   string
}

func ResolvePaths(home string, lookup LookupEnv) (Paths, error) {
	if !filepath.IsAbs(home) {
		return Paths{}, fmt.Errorf("home directory must be absolute")
	}
	legacy := filepath.Join(home, ".config", "mattermost-cli", "config.toml")
	configPath := legacy
	if value, ok := lookup("XDG_CONFIG_HOME"); ok && filepath.IsAbs(value) {
		configPath = filepath.Join(value, "mattermost-cli", "config.toml")
	}
	stateRoot := filepath.Join(home, ".local", "state")
	if value, ok := lookup("XDG_STATE_HOME"); ok && filepath.IsAbs(value) {
		stateRoot = value
	}
	return Paths{
		ConfigPath: configPath,
		LegacyPath: legacy,
		StateDir:   filepath.Join(stateRoot, "mattermost-cli"),
	}, nil
}
