//go:build windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"

	"github.com/uvwt/agentdock/internal/desktopruntime"
	"github.com/uvwt/agentdock/internal/fs/processlock"
	processctl "github.com/uvwt/agentdock/internal/process"
	"github.com/uvwt/agentdock/internal/updateengine"
)

const (
	setupRuntimeHostFlag = "--setup-runtime-host"
	taskCoreHostFlag     = "--run-core-task"
)

func main() {
	if len(os.Args) > 1 && strings.EqualFold(strings.TrimSpace(os.Args[1]), taskCoreHostFlag) {
		code, err := runTaskCoreHost(os.Args[2:])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(code)
	}
	if len(os.Args) > 1 && strings.EqualFold(strings.TrimSpace(os.Args[1]), setupRuntimeHostFlag) {
		code, err := runSetupRuntimeHost(os.Args[2:])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(code)
	}

	if len(os.Args) == 3 && os.Args[1] == "--setup-exec" {
		code, err := desktopruntime.RunSetupExecutor(os.Args[2])
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(code)
	}
	if err := run(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) == 3 && (os.Args[1] == "--setup-launch" || os.Args[1] == "--setup-worker") {
		return desktopruntime.RunSetupLauncher(os.Args[2], os.Args[1] == "--setup-worker")
	}
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve AgentDock stable entry: %w", err)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return fmt.Errorf("resolve AgentDock stable entry path: %w", err)
	}
	root := filepath.Dir(filepath.Dir(executable))
	store, err := updateengine.NewStore(root)
	if err != nil {
		return err
	}
	layout, err := updateengine.NewWindowsLayout(root)
	if err != nil {
		return err
	}
	tray := strings.EqualFold(filepath.Base(executable), updateengine.StableTrayShimName)
	active, err := resolveActiveWithRecovery(root, store, layout, !tray && coreLaunchRequiresParentLifetime(os.Args[1:]))
	if err != nil {
		return err
	}

	target := layout.GenerationCore(active.ActiveVersion)
	if tray {
		target = layout.GenerationTray(active.ActiveVersion)
	}
	if info, err := os.Stat(target); err != nil || info.IsDir() {
		return fmt.Errorf("AgentDock active generation is incomplete: %s", target)
	}
	if tray || !policyRecoveryCommand(os.Args[1:]) {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		if err := desktopruntime.CheckExecutionCompatibility(ctx, root, layout.GenerationCore(active.ActiveVersion)); err != nil {
			return err
		}
	}

	command := exec.Command(target, os.Args[1:]...)
	command.Dir = root
	if !tray || trayRequiresWait(os.Args[1:]) {
		command.Stdin = os.Stdin
		command.Stdout = os.Stdout
		command.Stderr = os.Stderr

		var runErr error
		if shimChildRequiresParentLifetime(tray, os.Args[1:]) {
			// Scheduled Task owns the stable shim, not its active-generation child. Keep
			// the long-lived child tree in a kill-on-close Job owned by this shim so ending
			// the task cannot orphan either the generation tray host or the generation Core.
			if err := command.Start(); err != nil {
				return fmt.Errorf("start AgentDock active generation: %w", err)
			}
			controller, err := processctl.Attach(command)
			if err != nil {
				_ = command.Process.Kill()
				_ = command.Wait()
				return fmt.Errorf("supervise AgentDock active generation: %w", err)
			}
			runErr = command.Wait()
			closeErr := controller.Close()
			if runErr == nil && closeErr != nil {
				return fmt.Errorf("release AgentDock active generation supervisor: %w", closeErr)
			}
		} else {
			runErr = command.Run()
		}
		if runErr != nil {
			var exitErr *exec.ExitError
			if errors.As(runErr, &exitErr) {
				os.Exit(exitErr.ExitCode())
			}
			return fmt.Errorf("run AgentDock active generation: %w", runErr)
		}
		return nil
	}

	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
	if err := command.Start(); err != nil {
		return fmt.Errorf("start AgentDock tray generation: %w", err)
	}
	return command.Process.Release()
}

