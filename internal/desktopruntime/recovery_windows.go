//go:build windows

package desktopruntime

import (
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/uvwt/agentdock/internal/fs/filelock"
)

// The new launcher also recovers verified abandoned locks before starting an
// older rollback generation. Package contents and live/unknown owners remain
// untouched; only AgentDock's known lock paths are considered.
func recoverAbandonedCoreLocks(manifest Manifest, root string) {
	paths := []string{}
	if home := strings.TrimSpace(manifest.AgentDockHome); filepath.IsAbs(home) {
		paths = append(paths, filepath.Join(home, "plugins", ".locks", "store.lock"))
		for _, directory := range []string{"mcp", "env", "tasks"} {
			paths = append(paths, filepath.Join(home, directory, ".store.lock"))
		}
	}
	for _, name := range []string{"auth-token.dpapi", "oauth-password.dpapi", "oauth-token-secret.dpapi"} {
		paths = append(paths, filepath.Join(root, name+".lock"))
	}
	for _, path := range paths {
		if filelock.RecoverAbandoned(path) {
			slog.Info("recovered abandoned runtime lock", "path", path)
		}
	}
}
