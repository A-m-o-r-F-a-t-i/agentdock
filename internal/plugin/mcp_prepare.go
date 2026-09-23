package plugin

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/uvwt/agentdock/internal/fs/securepath"
	mcpclient "github.com/uvwt/agentdock/internal/mcp/client"
)

// PrepareMCPData revalidates the selected package's current filesystem boundary
// before applying its private directory permissions. It never prepares unrelated
// or unselected Heavy services while reading the capability directory.
func (s *Store) PrepareMCPData(ctx context.Context, cfg mcpclient.ServerConfig) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validPluginName(cfg.PluginName) {
		return errors.New("invalid plugin data owner")
	}
	directory, _, err := s.Snapshot(ctx)
	if err != nil {
		return err
	}
	member, owned := directory.MCPMembership(cfg.Name)
	if !owned || !member.Enabled || member.Plugin != cfg.PluginName {
		return errors.New("plugin MCP is no longer enabled for this owner")
	}
	root, err := containedPath(s.root, filepath.Join(s.root, cfg.PluginName), false)
	if err != nil {
		return err
	}
	data, err := containedPath(s.root, filepath.Join(s.root, ".data", cfg.PluginName), true)
	if err != nil {
		return err
	}
	if root != cfg.PluginRoot || data != cfg.PluginData {
		return errors.New("plugin filesystem target changed; refresh the service before dispatch")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(data, 0700); err != nil {
		return err
	}
	after, err := containedPath(s.root, data, false)
	if err != nil {
		return err
	}
	if after != data {
		return errors.New("plugin data directory changed during preparation")
	}
	if err := securepath.EnsurePrivate(data); err != nil {
		return err
	}
	return ctx.Err()
}
