//go:build windows

package desktopruntime

import (
	"context"
	"errors"
	"os"
	"testing"
)

func TestSetupExecutorRunsFiniteCommandWithoutTaskScheduler(t *testing.T) {
	path, request := setupWorkerTestRequest(t, "probe")
	request.TimeoutSeconds = 1800
	request.OwnerSID, request.TaskName = "", ""
	if err := writeSetupJSON(path, request); err != nil {
		t.Fatal(err)
	}
	code, err := RunSetupExecutor(path)
	if err != nil || code != 0 {
		t.Fatalf("exit=%d error=%v", code, err)
	}
	result := readSetupTestResult(t, path)
	if result.PID <= 0 || result.ExitCode != 0 {
		t.Fatalf("bad direct execution receipt: %+v", result)
	}
}

func TestSetupExecutorPreservesChildFailure(t *testing.T) {
	path, request := setupWorkerTestRequest(t, "failure")
	if err := writeSetupJSON(path, request); err != nil {
		t.Fatal(err)
	}
	code, err := RunSetupExecutor(path)
	if err == nil || code != -1 {
		t.Fatalf("exit=%d error=%v", code, err)
	}
}

func TestSetupExecutorTimeoutReturns124AndReapsChild(t *testing.T) {
	path, request := setupWorkerTestRequest(t, "hang")
	request.TimeoutSeconds = 1
	if err := writeSetupJSON(path, request); err != nil {
		t.Fatal(err)
	}
	code, err := RunSetupExecutor(path)
	if code != 124 || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("exit=%d error=%v", code, err)
	}
	result := readSetupTestResult(t, path)
	if setupTestProcessAlive(result.PID) {
		t.Fatal("timed-out direct child remains alive")
	}
}

func TestSetupExecutorRejectsUnboundedBackgroundRequest(t *testing.T) {
	path, request := setupWorkerTestRequest(t, "probe")
	request.WaitForExit = false
	if err := writeSetupJSON(path, request); err != nil {
		t.Fatal(err)
	}
	code, err := RunSetupExecutor(path)
	if err == nil || code != 1 {
		t.Fatalf("exit=%d error=%v", code, err)
	}
}

func TestSetupRequestAcceptsWindowsUTF8BOM(t *testing.T) {
	path, request := setupWorkerTestRequest(t, "probe")
	if err := writeSetupJSON(path, request); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append([]byte{0xef, 0xbb, 0xbf}, data...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readSetupLaunchRequest(path); err != nil {
		t.Fatal(err)
	}
}
