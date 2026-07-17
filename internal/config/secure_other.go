//go:build !darwin && !linux

package config

import (
	"errors"
	"os"
)

func openConfigFile(_ string) (*os.File, os.FileInfo, UnsafeReason, error) {
	return nil, nil, UnsafeUnsupported, errors.New("secure config access is unsupported")
}

func createConfigFile(_ string) (*os.File, error) {
	return nil, errors.New("secure config access is unsupported")
}
