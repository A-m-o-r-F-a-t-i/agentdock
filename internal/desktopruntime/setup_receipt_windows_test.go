//go:build windows

package desktopruntime

import (
	"encoding/json"
	"errors"
	"golang.org/x/sys/windows"
	"os"
	"path/filepath"
	"testing"
)

func TestSetupReceiptSharingPartialNonceAndOriginalOutcome(t *testing.T) {
	path := filepath.Join(t.TempDir(), "result.json")
	expected := "AgentDock Setup Native test-nonce"
	check := func(want bool) setupLaunchResult {
		t.Helper()
		got, ready, err := readSetupReceipt(path, expected)
		if err != nil || ready != want {
			t.Fatalf("ready=%v err=%v", ready, err)
		}
		return got
	}
	check(false)
	original := setupLaunchResult{TaskName: expected, PID: 42, ExitCode: 7, ElapsedMS: 12, Error: "original child failure"}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	func() { defer windows.CloseHandle(handle); check(false) }()
	if got := check(true); got != original {
		t.Fatalf("child result altered: %+v", got)
	}
	for _, partial := range []string{"", "{", `{"task_name":`, `{"task_name":"AgentDock Setup Native test-nonce","exit_code":`} {
		if err := os.WriteFile(path, []byte(partial), 0600); err != nil {
			t.Fatal(err)
		}
		check(false)
	}
	original.TaskName = "old-nonce"
	if err := writeSetupJSON(path, original); err != nil {
		t.Fatal(err)
	}
	check(false)
	for _, invalid := range []string{`{"bad":!}`, `{"task_name":"AgentDock Setup Native test-nonce"}`, `{"task_name":"AgentDock Setup Native test-nonce","exit_code":null}`, `{"task_name":"AgentDock Setup Native test-nonce","exit_code":0} {}`} {
		if err := os.WriteFile(path, []byte(invalid), 0600); err != nil {
			t.Fatal(err)
		}
		if _, ready, err := readSetupReceipt(path, expected); ready || err == nil {
			t.Fatalf("corruption accepted: %s", invalid)
		}
	}
	for _, err := range []error{windows.ERROR_SHARING_VIOLATION, windows.ERROR_LOCK_VIOLATION, os.ErrNotExist} {
		if !setupReceiptPending(err) {
			t.Fatal(err)
		}
	}
	for _, err := range []error{windows.ERROR_ACCESS_DENIED, errors.New("unrelated I/O error")} {
		if setupReceiptPending(err) {
			t.Fatal("unrelated failure treated as pending", err)
		}
	}
}
