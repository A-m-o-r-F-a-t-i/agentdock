//go:build !windows

package desktopruntime

import "context"

func CleanupTailscaleFunnel(context.Context, string) (bool, string, error) { return false, "", nil }
