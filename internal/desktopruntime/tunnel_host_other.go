//go:build !windows

package desktopruntime

import (
	"context"
	"errors"
)

func prepareTunnelHostContext(ctx context.Context, root string, hostControlled bool) (context.Context, func(), error) {
	if hostControlled {
		return ctx, func() {}, errors.New("native runtime host control is Windows-only")
	}
	return ctx, func() {}, nil
}
