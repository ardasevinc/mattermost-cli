//go:build !darwin && !linux

package stageinput

import "errors"

func makeFIFO(string) error { return errors.New("unsupported") }
