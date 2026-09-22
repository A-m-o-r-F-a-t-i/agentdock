//go:build !windows

package desktopruntime

import (
	"context"
	"errors"
)

func platformVerifyTailscale(context.Context, string) (TunnelStatus, error) {
	return TunnelStatus{}, errors.New("native Funnel verification requires Windows Desktop")
}
