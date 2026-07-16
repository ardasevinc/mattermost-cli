package stagestore

import (
	"errors"
	"fmt"
	"path/filepath"
)

const DatabaseFilename = "stages.sqlite3"

func validateDatabasePath(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || filepath.Base(path) != DatabaseFilename {
		return errors.New("stage store: database path must name the canonical database")
	}
	return nil
}

type LookupEnv func(string) (string, bool)

type Paths struct {
	StateDir string
	DBPath   string
}

func ResolvePaths(home string, lookup LookupEnv) (Paths, error) {
	if !filepath.IsAbs(home) {
		return Paths{}, fmt.Errorf("stage store: home directory must be absolute")
	}
	root := filepath.Join(home, ".local", "state")
	if value, ok := lookup("XDG_STATE_HOME"); ok && filepath.IsAbs(value) {
		root = value
	}
	dir := filepath.Join(root, "mattermost-cli")
	return Paths{StateDir: dir, DBPath: filepath.Join(dir, DatabaseFilename)}, nil
}
