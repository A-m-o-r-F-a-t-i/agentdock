//go:build !windows

package bundledrg

import "os"

func openReadOnly(path string) (*os.File, error) { return os.Open(path) }
