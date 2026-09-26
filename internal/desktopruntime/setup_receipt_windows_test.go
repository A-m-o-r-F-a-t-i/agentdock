//go:build windows

package desktopruntime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestPostmergeSetupReceiptRecoversAfterExclusiveScannerHandle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "result.json")
	want := setupLaunchResult{TaskName: "AgentDock Setup Native fixture", PID: 123, ExitCode: 0}
	if err := writeSetupJSON(path, want); err != nil {
		t.Fatal(err)
	}
	native, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(native, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, ready, readErr := readSetupReceipt(path)
	closeErr := windows.CloseHandle(handle)
	if ready || readErr != nil {
		t.Fatalf("temporary sharing conflict aborted the broker: ready=%v err=%v", ready, readErr)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	got, ready, err := readSetupReceipt(path)
	if err != nil || !ready || got != want {
		t.Fatalf("receipt failed to recover after release: %+v ready=%v err=%v", got, ready, err)
	}
}

func TestPostmergeSetupReceiptRejectsDamageAndPersistentPermissionErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "result.json")
	if _, ready, err := readSetupReceipt(path); ready || err != nil {
		t.Fatalf("missing receipt is not pending: ready=%v err=%v", ready, err)
	}
	for _, content := range []string{"{broken", strings.Repeat("x", maximumSetupReceiptBytes+1)} {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if _, ready, err := readSetupReceipt(path); ready || err == nil {
			t.Fatal("damaged/oversized receipt was accepted or hidden as pending")
		}
	}
	if setupReceiptPending(windows.ERROR_ACCESS_DENIED) || setupReceiptPending(windows.ERROR_INVALID_DATA) {
		t.Fatal("persistent permission/corruption errors were hidden as transient")
	}
	for _, err := range []error{windows.ERROR_SHARING_VIOLATION, windows.ERROR_LOCK_VIOLATION} {
		if !setupReceiptPending(&os.PathError{Op: "open", Path: path, Err: err}) {
			t.Fatal("wrapped transient Windows error was not recognized")
		}
	}
}