// Only inspection and recovery commands may reach an old Core with policy
// state. An old tray can auto-start Core and is checked independently.
func policyRecoveryCommand(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "version":
		return len(args) == 1 || len(args) == 2 && args[1] == "--json"
	case "uninstall":
		return true
	case "install":
		if len(args) < 2 {
			return false
		}
		switch args[1] {
		case "inspect", "--engine-ready", "restore-files", "abandon", "detach-engine":
			return true
		}
	case "service", "tunnel":
		return len(args) >= 2 && (args[1] == "stop" || args[1] == "status")
	}
	return false
}

func coreLaunchRequiresParentLifetime(args []string) bool {
	return len(args) >= 2 &&
		strings.EqualFold(strings.TrimSpace(args[0]), "service") &&
		strings.EqualFold(strings.TrimSpace(args[1]), "launch-core")
}

func shimChildRequiresParentLifetime(tray bool, args []string) bool {
	if coreLaunchRequiresParentLifetime(args) {
		return true
	}
	if !tray {
		return false
	}
	for _, argument := range args {
		if strings.EqualFold(strings.TrimSpace(argument), "--run-core-task") {
			return true
		}
	}
	return false
}

func resolveActiveWithRecovery(root string, store *updateengine.Store, layout updateengine.WindowsLayout, allowInstallerHost ...bool) (updateengine.ActiveVersion, error) {
	active, err := store.ReadActive()
	if err != nil {
		return updateengine.ActiveVersion{}, fmt.Errorf("read AgentDock active version: %w", err)
	}

	// An elevated Core task must enter through the stable, job-owning shim while
	// Installer Engine is synchronously verifying its trial. Only that service
	// host entry may use a matching live install journal; ordinary commands and
	// abandoned trials retain the fail-closed behavior below.
	if active.State == updateengine.StateTrial && len(allowInstallerHost) > 0 && allowInstallerHost[0] {
		live, err := liveInstallerTrial(root, active)
		if err != nil {
			return updateengine.ActiveVersion{}, err
		}
		if live {
			return active, nil
		}
	}

	transaction, transactionErr := store.ReadTransaction()
	if transactionErr != nil {
		if active.State == updateengine.StateTrial {
			return updateengine.ActiveVersion{}, fmt.Errorf("active generation is still a trial without an authorized live installer host; refusing ordinary launch: %w", transactionErr)
		}
		return active, nil
	}
	pendingTrial := transaction.State == updateengine.StateTrial || transaction.State == updateengine.StateRollingBack
	if !pendingTrial && active.State != updateengine.StateTrial {
		return active, nil
	}
	if !pendingTrial {
		return updateengine.ActiveVersion{}, fmt.Errorf("active generation is trial but transaction %s is %s", transaction.TransactionID, transaction.State)
	}
	if transaction.Platform != "windows" || transaction.Windows == nil {
		return updateengine.ActiveVersion{}, errors.New("pending Windows generation transaction has no Windows plan")
	}
	if active.State == updateengine.StateTrial && active.TransactionID != transaction.TransactionID {
		return updateengine.ActiveVersion{}, fmt.Errorf("active trial transaction %s does not match journal %s", active.TransactionID, transaction.TransactionID)
	}

	// The Arbiter journals state=trial before it swaps the active pointer. A crash can therefore
	// leave either (a) active=trial target or (b) active=committed source with a pending trial
	// journal. In both cases a held OS lock means the original source Arbiter is still alive;
	// once the kernel releases the lock, the source known-good Arbiter performs conservative rollback.
	lockPath := filepath.Join(root, "update", "transaction.lock")
	lock, acquired, err := processlock.TryAcquire(lockPath)
	if err != nil {
		return updateengine.ActiveVersion{}, fmt.Errorf("probe update transaction lock: %w", err)
	}
	if !acquired {
		return active, nil
	}
	if err := lock.Release(); err != nil {
		return updateengine.ActiveVersion{}, fmt.Errorf("release update transaction probe lock: %w", err)
	}

	sourceVersion := updateengine.NormalizeVersion(transaction.SourceVersion)
	if sourceVersion == "" {
		return updateengine.ActiveVersion{}, errors.New("interrupted update trial has no source generation")
	}
	arbiterPath := layout.GenerationArbiter(sourceVersion)
	command := exec.Command(arbiterPath, "--root", root, "--transaction-id", transaction.TransactionID)
	command.Dir = root
	var recoveryStdout, recoveryStderr bytes.Buffer
	command.Stdout = &recoveryStdout
	command.Stderr = &recoveryStderr
	runErr := command.Run()
	result, resultErr := store.ReadResult(transaction.TransactionID)
	if resultErr != nil || (result.State != updateengine.StateRolledBack && result.State != updateengine.StateCommitted) {
		if runErr != nil {
			details := strings.TrimSpace(recoveryStderr.String())
			if details == "" {
				details = strings.TrimSpace(recoveryStdout.String())
			}
			if details != "" {
				return updateengine.ActiveVersion{}, fmt.Errorf("recover interrupted AgentDock update: %w: %s", runErr, details)
			}
			return updateengine.ActiveVersion{}, fmt.Errorf("recover interrupted AgentDock update: %w", runErr)
		}
		return updateengine.ActiveVersion{}, fmt.Errorf("recover interrupted AgentDock update did not reach a safe terminal result: %v", resultErr)
	}
	recovered, err := store.ReadActive()
	if err != nil {
		return updateengine.ActiveVersion{}, fmt.Errorf("read recovered AgentDock active version: %w", err)
	}
	return recovered, nil
}

