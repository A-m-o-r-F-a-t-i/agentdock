//go:build windows

package desktopruntime

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

	"github.com/uvwt/agentdock/internal/executioncompat"
	"golang.org/x/sys/windows"
)

// CheckExecutionCompatibility is used before a managed runtime is activated or
// restarted. A legacy version string alone is never treated as policy support.
func CheckExecutionCompatibility(ctx context.Context, runtimeRoot, targetCore string) error {
	manifest, err := Load(filepath.Join(runtimeRoot, "runtime.json"))
	if err != nil {
		return fmt.Errorf("read execution-policy data location: %w", err)
	}
	home := strings.TrimSpace(manifest.AgentDockHome)
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		home = filepath.Join(userHome, ".agentdock")
	}
	return checkExecutionCompatibility(home, func() (int, error) {
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		command := exec.CommandContext(probeCtx, targetCore, "version", "--json")
		command.Dir = runtimeRoot
		command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
		output := &capabilityOutput{}
		command.Stdout, command.Stderr = output, output
		if err := command.Run(); err != nil {
			return 0, fmt.Errorf("target capability probe failed: %w", err)
		}
		if output.overflow {
			return 0, errors.New("target capability response exceeds 64 KiB")
		}
		var info struct {
			ExecutionPolicyVersion int `json:"execution_policy_version"`
		}
		if err := json.Unmarshal(output.buffer.Bytes(), &info); err != nil {
			return 0, fmt.Errorf("invalid target capability response: %w", err)
		}
		return info.ExecutionPolicyVersion, nil
	})
}

func checkExecutionCompatibility(home string, probe func() (int, error)) error {
	required, err := executioncompat.RequiredVersion(home)
	if err != nil {
		return err
	}
	if required == 0 {
		return nil
	}
	supported, err := probe()
	if err != nil {
		return err
	}
	return executioncompat.Validate(required, supported)
}

type capabilityOutput struct {
	buffer   bytes.Buffer
	overflow bool
}

func (b *capabilityOutput) Len() int { return b.buffer.Len() }

func (b *capabilityOutput) Write(p []byte) (int, error) {
	n := len(p)
	left := (64 << 10) - b.Len()
	if n > left {
		b.overflow = true
		p = p[:left]
	}
	_, _ = b.buffer.Write(p)
	return n, nil
}
