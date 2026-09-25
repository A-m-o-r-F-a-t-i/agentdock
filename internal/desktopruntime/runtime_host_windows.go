//go:build windows

package desktopruntime

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	processctl "github.com/uvwt/agentdock/internal/process"
	"golang.org/x/sys/windows"
)

const runtimeHostEpochEnv = "AGENTDOCK_RUNTIME_HOST_EPOCH"
const runtimeHostGenerationEnv = "AGENTDOCK_RUNTIME_HOST_GENERATION"

// RunTaskRuntime owns one frozen generation and one kill-on-close Job. Only
// Core is replaced for a Quick origin change; the supervisor and cloudflared
// remain in this Job and retain their original process identities.
func RunTaskRuntime(parent context.Context, root, coreBinary, generation string) (resultErr error) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		return errors.New("elevated_unavailable: native runtime host is not elevated")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	name, err := windows.UTF16PtrFromString(tunnelSupervisorObjectName("runtime-host", root))
	if err != nil {
		return err
	}
	mutex, createErr := windows.CreateMutex(nil, false, name)
	if mutex == 0 {
		return createErr
	}
	defer windows.CloseHandle(mutex)
	state, err := windows.WaitForSingleObject(mutex, 0)
	if err != nil {
		return err
	}
	if state != windows.WAIT_OBJECT_0 && state != windows.WAIT_ABANDONED {
		return errors.New("runtime host already owns this root")
	}
	defer windows.ReleaseMutex(mutex)
	run, err := loadTunnelRuntime(root)
	if err != nil {
		return err
	}
	if _, err := nativeRuntimeTaskAction(parent, root, run.manifest, "validate"); err != nil {
		return err
	}
	// Effective mode is read from the canonical mode file, not the transient
	// manifest's none projection. Durable Named credentials are never cleared.
	if run.mode == "quick" {
		if err := clearActivePublicURL(run.files); err != nil {
			return err
		}
		if err := run.updateManifest("quick", ""); err != nil {
			return err
		}
	}
	run.manifest.Host = "127.0.0.1"
	run.manifest.Port = run.settings.Port
	job, err := processctl.NewJob()
	if err != nil {
		return err
	}
	defer job.Close()
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer null.Close()
	hostIdentity, err := currentRuntimeHostIdentity()
	if err != nil {
		return err
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	origin, err := readTrimmedText(run.files.serverURL)
	if err != nil {
		return err
	}
	owner := runtimeHostState{SchemaVersion: 1, Epoch: hex.EncodeToString(random[:]), Generation: generation, Host: hostIdentity, OriginHash: runtimeOriginDigest(origin)}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	defer func() {
		current, err := readRuntimeHostState(root)
		if err == nil && current.Epoch == owner.Epoch {
			_ = os.Remove(filepath.Join(root, runtimeHostStateFile))
		}
		if resultErr != nil {
			text := resultErr.Error()
			if len(text) > 4096 {
				text = text[:4096]
			}
			_ = writeRuntimeText(filepath.Join(root, "runtime-host-error.log"), text)
		}
	}()
	env := environmentWithout(environmentWithout(os.Environ(), runtimeHostEpochEnv), runtimeHostGenerationEnv)
	var core *processctl.JobChild
	defer func() {
		if core != nil {
			core.Close()
		}
	}()
	startCore := func() error {
		child, err := job.Start(coreBinary, []string{"service", "launch-core", "--runtime-root", root}, root, env, null, null, null)
		if err != nil {
			return err
		}
		core = child
		owner.Core = hostProcessIdentity(core)
		if err := writeRuntimeHostState(root, owner); err != nil {
			return err
		}
		return nil
	}
	if err := startCore(); err != nil {
		return err
	}
	if run.mode != "quick" && run.mode != "named" {
		code, err := core.Wait(ctx)
		if err == nil && code != 0 {
			err = fmt.Errorf("owned Core exited with code %d", code)
		}
		return err
	}
	toChildR, toChildW, err := os.Pipe()
	if err != nil {
		return err
	}
	defer toChildR.Close()
	defer toChildW.Close()
	fromChildR, fromChildW, err := os.Pipe()
	if err != nil {
		return err
	}
	defer fromChildR.Close()
	defer fromChildW.Close()
	supervisorEnv := append(append([]string(nil), env...), runtimeHostEpochEnv+"="+owner.Epoch, runtimeHostGenerationEnv+"="+generation)
	supervisor, err := job.Start(coreBinary, []string{"tunnel", "launch", "--host-controlled", "--runtime-root", root}, root, supervisorEnv, toChildR, fromChildW, null)
	if err != nil {
		return err
	}
	defer supervisor.Close()
	// Parent retains only its two private endpoints. A child cannot keep the
	// host's endpoint or the Job alive after Task Scheduler ends the host.
	toChildR.Close()
	fromChildW.Close()
	owner.Supervisor = hostProcessIdentity(supervisor)
	if err := writeRuntimeHostState(root, owner); err != nil {
		return err
	}
	type incoming struct {
		request RuntimeHostRequest
		err     error
	}
	requests := make(chan incoming, 1)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		reader := bufio.NewReaderSize(fromChildR, RuntimeHostFrameLimit+1)
		for {
			var request RuntimeHostRequest
			err := ReadRuntimeHostFrame(reader, &request)
			select {
			case requests <- incoming{request, err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	defer func() { cancel(); fromChildR.Close(); toChildW.Close(); <-readerDone }()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	next := uint64(1)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case message := <-requests:
			if message.err != nil {
				return fmt.Errorf("runtime supervisor channel closed: %w", message.err)
			}
			request := message.request
			if err := request.Validate(owner.Epoch, generation, next); err != nil {
				return err
			}
			if run.mode != "quick" {
				return errors.New("origin replacement is only available in effective Quick mode")
			}
			next++
			replace := func() error {
				release, err := acquireTunnelOperation(ctx, root)
				if err != nil {
					return err
				}
				current, loadErr := loadTunnelRuntime(root)
				if loadErr == nil && current.mode != "quick" {
					loadErr = errors.New("Quick mode changed before origin replacement")
				}
				if loadErr == nil {
					loadErr = writeRuntimeText(run.files.serverURL, request.Origin)
				}
				release()
				if loadErr != nil {
					return loadErr
				}
				stop, stopCancel := context.WithTimeout(ctx, 5*time.Second)
				defer stopCancel()
				if err := core.Stop(stop); err != nil {
					return err
				}
				core.Close()
				core = nil
				if alive, err := supervisor.Alive(); err != nil || !alive {
					return errors.New("supervisor exited during Core replacement")
				}
				if err := startCore(); err != nil {
					return err
				}
				owner.OriginHash = runtimeOriginDigest(request.Origin)
				if err := writeRuntimeHostState(root, owner); err != nil {
					return err
				}
				return waitCapturedCoreHealth(ctx, run.manifest, core, generation, owner.OriginHash)
			}
			err := replace()
			ack := RuntimeHostAck{SchemaVersion: 1, RequestID: request.RequestID, Epoch: owner.Epoch, Generation: generation, OK: err == nil}
			if core != nil {
				ack.CorePID = core.PID
			}
			if err != nil {
				ack.Error = "core_replace_failed"
			}
			if writeErr := WriteRuntimeHostFrame(toChildW, ack); writeErr != nil {
				return errors.Join(err, writeErr)
			}
			if err != nil && core == nil {
				return err
			}
		case <-ticker.C:
			for _, child := range []*processctl.JobChild{core, supervisor} {
				alive, err := child.Alive()
				if err != nil {
					return err
				}
				if !alive {
					return errors.New("owned runtime child exited")
				}
			}
		}
	}
}

func hostedRuntimeEnvironmentValid(epoch, generation string) bool {
	raw, err := hex.DecodeString(epoch)
	return err == nil && len(raw) == 16 && generation != "" && len(generation) <= 128 && !strings.ContainsAny(generation, "/\\:\x00")
}
