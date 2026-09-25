//go:build windows

package desktopruntime

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/uvwt/agentdock/internal/fs/atomicfile"
	processctl "github.com/uvwt/agentdock/internal/process"
	"github.com/uvwt/agentdock/internal/updateengine"
	"golang.org/x/sys/windows"
)

const runtimeHostStateFile = "runtime-host.json"

type runtimeHostProcess struct {
	PID     uint32 `json:"pid"`
	Created uint64 `json:"created"`
}
type runtimeHostState struct {
	SchemaVersion int                `json:"schema_version"`
	OriginHash    string             `json:"origin_hash"`
	Epoch         string             `json:"host_epoch"`
	Generation    string             `json:"generation"`
	Host          runtimeHostProcess `json:"host"`
	Core          runtimeHostProcess `json:"core"`
	Supervisor    runtimeHostProcess `json:"supervisor"`
}

func hostProcessIdentity(child *processctl.JobChild) runtimeHostProcess {
	if child == nil {
		return runtimeHostProcess{}
	}
	return runtimeHostProcess{PID: child.PID, Created: child.Created}
}
func writeRuntimeHostState(root string, state runtimeHostState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return atomicfile.Write(filepath.Join(root, runtimeHostStateFile), data, 0600)
}
func readRuntimeHostState(root string) (runtimeHostState, error) {
	var state runtimeHostState
	file, err := os.Open(filepath.Join(root, runtimeHostStateFile))
	if err != nil {
		return state, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, RuntimeHostFrameLimit+1))
	if err != nil {
		return state, err
	}
	if len(data) > RuntimeHostFrameLimit {
		return state, errors.New("invalid runtime host state size")
	}
	if err = json.Unmarshal(data, &state); err != nil {
		return state, err
	}
	epoch, err := hex.DecodeString(state.Epoch)
	if err != nil || len(epoch) != 16 || state.SchemaVersion != 1 || state.Generation == "" || len(state.Generation) > 128 || strings.ContainsAny(state.Generation, "/\\:\x00") || state.Host.PID == 0 || state.Host.Created == 0 {
		return state, errors.New("invalid runtime host identity")
	}
	return state, nil
}
func runtimePathsEqual(a, b string) bool {
	aa, ea := filepath.Abs(a)
	bb, eb := filepath.Abs(b)
	return ea == nil && eb == nil && strings.EqualFold(filepath.Clean(aa), filepath.Clean(bb))
}
func currentRuntimeHostIdentity() (runtimeHostProcess, error) {
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(windows.CurrentProcess(), &created, &exited, &kernel, &user); err != nil {
		return runtimeHostProcess{}, err
	}
	return runtimeHostProcess{PID: uint32(os.Getpid()), Created: uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime)}, nil
}

