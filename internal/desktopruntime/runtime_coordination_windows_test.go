//go:build windows

package desktopruntime

import (
	"context"
	"golang.org/x/sys/windows"
	"runtime"
	"testing"
	"unsafe"
)

func TestRuntimeCoordinationUsesPrivateSameUserIntegrity(t *testing.T) {
	attributes, sid, err := runtimeCoordinationAttributes()
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateEvent(attributes, 1, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	if err := validateRuntimeCoordinationObject(handle, sid); err != nil {
		t.Fatal(err)
	}
	if err := validateRuntimeCoordinationObject(handle, "S-1-5-18"); err == nil {
		t.Fatal("different user accepted")
	}
	ctx, closeCtx, err := tunnelActionContext(context.Background(), t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer closeCtx()
	if err := ctx.Err(); err != nil {
		t.Fatal(err)
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	release, err := acquireTunnelOperation(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	release()
}
func TestRuntimeCoordinationRejectsPrecreatedWideObjectWithoutSignalling(t *testing.T) {
	root := t.TempDir()
	sid, err := tokenUserSID(windows.GetCurrentProcessToken())
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := windows.SecurityDescriptorFromString("O:" + sid + "D:(A;;GA;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	attributes := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: descriptor}
	name, err := windows.UTF16PtrFromString(tunnelSupervisorObjectName("cancel-operation", root))
	if err != nil {
		t.Fatal(err)
	}
	event, err := windows.CreateEvent(&attributes, 1, 0, name)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(event)
	if _, closeCtx, err := tunnelActionContext(t.Context(), root, true); err == nil {
		closeCtx()
		t.Fatal("wide precreated object accepted")
	}
	state, err := windows.WaitForSingleObject(event, 0)
	if err != nil || state != uint32(windows.WAIT_TIMEOUT) {
		t.Fatal("foreign event was signalled", state, err)
	}
}
