//go:build !darwin && !linux

package stageinput

import "os"

type fileIdentity struct{ size int64 }

func (a fileIdentity) sameFile(fileIdentity) bool       { return false }
func (a fileIdentity) stable(fileIdentity) bool         { return false }
func openSecure(string) (*os.File, fileIdentity, error) { return nil, fileIdentity{}, ErrUnsupported }
func fileIdentityOf(*os.File) (fileIdentity, error)     { return fileIdentity{}, ErrUnsupported }
