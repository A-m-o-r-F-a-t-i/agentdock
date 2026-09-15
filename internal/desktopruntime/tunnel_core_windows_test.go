//go:build windows

package desktopruntime

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestTunnelCanceledCoreReloadStillInitiatesRecovery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	count := 0
	err := restartTunnelCoreUsing(ctx, "isolated", func(callContext context.Context, root, action string) error {
		count++
		if root != "isolated" {
			t.Fatal("wrong runtime")
		}
		if count == 1 {
			if action != "restart" {
				t.Fatal("missing restart")
			}
			cancel()
			return context.Canceled
		}
		if action != "start" || callContext.Err() != nil {
			t.Fatal("recovery inherited the stop cancellation")
		}
		deadline, exists := callContext.Deadline()
		if !exists || time.Until(deadline) > 3*time.Second {
			t.Fatal("unbounded Core recovery")
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) || count != 2 {
		t.Fatalf("count=%d error=%v", count, err)
	}
}

func TestTunnelAlreadyCanceledDoesNotRestartCore(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := restartTunnelCoreUsing(ctx, "isolated", func(context.Context, string, string) error { t.Fatal("Core action after cancellation"); return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestTunnelCoreReloadPreservesNonCancellationFailure(t *testing.T) {
	failure := errors.New("restart rejected")
	count := 0
	err := restartTunnelCoreUsing(context.Background(), "isolated", func(context.Context, string, string) error { count++; return failure })
	if !errors.Is(err, failure) || count != 1 {
		t.Fatalf("count=%d error=%v", count, err)
	}
}