func trayRequiresWait(args []string) bool {
	if len(args) == 0 {
		return false
	}
	for _, arg := range args {
		if !strings.EqualFold(strings.TrimSpace(arg), "--background") {
			return true
		}
	}
	return false
}

// liveInstallerTrial never commits, repairs, or discards installation state.
func liveInstallerTrial(root string, active updateengine.ActiveVersion) (bool, error) {
	data, err := os.ReadFile(filepath.Join(root, "install", "transaction.json"))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read installer trial: %w", err)
	}
	var transaction struct {
		SchemaVersion int                `json:"schema_version"`
		TransactionID string             `json:"transaction_id"`
		Action        string             `json:"action"`
		State         updateengine.State `json:"state"`
		Phase         string             `json:"phase"`
		TargetVersion string             `json:"target_version"`
		InstallRoot   string             `json:"install_root"`
		RuntimeRoot   string             `json:"runtime_root"`
	}
	if err := json.Unmarshal(data, &transaction); err != nil {
		return false, fmt.Errorf("decode installer trial: %w", err)
	}
	if transaction.SchemaVersion != 1 || transaction.TransactionID == "" ||
		transaction.TransactionID != active.TransactionID ||
		(transaction.Action != "install" && transaction.Action != "repair") ||
		transaction.State != updateengine.StateTrial || active.State != updateengine.StateTrial ||
		(transaction.Phase != "start" && transaction.Phase != "health") ||
		updateengine.NormalizeVersion(transaction.TargetVersion) != updateengine.NormalizeVersion(active.ActiveVersion) ||
		!strings.EqualFold(filepath.Clean(transaction.InstallRoot), filepath.Clean(root)) ||
		!strings.EqualFold(filepath.Clean(transaction.RuntimeRoot), filepath.Clean(root)) {
		return false, nil
	}
	lock, acquired, err := processlock.TryAcquire(filepath.Join(root, "install", "transaction.lock"))
	if err != nil {
		return false, fmt.Errorf("probe installer trial lock: %w", err)
	}
	if acquired {
		return false, lock.Release()
	}
	return true, nil
}

func sameWindowsPath(left, right string) bool {
	left, leftErr := filepath.Abs(strings.TrimSpace(left))
	right, rightErr := filepath.Abs(strings.TrimSpace(right))
	return leftErr == nil && rightErr == nil && strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
}
