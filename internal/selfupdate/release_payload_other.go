//go:build !windows

package selfupdate

import "errors"

type windowsReleasePayload struct {
	CorePath    string
	BundlePath  string
	DesktopPath string
}

func extractWindowsReleasePayload([]byte, string, string, string) (windowsReleasePayload, error) {
	return windowsReleasePayload{}, errors.New("Windows Release payload extraction is unavailable on this platform")
}
