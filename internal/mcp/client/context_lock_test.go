package client

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/uvwt/agentdock/internal/fs/filelock"
)

func TestContextRegistryAndFileLocksHonorCancellation(t *testing.T) {
	for _, kind := range []string{"registry", "file", "provider"} {
		t.Run(kind, func(t *testing.T) {
			m, err := NewManager(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer m.Close()
			var release func()
			var block atomic.Bool
			switch kind {
			case "registry":
				m.registryMu.Lock()
				release = m.registryMu.Unlock
			case "file":
				release, err = filelock.Acquire(t.Context(), m.store.lockPath)
				if err != nil {
					t.Fatal(err)
				}
			case "provider":
				if err := m.SetExternalServerProvider(func(ctx context.Context) (map[string]ServerConfig, error) {
					if block.Load() {
						<-ctx.Done()
						return nil, ctx.Err()
					}
					return map[string]ServerConfig{}, nil
				}); err != nil {
					t.Fatal(err)
				}
				block.Store(true)
				release = func() { block.Store(false) }
			}
			ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
			started := time.Now()
			_, err = m.EnabledIndexContext(ctx)
			cancel()
			release()
			if err == nil || !errors.Is(ctx.Err(), context.DeadlineExceeded) {
				t.Fatalf("lock ignored deadline: %v", err)
			}
			if time.Since(started) > time.Second {
				t.Fatal("caller deadline did not bound metadata read")
			}
			if _, err := m.EnabledIndexContext(t.Context()); err != nil {
				t.Fatalf("lock or waiter leaked: %v", err)
			}
		})
	}
}

func TestContextMetadataDoesNotWaitForExecutingServer(t *testing.T) {
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if _, err := m.Add(ServerConfig{Name: "busy", Description: "busy", Transport: TransportStreamableHTTP, URL: "https://example.invalid/mcp", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	_, _, release, err := m.lockServer("busy")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
	defer cancel()
	items, err := m.EnabledIndexContext(ctx)
	if err != nil || len(items) != 1 {
		t.Fatalf("metadata waited for service: %#v %v", items, err)
	}
	blocked, stop := context.WithTimeout(t.Context(), 25*time.Millisecond)
	defer stop()
	if _, _, unlock, err := m.lockServerContext(blocked, "busy"); err == nil {
		unlock()
		t.Fatal("cancelled queued operation acquired busy server")
	}
}
