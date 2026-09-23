package client

import "context"

// Metadata providers are read-only. Filesystem preparation is reserved for the
// one selected service, never a side effect of context or metadata listing.
func (m *Manager) SetExternalServerPreparation(prepare func(context.Context, ServerConfig) error) {
	m.mu.Lock()
	m.externalPrepare = prepare
	m.mu.Unlock()
}

func (m *Manager) prepareExternalServer(ctx context.Context, cfg ServerConfig) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if cfg.PluginRoot == "" {
		return nil
	}
	m.mu.RLock()
	prepare := m.externalPrepare
	m.mu.RUnlock()
	if prepare != nil {
		if err := prepare(ctx, cfg); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func (m *Manager) prepareRuntimeConfig(ctx context.Context, cfg ServerConfig) (ServerConfig, error) {
	resolved, err := m.runtimeConfig(cfg)
	if err != nil {
		return ServerConfig{}, err
	}
	if err := m.prepareExternalServer(ctx, resolved); err != nil {
		return ServerConfig{}, err
	}
	return resolved, nil
}
