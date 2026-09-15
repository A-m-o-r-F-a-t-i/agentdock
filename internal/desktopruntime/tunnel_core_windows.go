//go:build windows

package desktopruntime

import (
	"context"
	"errors"
	"fmt"
	"time"
)

func restartTunnelCore(ctx context.Context, root string) error {
	return restartTunnelCoreUsing(ctx, root, platformServiceAction)
}

// Stop may cancel an OAuth-origin reload between stopping the old Core and
// launching its replacement. In that narrow window, still initiate Core
// recovery with a separate bounded context before allowing the supervisor to
// exit. A canceled Tunnel must not leave the local application offline.
func restartTunnelCoreUsing(ctx context.Context, root string, action func(context.Context, string, string) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	err := action(ctx, root, "restart")
	if err == nil || ctx.Err() == nil {
		return err
	}
	recovery, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	if recoveryErr := action(recovery, root, "start"); recoveryErr != nil {
		return errors.Join(err, fmt.Errorf("restore Core after canceled Tunnel reload: %w", recoveryErr))
	}
	return err
}
