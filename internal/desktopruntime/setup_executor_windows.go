//go:build windows

package desktopruntime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/uvwt/agentdock/internal/fs/securepath"
)

// RunSetupExecutor is the finite, no-window parent for Inno's PowerShell
// adapter and probes. It deliberately creates no scheduled task: only the
// long-lived Core/Tray launch requests use the user-session broker. Its request
// directory contains stdout.log, stderr.log and a completed result.json.
func RunSetupExecutor(requestPath string) (int, error) {
	path, err := filepath.Abs(requestPath)
	if err != nil {
		return 1, err
	}
	request, err := readSetupRequest(path, 1800)
	if err != nil {
		return 1, err
	}
	if !request.WaitForExit {
		return 1, errors.New("setup-exec requires wait_for_exit=true")
	}
	root := filepath.Dir(path)
	if err := securepath.EnsurePrivate(root); err != nil {
		return 1, err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return 1, err
	}
	request.TaskName = "AgentDock Setup Direct " + hex.EncodeToString(nonce[:])
	result, runErr := runSetupProcess(path, request)
	// Inno normally supplies no console/stdout handles. The durable files are
	// authoritative there; callers that redirect pipes still receive the output.
	forwardSetupOutput(filepath.Join(root, "stdout.log"), os.Stdout)
	forwardSetupOutput(filepath.Join(root, "stderr.log"), os.Stderr)
	if errors.Is(runErr, context.DeadlineExceeded) {
		return 124, runErr
	}
	if runErr != nil && result.ExitCode == 0 {
		return 1, runErr
	}
	return result.ExitCode, runErr
}

func forwardSetupOutput(path string, output *os.File) {
	if _, err := output.Stat(); err != nil {
		return
	}
	file, err := os.Open(path)
	if err != nil {
		return
	}
	defer file.Close()
	_, _ = io.Copy(output, file)
}
