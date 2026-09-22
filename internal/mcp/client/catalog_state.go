package client

import (
	"context"
	"fmt"
	"reflect"
	"time"
)

type indexSnapshot struct {
	Ready         bool
	Known         bool
	Count         int
	Revision      uint64
	Version       string
	LastError     string
	LastErrorCode string
	RefreshedAt   time.Time
}

func stateSnapshotLocked(state *serverState) indexSnapshot {
	return indexSnapshot{Ready: state.client != nil, Known: state.discovered, Count: len(state.tools), Revision: state.indexRevision,
		Version: state.serverVersion, LastError: state.lastError, LastErrorCode: state.lastErrorCode, RefreshedAt: state.refreshedAt}
}
func publishStateLocked(state *serverState) {
	snapshot := stateSnapshotLocked(state)
	state.snapshot.Store(&snapshot)
}

// Summary reads do not wait for a long-running MCP tools/call that owns state.mu.
func summaryForSnapshot(cfg ServerConfig, snapshot indexSnapshot) ServerSummary {
	status := "idle"
	if !cfg.Enabled {
		status = "disabled"
	} else if snapshot.LastError != "" {
		status = "error"
	} else if snapshot.Ready {
		status = "ready"
	}
	summary := ServerSummary{Name: cfg.Name, Description: cfg.Description, Transport: cfg.Transport, Enabled: cfg.Enabled, Status: status,
		ToolCount: snapshot.Count, ToolCountKnown: snapshot.Known, ServerVersion: snapshot.Version, LastError: snapshot.LastError, LastErrorCode: snapshot.LastErrorCode,
		Revision: fmt.Sprintf("%s:%d", cfg.revision, snapshot.Revision), OverrideSource: cfg.overrideSource, LastGoodAvailable: snapshot.Ready && snapshot.LastError != ""}
	if !snapshot.RefreshedAt.IsZero() {
		summary.RefreshedAt = snapshot.RefreshedAt.Format(time.RFC3339Nano)
	}
	return summary
}

func catalogEqual(left, right map[string]Tool) bool {
	if len(left) != len(right) {
		return false
	}
	for name, a := range left {
		b, found := right[name]
		if !found {
			return false
		}
		a.inputValidator, b.inputValidator = nil, nil
		if !reflect.DeepEqual(a, b) {
			return false
		}
	}
	return true
}

func refreshStateLocked(ctx context.Context, cfg ServerConfig, state *serverState) (map[string]Tool, error) {
	candidate := &serverState{}
	tools, err := initializeStateLocked(ctx, cfg, candidate)
	if err != nil {
		recordStateError(state, err)
		return nil, err
	}
	previous := state.client
	revision := state.indexRevision
	if !state.discovered || !catalogEqual(state.tools, candidate.tools) || state.serverVersion != candidate.serverVersion {
		revision++
	}
	state.client, state.tools = candidate.client, candidate.tools
	state.discovered, state.serverVersion, state.indexRevision = true, candidate.serverVersion, revision
	state.lastError, state.lastErrorCode = "", ""
	state.refreshedAt = candidate.refreshedAt
	publishStateLocked(state)
	// The caller already pins this state: an in-flight call finishes before refresh
	// takes the lock. A failed candidate never destroys the previous connection.
	if previous != nil {
		_ = previous.close()
	}
	return tools, nil
}

// Snapshots returns already-discovered metadata without connecting, reading files
// or expanding a Heavy plugin. It is safe on the tool-response presentation path.
func (m *Manager) Snapshots(names []string) []ServerSummary {
	m.mu.RLock()
	defer m.mu.RUnlock()
	items := []ServerSummary{}
	for _, name := range names {
		if cfg, ok := m.servers[name]; ok {
			items = append(items, summaryFor(cfg, m.states[name]))
		}
	}
	return items
}
