//go:build windows

package desktopruntime

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/uvwt/agentdock/internal/fs/atomicfile"
	"github.com/uvwt/agentdock/internal/fs/securepath"
	"golang.org/x/sys/windows"
)

// The GUI-subsystem tray shim doubles as the installation launch broker. Task
// Scheduler launches this native worker in the same signed-in user's session;
// it never launches an interactive PowerShell console or changes elevation.
type SetupLaunchRequest struct {
	FilePath       string            `json:"file_path"`
	Arguments      string            `json:"arguments"`
	WaitForExit    bool              `json:"wait_for_exit"`
	TimeoutSeconds int               `json:"timeout_seconds"`
	Environment    map[string]string `json:"environment"`
	OwnerSID       string            `json:"owner_sid,omitempty"`
	TaskName       string            `json:"task_name,omitempty"`
}

type setupLaunchResult struct {
	TaskName  string `json:"task_name"`
	PID       int    `json:"pid"`
	ExitCode  int    `json:"exit_code"`
	ElapsedMS int64  `json:"elapsed_ms"`
	Error     string `json:"error,omitempty"`
}

// RunSetupLauncher handles only explicit installation launch modes. Ordinary
// stable-entry execution continues to use the generation pointer as before.
func RunSetupLauncher(requestPath string, worker bool) error {
	requestPath, err := filepath.Abs(requestPath)
	if err != nil {
		return err
	}
	request, err := readSetupLaunchRequest(requestPath)
	if err != nil {
		return err
	}
	if worker {
		return runSetupLaunchWorker(requestPath, request)
	}
	return runSetupLaunchBroker(requestPath, request)
}

func readSetupLaunchRequest(path string) (SetupLaunchRequest, error) {
	return readSetupRequest(path, 600)
}

func readSetupRequest(path string, maximumTimeout int) (SetupLaunchRequest, error) {
	var request SetupLaunchRequest
	info, err := os.Lstat(path)
	if err != nil {
		return request, err
	}
	if !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return request, errors.New("invalid setup launch request file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return request, err
	}
	// Windows script/installer writers may emit a UTF-8 BOM.
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	if err := json.Unmarshal(data, &request); err != nil {
		return request, err
	}
	if !filepath.IsAbs(request.FilePath) || request.TimeoutSeconds < 1 || request.TimeoutSeconds > maximumTimeout {
		return request, fmt.Errorf("setup launch requires an absolute executable and timeout between 1 and %d seconds", maximumTimeout)
	}
	if info, err := os.Stat(request.FilePath); err != nil || !info.Mode().IsRegular() {
		return request, fmt.Errorf("setup executable is not a regular file: %s", request.FilePath)
	}
	for key := range request.Environment {
		if key != "AGENTDOCK_HOME" && key != "AGENTDOCK_DEFAULT_DIR" {
			return request, fmt.Errorf("unsupported setup environment key: %s", key)
		}
	}
	return request, nil
}

func writeSetupJSON(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return atomicfile.Write(path, append(data, '\n'), 0o600)
}

