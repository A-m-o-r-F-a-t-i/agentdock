package installer

import (
	"context"
	"os/exec"
	"runtime"

	processctl "github.com/uvwt/agentdock/internal/process"
)

// Every native control command invoked by Setup inherits the no-console
// policy, including status probes and rollback commands.
func installerCommand(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	if runtime.GOOS == "windows" {
		processctl.Configure(cmd)
	}
	return cmd
}
