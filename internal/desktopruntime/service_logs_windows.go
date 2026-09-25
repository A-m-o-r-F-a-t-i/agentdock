//go:build windows

package desktopruntime

import (
	"context"
	"io"
	"path/filepath"
)

func platformServiceLogs(ctx context.Context, runtimeRoot string, lines int, follow bool, stdout, _ io.Writer) error {
	_, root, err := loadDesktopManifest(runtimeRoot)
	if err != nil {
		return err
	}
	return tailServiceLogFile(ctx, filepath.Join(root, "logs", "agentdock.err.log"), lines, follow, stdout)
}