func runSetupLaunchBroker(path string, request SetupLaunchRequest) error {
	root := filepath.Dir(path)
	if err := securepath.EnsurePrivate(root); err != nil {
		return err
	}
	sid, err := tokenUserSID(windows.GetCurrentProcessToken())
	if err != nil {
		return err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	request.OwnerSID = sid
	request.TaskName = "AgentDock Setup Native " + hex.EncodeToString(nonce[:])
	if err := writeSetupJSON(path, request); err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	if err := registerSetupTask(request, executable, path); err != nil {
		return err
	}
	completed := false
	defer func() { _ = deleteSetupTask(request.TaskName, !completed) }()
	if err := startInteractiveScheduledTaskNative(request.TaskName, sid); err != nil {
		return err
	}
	deadline := time.NewTimer(time.Duration(request.TimeoutSeconds+10) * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		result, ready, err := readSetupReceipt(filepath.Join(root, "result.json"))
		if err != nil {
			return err
		}
		if ready {
			// A manually retried request path can still contain the previous
			// worker's receipt. Only this launch's nonce may acknowledge success.
			if result.TaskName != request.TaskName {
				select {
				case <-deadline.C:
					return errors.New("native setup did not replace a stale launch receipt")
				case <-ticker.C:
					continue
				}
			}
			completed = true
			stdout, stdoutErr := setupStdout(filepath.Join(root, "stdout.log"), 4<<20)
			stderr := setupLogTail(filepath.Join(root, "stderr.log"), 16<<10)
			if result.ExitCode != 0 || result.Error != "" {
				return fmt.Errorf("Runtime process exited with exit code %d. Child exit status (unsigned): %d; pid=%d elapsed_ms=%d; %s; stderr: %s; stdout: %s", result.ExitCode, uint32(result.ExitCode), result.PID, result.ElapsedMS, result.Error, strings.TrimSpace(stderr), strings.TrimSpace(stdout))
			}
			if stdoutErr != nil {
				return stdoutErr
			}
			if stdout == "" {
				return nil
			}
			_, err = io.WriteString(os.Stdout, stdout)
			return err
		}
		select {
		case <-deadline.C:
			return fmt.Errorf("native setup launch did not finish within %d seconds; request=%s; stderr: %s", request.TimeoutSeconds+10, path, setupLogTail(filepath.Join(root, "stderr.log"), 16<<10))
		case <-ticker.C:
		}
	}
}

func setupLogTail(path string, limit int64) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	if info, err := file.Stat(); err == nil && info.Size() > limit {
		_, _ = file.Seek(-limit, io.SeekEnd)
	}
	data, _ := io.ReadAll(io.LimitReader(file, limit))
	return string(data)
}

func setupStdout(path string, limit int64) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return "", err
	}
	if int64(len(data)) > limit {
		return "", errors.New("native setup output exceeds size limit; refusing a truncated Engine response")
	}
	return string(data), nil
}

func runSetupLaunchWorker(path string, request SetupLaunchRequest) (runErr error) {
	sid, err := tokenUserSID(windows.GetCurrentProcessToken())
	if err != nil {
		return err
	}
	if sid != request.OwnerSID || !strings.HasPrefix(request.TaskName, "AgentDock Setup Native ") {
		return errors.New("setup worker user or task identity mismatch")
	}
	// Worker cleanup also runs if the installer/broker has already exited.
	defer func() { _ = deleteSetupTask(request.TaskName, false) }()
	_, err = runSetupProcess(path, request)
	return err
}