// Retain an original handle, not a PID that may later be reused. Query-only
// handles also work when the controller is the standard-user tray.
func openRuntimeHostProcess(identity runtimeHostProcess, binary string) (*processctl.JobChild, error) {
	if identity.PID == 0 {
		return nil, nil
	}
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, identity.PID)
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	child := &processctl.JobChild{Handle: handle, PID: identity.PID, Created: identity.Created}
	fail := func(err error) (*processctl.JobChild, error) { child.Close(); return nil, err }
	alive, err := child.Alive()
	if err != nil || !alive {
		return fail(err)
	}
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &created, &exited, &kernel, &user); err != nil {
		return fail(err)
	}
	if identity.Created != uint64(created.HighDateTime)<<32|uint64(created.LowDateTime) {
		return fail(nil)
	}
	buffer := make([]uint16, 32768)
	size := uint32(len(buffer))
	if err := windows.QueryFullProcessImageName(handle, 0, &buffer[0], &size); err != nil {
		return fail(err)
	}
	if !runtimePathsEqual(windows.UTF16ToString(buffer[:size]), binary) {
		return fail(errors.New("runtime process identity path mismatch"))
	}
	var token windows.Token
	if err := windows.OpenProcessToken(handle, windows.TOKEN_QUERY, &token); err != nil {
		return fail(err)
	}
	ownerSID, userErr := tokenUserSID(token)
	token.Close()
	current, currentErr := tokenUserSID(windows.GetCurrentProcessToken())
	if userErr != nil || currentErr != nil || ownerSID != current {
		return fail(errors.New("runtime process belongs to a different Windows user"))
	}
	return child, nil
}
func captureRuntimeHostProcesses(root string) ([]*processctl.JobChild, error) {
	state, err := readRuntimeHostState(root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	layout, err := updateengine.NewWindowsLayout(root)
	if err != nil {
		return nil, err
	}
	var children []*processctl.JobChild
	for _, item := range []struct {
		identity runtimeHostProcess
		path     string
	}{
		{state.Host, layout.TrayShim()}, {state.Core, layout.GenerationCore(state.Generation)}, {state.Supervisor, layout.GenerationCore(state.Generation)},
	} {
		child, err := openRuntimeHostProcess(item.identity, item.path)
		if err != nil {
			for _, c := range children {
				c.Close()
			}
			return nil, err
		}
		if child != nil {
			children = append(children, child)
		}
	}
	tunnel, tunnelErr := readRuntimeTunnelChild(root)
	if tunnelErr == nil && tunnel.Epoch == state.Epoch && tunnel.Generation == state.Generation && tunnel.Supervisor == state.Supervisor {
		manifest, _, err := loadDesktopManifest(root)
		if err == nil {
			child, openErr := openRuntimeHostProcess(tunnel.Child, manifest.CloudflaredBinary)
			err = openErr
			if child != nil {
				children = append(children, child)
			}
		}
		if err != nil {
			for _, child := range children {
				child.Close()
			}
			return nil, err
		}
	} else if tunnelErr != nil && !errors.Is(tunnelErr, os.ErrNotExist) {
		for _, child := range children {
			child.Close()
		}
		return nil, tunnelErr
	}
	return children, nil
}

type runtimeCoreHealth struct {
	OK         bool   `json:"ok"`
	Version    string `json:"version"`
	PID        uint32 `json:"process_id"`
	OriginHash string `json:"origin_hash"`
}

var runtimeHostHTTP = &http.Client{
	Transport:     &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: time.Second}).DialContext, MaxIdleConnsPerHost: 1, IdleConnTimeout: 10 * time.Second},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func readRuntimeCoreHealth(ctx context.Context, endpoint string) (runtimeCoreHealth, error) {
	var health runtimeCoreHealth
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || !net.ParseIP(parsed.Hostname()).IsLoopback() || parsed.Path != "/healthz" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return health, errors.New("owned Core health requires a literal loopback endpoint")
	}
	ctx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return health, err
	}
	response, err := runtimeHostHTTP.Do(request)
	if err != nil {
		return health, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return health, fmt.Errorf("Core health status %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, RuntimeHostFrameLimit+1))
	if err != nil {
		return health, err
	}
	if len(data) > RuntimeHostFrameLimit {
		return health, errors.New("Core health exceeds limit")
	}
	if err := json.Unmarshal(data, &health); err != nil {
		return health, err
	}
	if !health.OK || health.PID == 0 || health.Version == "" {
		return health, errors.New("Core health lacks owned process identity")
	}
	return health, nil
}
func waitCapturedCoreHealth(ctx context.Context, manifest Manifest, core *processctl.JobChild, generation, originHash string) error {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	for {
		alive, err := core.Alive()
		if err != nil {
			return err
		}
		if !alive {
			return errors.New("owned Core exited before health")
		}
		health, err := readRuntimeCoreHealth(ctx, manifest.HealthURL())
		if err == nil && health.PID == core.PID && strings.TrimPrefix(health.Version, "v") == strings.TrimPrefix(generation, "v") && health.OriginHash == originHash {
			if alive, aliveErr := core.Alive(); aliveErr == nil && alive {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("owned Core health: %w", ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}
func runtimeHostHealthy(ctx context.Context, root, epoch, generation string) bool {
	state, err := readRuntimeHostState(root)
	if err != nil {
		return false
	}
	if epoch != "" && state.Epoch != epoch || generation != "" && state.Generation != generation {
		return false
	}
	layout, err := updateengine.NewWindowsLayout(root)
	if err != nil {
		return false
	}
	host, err := openRuntimeHostProcess(state.Host, layout.TrayShim())
	if err != nil || host == nil {
		return false
	}
	defer host.Close()
	core, err := openRuntimeHostProcess(state.Core, layout.GenerationCore(state.Generation))
	if err != nil || core == nil {
		return false
	}
	defer core.Close()
	manifest, _, err := loadDesktopManifest(root)
	if err != nil {
		return false
	}
	health, err := readRuntimeCoreHealth(ctx, manifest.HealthURL())
	latest, latestErr := readRuntimeHostState(root)
	hostAlive, hostErr := host.Alive()
	coreAlive, coreErr := core.Alive()
	return err == nil && latestErr == nil && latest == state && hostErr == nil && coreErr == nil && hostAlive && coreAlive && health.PID == core.PID && strings.TrimPrefix(health.Version, "v") == strings.TrimPrefix(state.Generation, "v") && health.OriginHash == state.OriginHash
}
func waitRuntimeHostHealth(ctx context.Context, root, epoch, generation string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	for {
		if runtimeHostHealthy(ctx, root, epoch, generation) {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("elevated_unavailable: owned runtime health: %w", ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func runtimeHostServiceStatus(ctx context.Context, root string, manifest Manifest) (ServiceStatus, error) {
	task, err := nativeRuntimeTaskAction(ctx, root, manifest, "status")
	if err != nil {
		return ServiceStatus{}, err
	}
	result := ServiceStatus{StartupEnabled: task.Enabled}
	state, err := readRuntimeHostState(root)
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	layout, err := updateengine.NewWindowsLayout(root)
	if err != nil {
		return result, err
	}
	host, err := openRuntimeHostProcess(state.Host, layout.TrayShim())
	if err != nil || host == nil {
		return result, err
	}
	defer host.Close()
	core, err := openRuntimeHostProcess(state.Core, layout.GenerationCore(state.Generation))
	if err != nil || core == nil {
		return result, err
	}
	defer core.Close()
	result.Running = true
	result.Healthy = runtimeHostHealthy(ctx, root, state.Epoch, state.Generation)
	return result, nil
}
