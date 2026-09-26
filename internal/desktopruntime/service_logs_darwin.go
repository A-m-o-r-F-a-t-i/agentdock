//go:build darwin

package desktopruntime

import (
	"context"
	"io"
	"path/filepath"
)

func platformServiceLogs(ctx context.Context, runtimeRoot string, lines int, follow bool, stdout, _ io.Writer) error {
	if _, _, err := loadUnixRuntime(runtimeRoot); err != nil {
		return err
	}
	logDir, err := macOSLogDir()
	if err != nil {
		return err
	}
	return tailServiceLogFile(ctx, filepath.Join(logDir, "agentdock.err.log"), lines, follow, stdout)
}
