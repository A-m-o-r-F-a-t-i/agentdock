//go:build windows

package desktopruntime

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func setupWorkerTestRequest(t *testing.T, mode string) (string, SetupLaunchRequest) {
	t.Helper()
	t.Setenv("AGENTDOCK_TEST_SETUP_CHILD", "1")
	t.Setenv("AGENTDOCK_TEST_SETUP_MODE", mode)
	root := t.TempDir()
	requestRoot := filepath.Join(root, "request")
	if err := os.Mkdir(requestRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	sid, err := tokenUserSID(windows.GetCurrentProcessToken())
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(requestRoot, "request.json"), SetupLaunchRequest{
		FilePath: executable, Arguments: "-test.run=^TestSetupLaunchTestChild$",
		WaitForExit: true, TimeoutSeconds: 5, OwnerSID: sid,
		TaskName:    "AgentDock Setup Native test-" + filepath.Base(root),
		Environment: map[string]string{"AGENTDOCK_DEFAULT_DIR": root},
	}
}

func readSetupTestResult(t *testing.T, path string) setupLaunchResult {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(filepath.Dir(path), "result.json"))
	if err != nil {
		t.Fatal(err)
	}
	var result setupLaunchResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestSetupWorkerCreatesNoConsole(t *testing.T) {
	path, request := setupWorkerTestRequest(t, "probe")
	if err := runSetupLaunchWorker(path, request); err != nil {
		t.Fatal(err)
	}
	result := readSetupTestResult(t, path)
	if result.ExitCode != 0 || result.PID <= 0 {
		t.Fatalf("bad receipt: %+v", result)
	}
	stdout, err := setupStdout(filepath.Join(filepath.Dir(path), "stdout.log"), 4096)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(stdout) != "console=0 UTF8=中文" {
		t.Fatalf("console was created or UTF-8 changed: %q", stdout)
	}
}

func TestSetupWorkerPreservesSignedExitCode(t *testing.T) {
	path, request := setupWorkerTestRequest(t, "failure")
	if err := runSetupLaunchWorker(path, request); err == nil {
		t.Fatal("failed child was accepted")
	}
	result := readSetupTestResult(t, path)
	if result.ExitCode != -1 {
		t.Fatalf("exit=%d, want -1", result.ExitCode)
	}
}

func TestSetupWorkerTimeoutTerminatesItsChild(t *testing.T) {
	path, request := setupWorkerTestRequest(t, "hang")
	request.TimeoutSeconds = 1
	started := time.Now()
	if err := runSetupLaunchWorker(path, request); err == nil {
		t.Fatal("timeout was accepted")
	}
	result := readSetupTestResult(t, path)
	if result.PID <= 0 || setupTestProcessAlive(result.PID) {
		t.Fatalf("timed out child remains alive: %+v", result)
	}
	if time.Since(started) > 5*time.Second {
		t.Fatal("child timeout exceeded cleanup budget")
	}
}

func TestSetupNoWaitChildSurvivesAndDoesNotLockRequestFiles(t *testing.T) {
	path, request := setupWorkerTestRequest(t, "hang")
	request.WaitForExit = false
	if err := runSetupLaunchWorker(path, request); err != nil {
		t.Fatal(err)
	}
	result := readSetupTestResult(t, path)
	if result.PID <= 0 {
		t.Fatalf("missing PID: %+v", result)
	}
	defer func() {
		if process, err := os.FindProcess(result.PID); err == nil {
			_ = process.Kill()
			_, _ = process.Wait()
		}
	}()
	if err := os.RemoveAll(filepath.Dir(path)); err != nil {
		t.Fatalf("released child kept request logs open: %v", err)
	}
	time.Sleep(250 * time.Millisecond)
	if !setupTestProcessAlive(result.PID) {
		t.Fatal("released child died when the launch worker returned")
	}
}

func setupTestProcessAlive(pid int) bool {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)
	var code uint32
	return windows.GetExitCodeProcess(handle, &code) == nil && code == 259
}

func TestSetupStdoutRejectsTruncatedProtocol(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stdout.log")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 65)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := setupStdout(path, 64); err == nil {
		t.Fatal("oversized JSON stdout was silently truncated")
	}
}

func TestSetupRequestValidation(t *testing.T) {
	path, request := setupWorkerTestRequest(t, "probe")
	request.Environment["PATH"] = "untrusted"
	if err := writeSetupJSON(path, request); err != nil {
		t.Fatal(err)
	}
	if _, err := readSetupLaunchRequest(path); err == nil {
		t.Fatal("unexpected environment override accepted")
	}
	delete(request.Environment, "PATH")
	request.TimeoutSeconds = 0
	if err := writeSetupJSON(path, request); err != nil {
		t.Fatal(err)
	}
	if _, err := readSetupLaunchRequest(path); err == nil {
		t.Fatal("unbounded launch accepted")
	}
	request.TimeoutSeconds = 2
	if err := writeSetupJSON(path, request); err != nil {
		t.Fatal(err)
	}
	if _, err := readSetupLaunchRequest(path); err != nil {
		t.Fatal(err)
	}
}

// This is a real child process so a hidden inherited console cannot satisfy
// the no-console assertion. The native worker must use CREATE_NO_WINDOW.
func TestSetupLaunchTestChild(t *testing.T) {
	if os.Getenv("AGENTDOCK_TEST_SETUP_CHILD") != "1" {
		t.Skip("helper process only")
	}
	switch os.Getenv("AGENTDOCK_TEST_SETUP_MODE") {
	case "probe":
		window, _, _ := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleWindow").Call()
		fmt.Printf("console=%d UTF8=中文\n", window)
		os.Exit(0)
	case "failure":
		fmt.Fprintln(os.Stderr, "failure fixture")
		os.Exit(-1)
	case "hang":
		time.Sleep(time.Minute)
		os.Exit(0)
	default:
		t.Fatal(errors.New("invalid helper mode"))
	}
}
