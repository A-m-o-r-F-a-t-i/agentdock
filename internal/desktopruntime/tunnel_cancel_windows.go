//go:build windows

package desktopruntime

import (
	"context"
	"fmt"
	"time"

	"golang.org/x/sys/windows"
)

// stopContext delivers stop to provisioning, child Wait and Core health waits,
// not just to the supervisor's retry loop. The watcher is joined before the
// guard closes its native handles.
func (guard *tunnelSupervisorGuard) stopContext(parent context.Context) (context.Context, func()) {
	return watchTunnelStop(parent, guard.stopRequested)
}

func watchTunnelStop(parent context.Context, stopped func() (bool, error)) (context.Context, func()) {
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			requested, err := stopped()
			if requested || err != nil {
				cancel()
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return ctx, func() { cancel(); <-done }
}

// A stop request preempts an in-flight controller before waiting for its
// operation mutex. All already queued starts share this manual-reset event;
// after they leave, the kernel destroys it and a subsequent start is fresh.
// This prevents "stop" waiting for a failed Quick Tunnel's full start timeout.
func tunnelActionContext(parent context.Context, root string, stop bool) (context.Context, func(), error) {
	name, err := windows.UTF16PtrFromString(tunnelSupervisorObjectName("cancel-operation", root))
	if err != nil {
		return nil, nil, err
	}
	attributes, sid, err := runtimeCoordinationAttributes()
	if err != nil {
		return nil, nil, err
	}
	event, createErr := windows.CreateEvent(attributes, 1, 0, name)
	if event == 0 {
		return nil, nil, fmt.Errorf("create tunnel operation event: %w", createErr)
	}
	if err := validateRuntimeCoordinationObject(event, sid); err != nil {
		windows.CloseHandle(event)
		return nil, nil, err
	}
	closeEvent := func() { _ = windows.CloseHandle(event) }
	if stop {
		if err := windows.SetEvent(event); err != nil {
			closeEvent()
			return nil, nil, err
		}
		return parent, closeEvent, nil
	}
	ctx, cancelJoin := watchTunnelStop(parent, func() (bool, error) {
		state, err := windows.WaitForSingleObject(event, 0)
		return state == windows.WAIT_OBJECT_0, err
	})
	return ctx, func() { cancelJoin(); closeEvent() }, nil
}

// Caller pins its OS thread until release. Operations in different processes
// serialize per runtime root; the long-lived supervisor uses a separate mutex.
func acquireTunnelOperation(ctx context.Context, root string) (func(), error) {
	name, err := windows.UTF16PtrFromString(tunnelSupervisorObjectName("operation", root))
	if err != nil {
		return nil, err
	}
	attributes, sid, err := runtimeCoordinationAttributes()
	if err != nil {
		return nil, err
	}
	mutex, createErr := windows.CreateMutex(attributes, false, name)
	if mutex == 0 {
		return nil, fmt.Errorf("create tunnel operation mutex: %w", createErr)
	}
	if err := validateRuntimeCoordinationObject(mutex, sid); err != nil {
		windows.CloseHandle(mutex)
		return nil, err
	}
	for {
		if err := ctx.Err(); err != nil {
			windows.CloseHandle(mutex)
			return nil, err
		}
		state, err := windows.WaitForSingleObject(mutex, 100)
		if err != nil {
			windows.CloseHandle(mutex)
			return nil, err
		}
		if state == windows.WAIT_OBJECT_0 || state == windows.WAIT_ABANDONED {
			return func() { windows.ReleaseMutex(mutex); windows.CloseHandle(mutex) }, nil
		}
		if state != uint32(windows.WAIT_TIMEOUT) {
			windows.CloseHandle(mutex)
			return nil, fmt.Errorf("tunnel operation wait: 0x%x", state)
		}
	}
}
