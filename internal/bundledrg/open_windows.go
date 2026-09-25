//go:build windows

package bundledrg

import (
	"golang.org/x/sys/windows"
	"os"
)

// Keep the verified executable immutable until its search process has exited.
// This changes only sharing on the handle, not ACLs or global security policy.
func openReadOnly(path string) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(handle), path), nil
}
