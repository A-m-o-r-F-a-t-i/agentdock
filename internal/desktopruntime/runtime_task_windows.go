//go:build windows

package desktopruntime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/uvwt/agentdock/internal/updateengine"
	"golang.org/x/sys/windows"
)

type runtimeTaskContract struct {
	Name, UserSID, Path, Arguments, WorkingDirectory string
	LogonType, RunLevel, Actions, ActionType         int
}
type runtimeTaskState struct {
	Enabled bool `json:"enabled"`
	Running bool `json:"running"`
}

func validateRuntimeTaskContract(root, name, sid string, contract runtimeTaskContract) error {
	layout, err := updateengine.NewWindowsLayout(root)
	if err != nil {
		return err
	}
	if name != "AgentDock" || contract.Name != name || !strings.EqualFold(contract.UserSID, sid) || contract.LogonType != taskLogonInteractiveToken || contract.RunLevel != 1 || contract.Actions != 1 || contract.ActionType != 0 || !runtimePathsEqual(contract.Path, layout.TrayShim()) {
		return errors.New("task name, principal, privilege or stable action does not match this runtime")
	}
	if contract.WorkingDirectory != "" && !runtimePathsEqual(contract.WorkingDirectory, root) {
		return errors.New("task working directory differs from runtime root")
	}
	canonical := `--run-core-task --runtime-root "` + filepath.Clean(root) + `"`
	if !strings.EqualFold(contract.Arguments, canonical) {
		return errors.New("task arguments are not the exact native runtime host action")
	}
	return nil
}

