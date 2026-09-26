//go:build !windows && !darwin && !linux

package desktopruntime

import (
	"context"
	"io"
)

func platformServiceLogs(context.Context, string, int, bool, io.Writer, io.Writer) error {
	return errServiceControlUnsupported
}
