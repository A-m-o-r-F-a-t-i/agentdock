//go:build !windows

package desktopruntime

import (
	"context"
	"errors"
)

func platformTailscaleStatus(_ context.Context, _, _ string) (TunnelStatus, error) {
	return TunnelStatus{}, errors.New("当前平台不支持原生 Tailscale Funnel 管理；首期仅支持 Windows Desktop")
}
