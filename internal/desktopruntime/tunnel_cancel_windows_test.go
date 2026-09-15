//go:build windows

package desktopruntime

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"
)

func TestTunnelStopEventCancelsBlockedOperation(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	root := t.TempDir()
	guard, err := acquireTunnelSupervisor(root)
	if err != nil || guard == nil {
		t.Fatalf("guard=%v err=%v", guard, err)
	}
	defer guard.Close()
	ctx, cancelJoin := guard.stopContext(context.Background())
	defer cancelJoin()
	if err := signalTunnelSupervisorStop(root); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("stop event did not cancel the provisioning/child-wait context")
	}
}

func TestTunnelOperationMutexSerializesSeparateThreads(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	root := t.TempDir()
	release, err := acquireTunnelOperation(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	held := true
	defer func() {
		if held {
			release()
		}
	}()
	tryAcquire := func(timeout time.Duration) <-chan error {
		result := make(chan error, 1)
		go func() {
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			unlock, err := acquireTunnelOperation(ctx, root)
			if err == nil {
				unlock()
			}
			result <- err
		}()
		return result
	}
	if err := <-tryAcquire(150 * time.Millisecond); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("concurrent operation entered: %v", err)
	}
	release()
	held = false
	if err := <-tryAcquire(time.Second); err != nil {
		t.Fatalf("released operation remained locked: %v", err)
	}
}

func TestTunnelStopPreemptsStartWithoutPoisoningNextStart(t *testing.T) {
	root := t.TempDir()
	active, finishActive, err := tunnelActionContext(context.Background(), root, false)
	if err != nil {
		t.Fatal(err)
	}
	_, finishStop, err := tunnelActionContext(context.Background(), root, true)
	if err != nil {
		finishActive()
		t.Fatal(err)
	}
	select {
	case <-active.Done():
	case <-time.After(time.Second):
		finishStop()
		finishActive()
		t.Fatal("stop did not preempt the active start")
	}
	finishStop()
	finishActive()
	next, finishNext, err := tunnelActionContext(context.Background(), root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer finishNext()
	select {
	case <-next.Done():
		t.Fatal("completed stop canceled a later independent start")
	case <-time.After(100 * time.Millisecond):
	}
}
