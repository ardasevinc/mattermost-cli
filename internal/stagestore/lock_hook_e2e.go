//go:build (darwin || linux) && e2e

package stagestore

import "os"

func notifyFlockContention() {
	path := os.Getenv("MM_E2E_LOCK_CONTENTION_MARKER")
	if path == "" {
		return
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		_ = file.Close()
	}
}
