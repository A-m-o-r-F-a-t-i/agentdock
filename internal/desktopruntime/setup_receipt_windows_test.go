//go:build windows

package desktopruntime

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestPostmergeSetupReceiptRecoversAfterExclusiveScannerHandle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "result.json")
	want := setupLaunchResult{TaskName: "AgentDock Setup Native fixture", PID: 123, ExitCode: 0}
	if err := writeSetupJSON(path, want); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, executable, "-test.run=^TestPostmergeSetupReceiptLockChild$")
	child.Env = append(os.Environ(), "AGENTDOCK_TEST_RECEIPT_LOCK_PATH="+path)
	child.Stderr = os.Stderr
	input, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		_ = input.Close()
		if !waited {
			_ = child.Wait()
		}
	}()
	if line, err := bufio.NewReader(output).ReadString('\n'); err != nil || strings.TrimSpace(line) != "locked" {
		t.Fatalf("external scanner did not acquire its handle: %q %v", line, err)
	}
	// Assert that the native failure exists before testing its interpretation.
	if _, err := os.ReadFile(path); !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
		t.Fatalf("native sharing-conflict fixture was not established: %v", err)
	}
	_, ready, readErr := readSetupReceipt(path)
	_ = input.Close()
	closeErr := child.Wait()
	waited = true
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

func TestPostmergeSetupReceiptLockChild(t *testing.T) {
	path := os.Getenv("AGENTDOCK_TEST_RECEIPT_LOCK_PATH")
	if path == "" {
		t.Skip("external lock fixture only")
	}
	native, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(native, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	fmt.Fprintln(os.Stdout, "locked")
	_, _ = io.Copy(io.Discard, os.Stdin)
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
