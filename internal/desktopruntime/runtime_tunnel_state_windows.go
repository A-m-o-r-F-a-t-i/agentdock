//go:build windows

package desktopruntime

import (
	"context"
	"encoding/json"
	"errors"
	processctl "github.com/uvwt/agentdock/internal/process"
	"github.com/uvwt/agentdock/internal/updateengine"
	"golang.org/x/sys/windows"
	"io"
	"os"
	"path/filepath"
)

const runtimeTunnelStateFile = "runtime-tunnel-child.json"

type runtimeTunnelChildState struct {
	SchemaVersion int                `json:"schema_version"`
	Epoch         string             `json:"host_epoch"`
	Generation    string             `json:"generation"`
	Supervisor    runtimeHostProcess `json:"supervisor"`
	Child         runtimeHostProcess `json:"child"`
	Phase         string             `json:"phase"`
	Ready         bool               `json:"ready"`
}

func captureStartedTunnelProcess(pid int, binary string) (*processctl.JobChild, error) {
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return nil, err
	}
	var created, exited, kernel, user windows.Filetime
	err = windows.GetProcessTimes(handle, &created, &exited, &kernel, &user)
	windows.CloseHandle(handle)
	if err != nil {
		return nil, err
	}
	identity := runtimeHostProcess{PID: uint32(pid), Created: uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime)}
	child, err := openRuntimeHostProcess(identity, binary)
	if err == nil && child == nil {
		err = errCloudflaredChildExit
	}
	return child, err
}
func writeRuntimeTunnelChild(root string, state runtimeTunnelChildState) error {
	return writeSetupJSON(filepath.Join(root, runtimeTunnelStateFile), state)
}
func readRuntimeTunnelChild(root string) (runtimeTunnelChildState, error) {
	var state runtimeTunnelChildState
	f, err := os.Open(filepath.Join(root, runtimeTunnelStateFile))
	if err != nil {
		return state, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, RuntimeHostFrameLimit+1))
	if err != nil {
		return state, err
	}
	if len(data) > RuntimeHostFrameLimit {
		return state, errors.New("runtime Tunnel state exceeds limit")
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return state, err
	}
	if state.SchemaVersion != 1 || !hostedRuntimeEnvironmentValid(state.Epoch, state.Generation) || state.Supervisor.PID == 0 || state.Supervisor.Created == 0 || state.Child.PID == 0 || state.Child.Created == 0 {
		return state, errors.New("invalid runtime Tunnel child identity")
	}
	return state, nil
}
func runtimeHostTunnelStatus(ctx context.Context, run tunnelRuntime) (TunnelStatus, error) {
	result := TunnelStatus{Mode: run.mode, Provider: PublicAccessProviderNone}
	if run.mode == "quick" || run.mode == "named" {
		result.Provider = PublicAccessProviderCloudflare
	}
	task, err := nativeRuntimeTaskAction(ctx, run.root, run.manifest, "status")
	if err != nil {
		return result, err
	}
	result.StartupEnabled = task.Enabled
	owner, err := readRuntimeHostState(run.root)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	layout, err := updateengine.NewWindowsLayout(run.root)
	if err != nil {
		return result, err
	}
	host, err := openRuntimeHostProcess(owner.Host, layout.TrayShim())
	if err != nil || host == nil {
		return result, err
	}
	defer host.Close()
	childState, err := readRuntimeTunnelChild(run.root)
	if errors.Is(err, os.ErrNotExist) {
		result.Phase = "health_pending"
		return result, nil
	}
	if err != nil {
		return result, err
	}
	if childState.Epoch != owner.Epoch || childState.Generation != owner.Generation || childState.Supervisor != owner.Supervisor {
		return result, nil
	}
	supervisor, err := openRuntimeHostProcess(owner.Supervisor, layout.GenerationCore(owner.Generation))
	if err != nil || supervisor == nil {
		return result, err
	}
	defer supervisor.Close()
	child, err := openRuntimeHostProcess(childState.Child, run.manifest.CloudflaredBinary)
	if err != nil || child == nil {
		return result, err
	}
	defer child.Close()
	result.Running = true
	result.Phase = childState.Phase
	result.LocalReady = runtimeHostHealthy(ctx, run.root, owner.Epoch, owner.Generation)
	public, err := readTunnelPublicURL(run)
	if err != nil {
		return result, err
	}
	result.Ready = childState.Ready && result.LocalReady && public != ""
	if result.Ready {
		result.PublicURL = public
	}
	return result, nil
}
