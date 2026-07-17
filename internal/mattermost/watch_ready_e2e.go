//go:build e2e

package mattermost

import "os"

func notifyWatchReady() {
	path := os.Getenv("MM_E2E_WATCH_READY_MARKER")
	if path == "" {
		return
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		_ = file.Close()
	}
}
