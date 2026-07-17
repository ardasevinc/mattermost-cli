//go:build !darwin && !linux

package stagestore

import (
	"context"
	"fmt"
)

func prepareWritable(context.Context, string) (bool, bool, func(), error) {
	return false, false, nil, fmt.Errorf("stage store: unsupported platform")
}
func lockExisting(context.Context, string) (func(), error) {
	return nil, fmt.Errorf("stage store: unsupported platform")
}
func validateExisting(string) error      { return fmt.Errorf("stage store: unsupported platform") }
func validateSidecars(string) error      { return fmt.Errorf("stage store: unsupported platform") }
func validateImmutableRead(string) error { return fmt.Errorf("stage store: unsupported platform") }
func removeFailedNewDatabase(string)     {}
func clearBootstrapPending(string) error { return fmt.Errorf("stage store: unsupported platform") }
func platformSupported() bool            { return false }
func permissionModelLimitations() []string {
	return []string{"secure stage storage is unsupported on this platform"}
}