func runSetupProcess(path string, request SetupLaunchRequest) (result setupLaunchResult, runErr error) {
	root := filepath.Dir(path)
	started := time.Now()
	result = setupLaunchResult{TaskName: request.TaskName, ExitCode: 1}
	defer func() {
		result.ElapsedMS = time.Since(started).Milliseconds()
		if runErr != nil {
			result.Error = runErr.Error()
		}
		writeErr := writeSetupJSON(filepath.Join(root, "result.json"), result)
		runErr = errors.Join(runErr, writeErr)
	}()
	stdout, err := os.OpenFile(filepath.Join(root, "stdout.log"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return result, err
	}
	defer stdout.Close()
	stderr, err := os.OpenFile(filepath.Join(root, "stderr.log"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return result, err
	}
	defer stderr.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(request.TimeoutSeconds)*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, request.FilePath)
	if !request.WaitForExit {
		command = exec.Command(request.FilePath)
	}
	command.Dir = filepath.Dir(request.FilePath)
	command.Env = os.Environ()
	for key, value := range request.Environment {
		command.Env = append(command.Env, key+"="+value)
	}
	command.SysProcAttr = &syscall.SysProcAttr{
		HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW | windows.CREATE_NEW_PROCESS_GROUP,
		CmdLine: windows.EscapeArg(request.FilePath) + " " + request.Arguments,
	}
	// Regular files, not anonymous pipes: grandchildren cannot hold a pipe open
	// and prevent finite Engine/service commands from reporting their exit.
	command.Stdout, command.Stderr = stdout, stderr
	if !request.WaitForExit {
		// A released GUI process may outlive Setup. It must not inherit open
		// handles to the short-lived request directory and prevent its cleanup.
		command.Stdout, command.Stderr = nil, nil
	}
	if err := command.Start(); err != nil {
		return result, err
	}
	result.PID = command.Process.Pid
	if !request.WaitForExit {
		// No Wait means launch acknowledgement only (Tray). Do not cancel the
		// CommandContext before releasing its running child: use a non-context
		// command for this mode so the worker cannot terminate the new Tray.
		result.ExitCode = 0
		return result, command.Process.Release()
	}
	err = command.Wait()
	if command.ProcessState != nil {
		result.ExitCode = int(int32(command.ProcessState.ExitCode()))
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if err != nil {
		return result, err
	}
	return result, nil
}

func withSetupTaskFolder(operation func(*iDispatch) error) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	hr, _, _ := procCoInitializeEx.Call(0, coInitApartmentThreaded)
	if hr != 0 && hr != 1 && hr != 0x80010106 {
		return fmt.Errorf("initialize Task Scheduler COM: 0x%x", hr)
	}
	if hr == 0 || hr == 1 {
		defer procCoUninitialize.Call()
	}
	service, err := createDispatch("Schedule.Service")
	if err != nil {
		return err
	}
	defer releaseDispatch(service)
	if _, err := invokeDispatch(service, "Connect", dispatchMethod); err != nil {
		return err
	}
	folderValue, err := invokeDispatch(service, "GetFolder", dispatchMethod, variantBSTR(`\`))
	if err != nil {
		return err
	}
	folder := variantDispatch(folderValue)
	if folder == nil {
		return errors.New("Task Scheduler root folder missing")
	}
	defer releaseDispatch(folder)
	return operation(folder)
}

func setupXMLText(text string) string {
	var escaped bytes.Buffer
	_ = xml.EscapeText(&escaped, []byte(text))
	return escaped.String()
}

func registerSetupTask(request SetupLaunchRequest, executable, path string) error {
	definition := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
<Principals><Principal id="Author"><UserId>%s</UserId><LogonType>InteractiveToken</LogonType><RunLevel>LeastPrivilege</RunLevel></Principal></Principals>
<Settings><MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy><DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries><StopIfGoingOnBatteries>false</StopIfGoingOnBatteries><AllowHardTerminate>true</AllowHardTerminate><StartWhenAvailable>false</StartWhenAvailable><RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable><Enabled>true</Enabled><Hidden>true</Hidden><ExecutionTimeLimit>PT%dS</ExecutionTimeLimit></Settings>
<Actions Context="Author"><Exec><Command>%s</Command><Arguments>%s</Arguments><WorkingDirectory>%s</WorkingDirectory></Exec></Actions></Task>`,
		setupXMLText(request.OwnerSID), request.TimeoutSeconds+15, setupXMLText(executable), setupXMLText("--setup-worker "+windows.EscapeArg(path)), setupXMLText(filepath.Dir(executable)))
	return withSetupTaskFolder(func(folder *iDispatch) error {
		value, err := invokeDispatch(folder, "RegisterTask", dispatchMethod, variantBSTR(request.TaskName), variantBSTR(definition), variantInt32(2), variantBSTR(request.OwnerSID), variantEmpty(), variantInt32(taskLogonInteractiveToken), variantEmpty())
		if object := variantDispatch(value); object != nil {
			releaseDispatch(object)
		}
		return err
	})
}

func deleteSetupTask(name string, stop bool) error {
	if !strings.HasPrefix(name, "AgentDock Setup Native ") {
		return errors.New("refusing unrelated task cleanup")
	}
	return withSetupTaskFolder(func(folder *iDispatch) error {
		if stop {
			value, err := invokeDispatch(folder, "GetTask", dispatchMethod, variantBSTR(name))
			if err == nil {
				if task := variantDispatch(value); task != nil {
					_, _ = invokeDispatch(task, "Stop", dispatchMethod, variantInt32(0))
					releaseDispatch(task)
				}
			}
		}
		_, err := invokeDispatch(folder, "DeleteTask", dispatchMethod, variantBSTR(name), variantInt32(0))
		return err
	})
}
