package client

import (
	"context"
	"sync"
	"time"
)

// lockWithContext waits without spawning a goroutine that could outlive the
// request and later acquire a lock nobody will release. Fast paths do not sleep.
func lockWithContext(ctx context.Context, mutex *sync.Mutex) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if mutex.TryLock() {
		if err := ctx.Err(); err != nil {
			mutex.Unlock()
			return err
		}
		return nil
	}
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if mutex.TryLock() {
				if err := ctx.Err(); err != nil {
					mutex.Unlock()
					return err
				}
				return nil
			}
		}
	}
}

// Caller holds registryMu, serializing publications and new retired generations.
func (m *Manager) canRetireRegistry(next map[string]ServerConfig) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	count := 0
	for name, old := range m.servers {
		cfg, exists := next[name]
		if !exists || !sameConnection(old, cfg) {
			count++
		}
	}
	m.retiredMu.Lock()
	defer m.retiredMu.Unlock()
	return len(m.retired)+count <= maxRetiredServers
}
