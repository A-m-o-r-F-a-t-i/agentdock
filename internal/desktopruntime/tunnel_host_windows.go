//go:build windows

package desktopruntime

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"github.com/uvwt/agentdock/internal/updateengine"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type tunnelHostContextKey struct{}
type tunnelHostClient struct {
	mu                      sync.Mutex
	closed                  atomic.Bool
	input, output           *os.File
	reader                  *bufio.Reader
	root, epoch, generation string
	requestID               uint64
}

func prepareTunnelHostContext(ctx context.Context, root string, hostControlled bool) (context.Context, func(), error) {
	manifest, _, err := loadDesktopManifest(root)
	if err != nil {
		return ctx, func() {}, err
	}
	if !hostControlled {
		if manifest.UsesScheduledTask() {
			return ctx, func() {}, errors.New("elevated_unavailable: an elevated Tunnel must be owned by the native runtime host")
		}
		return ctx, func() {}, nil
	}
	epoch, generation := os.Getenv(runtimeHostEpochEnv), os.Getenv(runtimeHostGenerationEnv)
	if !manifest.UsesScheduledTask() || !hostedRuntimeEnvironmentValid(epoch, generation) {
		return ctx, func() {}, errors.New("invalid private runtime host binding")
	}
	for _, file := range []*os.File{os.Stdin, os.Stdout} {
		kind, err := windows.GetFileType(windows.Handle(file.Fd()))
		if err != nil || kind != windows.FILE_TYPE_PIPE {
			return ctx, func() {}, errors.New("runtime host control requires inherited anonymous pipes")
		}
		if err := windows.SetHandleInformation(windows.Handle(file.Fd()), windows.HANDLE_FLAG_INHERIT, 0); err != nil {
			return ctx, func() {}, err
		}
	}
	if err := verifyInheritedRuntimeHost(root, epoch, generation); err != nil {
		return ctx, func() {}, err
	}
	client := &tunnelHostClient{input: os.Stdin, output: os.Stdout, reader: bufio.NewReaderSize(os.Stdin, RuntimeHostFrameLimit+1), root: root, epoch: epoch, generation: generation}
	return context.WithValue(ctx, tunnelHostContextKey{}, client), client.close, nil
}
func (client *tunnelHostClient) close() {
	if client.closed.CompareAndSwap(false, true) {
		client.input.Close()
		client.output.Close()
	}
}
func tunnelHostFromContext(ctx context.Context) *tunnelHostClient {
	client, _ := ctx.Value(tunnelHostContextKey{}).(*tunnelHostClient)
	return client
}
func (client *tunnelHostClient) replaceOrigin(ctx context.Context, origin string) error {
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.closed.Load() {
		return errors.New("runtime host control channel is closed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	client.requestID++
	request := RuntimeHostRequest{SchemaVersion: 1, Op: RuntimeHostReplaceOrigin, RequestID: client.requestID, Epoch: client.epoch, Generation: client.generation, Origin: origin}
	if err := request.Validate(client.epoch, client.generation, client.requestID); err != nil {
		return err
	}
	type result struct {
		ack RuntimeHostAck
		err error
	}
	done := make(chan result, 1)
	go func() {
		var ack RuntimeHostAck
		err := WriteRuntimeHostFrame(client.output, request)
		if err == nil {
			err = ReadRuntimeHostFrame(client.reader, &ack)
		}
		done <- result{ack, err}
	}()
	wait, cancel := context.WithTimeout(ctx, 55*time.Second)
	defer cancel()
	select {
	case <-wait.Done():
		client.close()
		<-done
		return wait.Err()
	case response := <-done:
		ack := response.ack
		if response.err != nil || ack.SchemaVersion != 1 || ack.RequestID != request.RequestID || ack.Epoch != client.epoch || ack.Generation != client.generation {
			client.close()
			return errors.New("runtime host rejected or lost the matching Core replacement acknowledgement")
		}
		if !ack.OK {
			return errors.New("runtime host Core replacement is not healthy: " + ack.Error)
		}
		if ack.CorePID == 0 {
			return errors.New("host acknowledgement has no Core identity")
		}
		if err := ctx.Err(); err != nil {
			client.close()
			return err
		}
		if !runtimeHostHealthy(ctx, client.root, client.epoch, client.generation) {
			client.close()
			return errors.New("runtime host acknowledgement no longer has a healthy owned Core")
		}
		return nil
	}
}
func waitCloudflaredCoreHealth(ctx context.Context, run tunnelRuntime) error {
	if client := tunnelHostFromContext(ctx); client != nil {
		if client.closed.Load() {
			return errors.New("runtime host control channel is closed")
		}
		return waitRuntimeHostHealth(ctx, run.root, client.epoch, client.generation)
	}
	// Standard mode still refuses another process's generic 200 response. The
	// supervisor executable is the captured generation; Core uses the same image.
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	wait, cancel := context.WithCancel(ctx)
	defer cancel()
	for {
		health, err := readRuntimeCoreHealth(wait, run.localOrigin()+"/healthz")
		if err == nil {
			path, pathErr := queryProcessPath(health.PID)
			origin, originErr := readTrimmedText(run.files.serverURL)
			if pathErr == nil && originErr == nil && runtimePathsEqual(path, binary) && health.OriginHash == runtimeOriginDigest(origin) {
				return nil
			}
		}
		select {
		case <-wait.Done():
			return fmt.Errorf("Core is not ready before cloudflared creation: %w", wait.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// A flag or environment variable does not authorize a supervisor. Its actual
// parent must be the live recorded stable host; inherited pipe ends were also
// verified above and are no longer inheritable by cloudflared.
func verifyInheritedRuntimeHost(root, epoch, generation string) error {
	state, err := readRuntimeHostState(root)
	if err != nil {
		return err
	}
	if state.Epoch != epoch || state.Generation != generation {
		return errors.New("private host identity mismatch")
	}
	layout, err := updateengine.NewWindowsLayout(root)
	if err != nil {
		return err
	}
	parent, err := openRuntimeHostProcess(state.Host, layout.TrayShim())
	if err != nil {
		return err
	}
	if parent == nil {
		return errors.New("private host is not alive")
	}
	defer parent.Close()
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	if !runtimePathsEqual(executable, layout.GenerationCore(generation)) {
		return errors.New("private supervisor is not in its host generation")
	}
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ProcessEntry32{}
	entry.Size = uint32(unsafe.Sizeof(entry))
	for err = windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		if entry.ProcessID == uint32(os.Getpid()) {
			if entry.ParentProcessID != state.Host.PID {
				return errors.New("private supervisor was not created by its recorded host")
			}
			return nil
		}
	}
	return fmt.Errorf("cannot verify supervisor parent for %s", filepath.Base(executable))
}
