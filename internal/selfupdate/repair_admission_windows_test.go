//go:build windows

package selfupdate

import (
	"context"
	"testing"

	"golang.org/x/sys/windows"
)

func TestPostmergeExplicitDesktopRepairCannotSilentlySkipBusyOwner(t *testing.T) {
	name, err := windows.UTF16PtrFromString(windowsDesktopRepairMutexName)
	if err != nil {
		t.Fatal(err)
	}
	mutex, err := windows.CreateMutex(nil, false, name)
	if mutex != 0 {
		defer windows.CloseHandle(mutex)
	}
	if err == windows.ERROR_ALREADY_EXISTS {
		t.Skip("unrelated repair mutex already present on isolated runner")
	}
	if err != nil {
		t.Fatal(err)
	}
	// Manual repair must not report success when its archive was never used.
	handled, err := handleWindowsDesktopRepairCommand(context.Background(), []string{
		windowsDesktopRepairCommand, "--local-archive", "fixture.zip", "--checksum", "fixture.sha256",
	})
	if !handled || err == nil {
		t.Fatalf("explicit repair silently skipped: handled=%v error=%v", handled, err)
	}
}