// withManagedRuntimeTask is the single normal-runtime Task Scheduler adapter.
// It never requests elevation, invokes a shell, registers a task, or repairs an
// unrecognised task. The separate explicit TaskAdmin transition owns setup.
func withManagedRuntimeTask(ctx context.Context, root string, manifest Manifest, run func(*iDispatch, runtimeTaskState, string) error) (result runtimeTaskState, resultErr error) {
	defer func() {
		if resultErr != nil {
			resultErr = fmt.Errorf("elevated_unavailable: %w", resultErr)
		}
	}()
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if !manifest.UsesScheduledTask() {
		return result, errors.New("runtime is not configured for a managed elevated task")
	}
	name := strings.TrimSpace(manifest.AgentDockTaskName)
	if name != "AgentDock" {
		return result, errors.New("unrecognised elevated runtime task name")
	}
	sid, err := tokenUserSID(windows.GetCurrentProcessToken())
	if err != nil {
		return result, err
	}
	owner, err := os.ReadFile(filepath.Join(root, credentialOwnerSIDFile))
	if err != nil && !os.IsNotExist(err) {
		return result, err
	}
	if len(owner) == 0 {
		if err := validateCredentialRecoveryUser(filepath.Join(root, "auth-token.dpapi")); err != nil {
			return result, err
		}
	} else if !strings.EqualFold(strings.TrimSpace(string(owner)), sid) {
		return result, errors.New("runtime credential owner differs from controller user")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	hr, _, _ := procCoInitializeEx.Call(0, coInitApartmentThreaded)
	if hr != 0 && hr != 1 && hr != 0x80010106 {
		return result, fmt.Errorf("Task Scheduler COM init 0x%x", hr)
	}
	if hr == 0 || hr == 1 {
		defer procCoUninitialize.Call()
	}
	service, err := createDispatch("Schedule.Service")
	if err != nil {
		return result, err
	}
	defer releaseDispatch(service)
	if _, err = invokeDispatch(service, "Connect", dispatchMethod); err != nil {
		return result, err
	}
	getObject := func(object *iDispatch, property string, args ...variant) (*iDispatch, error) {
		flags := uint16(dispatchPropertyGet)
		if len(args) > 0 {
			flags = dispatchMethod
		}
		value, err := invokeDispatch(object, property, flags, args...)
		if err != nil {
			return nil, err
		}
		child := variantDispatch(value)
		if child == nil {
			return nil, fmt.Errorf("missing Task Scheduler %s", property)
		}
		return child, nil
	}
	folder, err := getObject(service, "GetFolder", variantBSTR(`\`))
	if err != nil {
		return result, err
	}
	defer releaseDispatch(folder)
	task, err := getObject(folder, "GetTask", variantBSTR(name))
	if err != nil {
		return result, err
	}
	defer releaseDispatch(task)
	definition, err := getObject(task, "Definition")
	if err != nil {
		return result, err
	}
	defer releaseDispatch(definition)
	principal, err := getObject(definition, "Principal")
	if err != nil {
		return result, err
	}
	defer releaseDispatch(principal)
	actions, err := getObject(definition, "Actions")
	if err != nil {
		return result, err
	}
	defer releaseDispatch(actions)
	var contract runtimeTaskContract
	getString := func(object *iDispatch, name string, target *string) error {
		v, e := invokeDispatch(object, name, dispatchPropertyGet)
		if e == nil {
			*target = variantString(v)
		}
		return e
	}
	getInt := func(object *iDispatch, name string, target *int) error {
		v, e := invokeDispatch(object, name, dispatchPropertyGet)
		if e == nil {
			*target = int(variantInt(v))
		}
		return e
	}
	for _, read := range []func() error{
		func() error { return getString(task, "Name", &contract.Name) }, func() error { return getString(principal, "UserId", &contract.UserSID) },
		func() error { return getInt(principal, "LogonType", &contract.LogonType) }, func() error { return getInt(principal, "RunLevel", &contract.RunLevel) }, func() error { return getInt(actions, "Count", &contract.Actions) },
	} {
		if err := read(); err != nil {
			return result, err
		}
	}
	contract.UserSID, err = windowsUserSID(contract.UserSID)
	if err != nil {
		return result, err
	}
	if contract.Actions != 1 {
		return result, errors.New("managed runtime task must have exactly one action")
	}
	value, err := invokeDispatch(actions, "Item", dispatchPropertyGet, variantInt32(1))
	if err != nil {
		return result, err
	}
	action := variantDispatch(value)
	if action == nil {
		return result, errors.New("managed task action missing")
	}
	defer releaseDispatch(action)
	for _, read := range []func() error{
		func() error { return getInt(action, "Type", &contract.ActionType) }, func() error { return getString(action, "Path", &contract.Path) },
		func() error { return getString(action, "Arguments", &contract.Arguments) }, func() error { return getString(action, "WorkingDirectory", &contract.WorkingDirectory) },
	} {
		if err := read(); err != nil {
			return result, err
		}
	}
	if err := validateRuntimeTaskContract(root, name, sid, contract); err != nil {
		return result, err
	}
	var enabled, state int
	if err := getInt(task, "Enabled", &enabled); err != nil {
		return result, err
	}
	if err := getInt(task, "State", &state); err != nil {
		return result, err
	}
	if state == 0 {
		return result, errors.New("Task Scheduler reports unknown runtime state")
	}
	result = runtimeTaskState{Enabled: enabled != 0, Running: state == 4 || state == 2}
	if run != nil {
		resultErr = run(task, result, sid)
	}
	return result, resultErr
}
func nativeRuntimeTaskAction(ctx context.Context, root string, manifest Manifest, action string) (runtimeTaskState, error) {
	return withManagedRuntimeTask(ctx, root, manifest, func(task *iDispatch, state runtimeTaskState, sid string) (resultErr error) {
		if err := ctx.Err(); err != nil {
			return err
		}
		switch action {
		case "status", "validate":
			return nil
		case "enable", "disable":
			_, err := invokeDispatch(task, "Enabled", dispatchPropertyPut, variantBool(action == "enable"))
			return err
		case "stop":
			children, err := captureRuntimeHostProcesses(root)
			if err != nil {
				return err
			}
			defer func() {
				for _, child := range children {
					child.Close()
				}
			}()
			instances, err := runtimeTaskInstanceIDs(task)
			if err != nil {
				return err
			}
			if state.Running || len(instances) > 0 {
				if err := runScheduledTaskCommand(ctx, "/End", "/TN", scheduledTaskPath(manifest.AgentDockTaskName)); err != nil {
					return err
				}
			}
			wait, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			for {
				current, err := runtimeTaskInstanceIDs(task)
				if err != nil {
					return err
				}
				remaining := false
				for id := range instances {
					if current[id] {
						remaining = true
						break
					}
				}
				if !remaining {
					break
				}
				select {
				case <-wait.Done():
					return fmt.Errorf("previous task instance did not end: %w", wait.Err())
				case <-time.After(50 * time.Millisecond):
				}
			}
			for _, child := range children {
				if _, err := child.Wait(wait); err != nil {
					return fmt.Errorf("owned runtime did not stop: %w", err)
				}
			}
			return nil
		case "start":
			if state.Running {
				return nil
			}
			if !state.Enabled {
				if _, err := invokeDispatch(task, "Enabled", dispatchPropertyPut, variantBool(true)); err != nil {
					return err
				}
				defer func() {
					_, err := invokeDispatch(task, "Enabled", dispatchPropertyPut, variantBool(false))
					resultErr = errors.Join(resultErr, err)
				}()
			}
			return runScheduledTaskCommand(ctx, "/Run", "/TN", scheduledTaskPath(manifest.AgentDockTaskName))
		default:
			return fmt.Errorf("unsupported managed runtime action %q", action)
		}
	})
}

// One shared sequence for external start/stop/restart and configuration handoff.
// All callbacks are production operations; unit tests replace only this small boundary.
type scheduledActionOps struct {
	Validate func(context.Context) error
	End      func(context.Context) error
	Run      func(context.Context) error
}

func applyScheduledAction(ctx context.Context, action string, ops scheduledActionOps) error {
	if action != "start" && action != "stop" && action != "restart" && action != "regenerate" {
		return fmt.Errorf("unsupported scheduled action %q", action)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ops.Validate(ctx); err != nil {
		return err
	}
	if action != "start" {
		if err := ops.End(ctx); err != nil {
			return err
		}
	}
	if action == "stop" {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return ops.Run(ctx)
}
func elevatedRuntimeActionLocked(ctx context.Context, root string, manifest Manifest, action string) error {
	call := func(action string) func(context.Context) error {
		return func(ctx context.Context) error {
			_, err := nativeRuntimeTaskAction(ctx, root, manifest, action)
			return err
		}
	}
	return applyScheduledAction(ctx, action, scheduledActionOps{Validate: call("validate"), End: call("stop"), Run: call("start")})
}
func runtimeTaskInstanceIDs(task *iDispatch) (map[string]bool, error) {
	value, err := invokeDispatch(task, "GetInstances", dispatchMethod, variantInt32(0))
	if err != nil {
		return nil, err
	}
	collection := variantDispatch(value)
	if collection == nil {
		return nil, errors.New("Task Scheduler instance collection missing")
	}
	defer releaseDispatch(collection)
	countValue, err := invokeDispatch(collection, "Count", dispatchPropertyGet)
	if err != nil {
		return nil, err
	}
	count := variantInt(countValue)
	if count < 0 || count > 32 {
		return nil, errors.New("unexpected task instance count")
	}
	result := map[string]bool{}
	for i := int64(1); i <= count; i++ {
		value, err := invokeDispatch(collection, "Item", dispatchPropertyGet, variantInt32(int32(i)))
		if err != nil {
			return nil, err
		}
		instance := variantDispatch(value)
		if instance == nil {
			return nil, errors.New("missing task instance")
		}
		idValue, err := invokeDispatch(instance, "InstanceGuid", dispatchPropertyGet)
		releaseDispatch(instance)
		if err != nil {
			return nil, err
		}
		id := variantString(idValue)
		if id == "" {
			return nil, errors.New("task instance identity missing")
		}
		result[id] = true
	}
	return result, nil
}
func elevatedRuntimeAction(ctx context.Context, root string, manifest Manifest, action string) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	ctx, finish, err := tunnelActionContext(ctx, root, action == "stop")
	if err != nil {
		return err
	}
	defer finish()
	release, err := acquireTunnelOperation(ctx, root)
	if err != nil {
		return err
	}
	defer release()
	return elevatedRuntimeActionLocked(ctx, root, manifest, action)
}

// ValidateManagedRuntimeTask is read-only. Setup uses the exact same owner and
// action contract as ordinary runtime commands, after staging its manifest.
func ValidateManagedRuntimeTask(ctx context.Context, root string) (bool, bool, error) {
	manifest, root, err := loadDesktopManifest(root)
	if err != nil {
		return false, false, err
	}
	state, err := nativeRuntimeTaskAction(ctx, root, manifest, "validate")
	return state.Enabled, state.Running, err
}
