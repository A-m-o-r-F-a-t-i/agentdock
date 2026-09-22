package client

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/uvwt/agentdock/internal/fs/atomicfile"
	"github.com/uvwt/agentdock/internal/fs/filelock"
)

const maxRetiredServers = 32

type ConfigPatch map[string]json.RawMessage

type overrideFile struct {
	SchemaVersion int                    `json:"schema_version"`
	Revision      uint64                 `json:"revision"`
	Servers       map[string]ConfigPatch `json:"servers"`
}

func revisionInstance() (string, error) {
	value := make([]byte, 12)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func (m *Manager) nextRevisionLocked() string {
	m.revisionSequence++
	return fmt.Sprintf("%s:%d", m.revisionInstance, m.revisionSequence)
}

func clonePatch(patch ConfigPatch) ConfigPatch {
	result := ConfigPatch{}
	for key, value := range patch {
		result[key] = append(json.RawMessage(nil), value...)
	}
	return result
}

func mergePatch(previous, change ConfigPatch) ConfigPatch {
	result := clonePatch(previous)
	for key, value := range change {
		result[key] = append(json.RawMessage(nil), value...)
	}
	return result
}

func patchConfig(base ServerConfig, patch ConfigPatch) (ServerConfig, error) {
	cfg := normalizeServerConfig(base)
	for key, value := range patch {
		if len(value) == 0 || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return cfg, fmt.Errorf("MCP patch %s must have an explicit typed value", key)
		}
		var target any
		switch key {
		case "description":
			target = &cfg.Description
		case "transport":
			target = &cfg.Transport
		case "url":
			target = &cfg.URL
		case "command":
			target = &cfg.Command
		case "args":
			target = &cfg.Args
		case "cwd":
			target = &cfg.Cwd
		case "header_env":
			target = &cfg.HeaderEnv
		case "env_from_env":
			target = &cfg.EnvFromEnv
		case "timeout_ms":
			target = &cfg.TimeoutMS
		default:
			return cfg, fmt.Errorf("MCP patch field %q is not editable; ownership, enabled state, secrets and discovered facts retain their canonical owner", key)
		}
		if err := json.Unmarshal(value, target); err != nil {
			return cfg, fmt.Errorf("invalid MCP patch %s: %w", key, err)
		}
	}
	cfg = normalizeServerConfig(cfg)
	if len(cfg.Description) > 8192 || len(cfg.Args) > 128 || len(cfg.HeaderEnv) > 128 || len(cfg.EnvFromEnv) > 128 {
		return cfg, errors.New("MCP patch exceeds bounded metadata limits")
	}
	if err := validateServerConfig(cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func sameConfiguration(left, right ServerConfig) bool {
	left.revision, right.revision = "", ""
	return reflect.DeepEqual(left, right)
}
func sameConnection(left, right ServerConfig) bool {
	left.revision, right.revision = "", ""
	left.overrideSource, right.overrideSource = "", ""
	left.overlayRevision, right.overlayRevision = 0, 0
	left.Description, right.Description = "", ""
	left.TimeoutMS, right.TimeoutMS = 0, 0
	return reflect.DeepEqual(left, right)
}

func (m *Manager) readOverrides() (overrideFile, error) {
	result := overrideFile{SchemaVersion: 1, Servers: map[string]ConfigPatch{}}
	file, err := os.Open(m.overridePath)
	if os.IsNotExist(err) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxRegistryFileBytes+1))
	if err != nil {
		return result, err
	}
	if len(data) > maxRegistryFileBytes {
		return result, errors.New("MCP override store exceeds size limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&result); err != nil {
		return result, err
	}
	if err = decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return result, errors.New("MCP override store has trailing data")
	}
	if result.SchemaVersion != 1 || len(result.Servers) > 512 {
		return result, errors.New("unsupported MCP override store; original preserved")
	}
	if result.Servers == nil {
		result.Servers = map[string]ConfigPatch{}
	}
	for name, patch := range result.Servers {
		if !serverNamePattern.MatchString(name) || len(patch) > 10 {
			return result, errors.New("invalid MCP override entry")
		}
	}
	return result, nil
}

func (m *Manager) writeOverrides(ctx context.Context, expected uint64, next overrideFile) error {
	release, err := filelock.Acquire(ctx, m.overridePath+".lock")
	if err != nil {
		return err
	}
	defer release()
	current, err := m.readOverrides()
	if err != nil {
		return err
	}
	if current.Revision != expected {
		return newError("MCP_REVISION_CONFLICT", "MCP overrides changed concurrently; inspect again before updating", false, nil, nil)
	}
	next.Revision = current.Revision + 1
	data, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > maxRegistryFileBytes {
		return errors.New("MCP override store exceeds size limit")
	}
	return atomicfile.Write(m.overridePath, append(data, '\n'), 0600)
}

func (m *Manager) applyOverridesLocked(base map[string]ServerConfig) (map[string]ServerConfig, error) {
	stored, err := m.readOverrides()
	if err != nil {
		return nil, newError("MCP_OVERRIDE_READ_FAILED", "read MCP overrides; previous runtime retained", true, nil, err)
	}
	result := maps.Clone(base)
	for name, cfg := range result {
		cfg, err = patchConfig(cfg, stored.Servers[name])
		if err != nil {
			return nil, newError("MCP_OVERRIDE_CONFLICT", "saved override conflicts with current plugin defaults; reset that override", false, map[string]any{"server": name}, err)
		}
		cfg, err = patchConfig(cfg, m.runtimeOverrides[name])
		if err != nil {
			return nil, newError("MCP_OVERRIDE_CONFLICT", "runtime override conflicts with current defaults; reset that override", false, map[string]any{"server": name}, err)
		}
		cfg.overlayRevision = stored.Revision
		cfg.overrideSource = "default"
		if len(stored.Servers[name]) > 0 {
			cfg.overrideSource = "persistent"
		}
		if len(m.runtimeOverrides[name]) > 0 {
			cfg.overrideSource = "runtime"
		}
		result[name] = cfg
	}
	return result, nil
}

// Update changes a host-owned overlay without rewriting portable plugin files.
// Metadata updates preserve the existing connection. Connection updates initialize
// a candidate first, atomically switch only after validation, and drain the old
// generation without cancelling an in-flight call.
func (m *Manager) Update(ctx context.Context, name, expected, scope string, patch ConfigPatch, reset bool) (ServerSummary, error) {
	name = strings.TrimSpace(name)
	if scope != "runtime" && scope != "persistent" {
		return ServerSummary{}, newError("MCP_SCOPE_INVALID", "scope must be runtime or persistent", false, nil, nil)
	}
	if expected == "" {
		return ServerSummary{}, newError("MCP_REVISION_REQUIRED", "inspect the server and supply expected_revision", false, nil, nil)
	}
	if !reset && len(patch) == 0 {
		return ServerSummary{}, newError("MCP_PATCH_REQUIRED", "non-empty patch is required", false, nil, nil)
	}
	if err := m.syncRegistry(); err != nil {
		var issue *Error
		if !reset || !errors.As(err, &issue) || issue.Code != "MCP_OVERRIDE_CONFLICT" {
			return ServerSummary{}, err
		}
	}
	m.registryMu.Lock()
	defer m.registryMu.Unlock()
	if err := m.ensureOpenLocked(); err != nil {
		return ServerSummary{}, err
	}
	m.mu.RLock()
	old, found := m.servers[name]
	oldState := m.states[name]
	m.mu.RUnlock()
	if !found {
		return ServerSummary{}, newError("MCP_SERVER_NOT_FOUND", "dynamic MCP server not found", false, map[string]any{"server": name}, nil)
	}
	if summaryFor(old, oldState).Revision != expected {
		return ServerSummary{}, newError("MCP_REVISION_CONFLICT", "MCP server changed; inspect again before updating", false, map[string]any{"server": name, "revision": summaryFor(old, oldState).Revision}, nil)
	}
	standalone, err := m.store.load()
	if err != nil {
		return ServerSummary{}, err
	}
	base, err := m.mergeExternalBaseLocked(standalone)
	if err != nil {
		return ServerSummary{}, err
	}
	original, found := base[name]
	if !found {
		return ServerSummary{}, newError("MCP_SERVER_NOT_FOUND", "MCP owner removed this server", false, nil, nil)
	}
	stored, err := m.readOverrides()
	if err != nil {
		return ServerSummary{}, err
	}
	nextStored := overrideFile{SchemaVersion: 1, Revision: stored.Revision, Servers: maps.Clone(stored.Servers)}
	runtimePatch := clonePatch(m.runtimeOverrides[name])
	if scope == "persistent" {
		if reset {
			delete(nextStored.Servers, name)
		} else {
			nextStored.Servers[name] = mergePatch(stored.Servers[name], patch)
		}
	} else {
		if reset {
			runtimePatch = ConfigPatch{}
		} else {
			runtimePatch = mergePatch(runtimePatch, patch)
		}
	}
	selected, err := patchConfig(original, nextStored.Servers[name])
	if err != nil {
		return ServerSummary{}, newError("MCP_CONFIG_INVALID", err.Error(), false, nil, err)
	}
	selected, err = patchConfig(selected, runtimePatch)
	if err != nil {
		return ServerSummary{}, newError("MCP_CONFIG_INVALID", err.Error(), false, nil, err)
	}
	selected.overlayRevision = stored.Revision
	if scope == "persistent" {
		selected.overlayRevision++
	}
	selected.overrideSource = "default"
	if len(nextStored.Servers[name]) > 0 {
		selected.overrideSource = "persistent"
	}
	if len(runtimePatch) > 0 {
		selected.overrideSource = "runtime"
	}
	nextState := oldState
	changedConnection := !sameConnection(old, selected)
	if changedConnection {
		m.retiredMu.Lock()
		busy := len(m.retired) >= maxRetiredServers
		m.retiredMu.Unlock()
		if busy {
			return ServerSummary{}, newError("MCP_UPDATE_BUSY", "prior MCP generations are still draining", true, nil, nil)
		}
		nextState = &serverState{}
		if selected.Enabled {
			resolved, resolveErr := m.runtimeConfig(selected)
			if resolveErr != nil {
				return ServerSummary{}, resolveErr
			}
			checkCtx, cancel := context.WithTimeout(ctx, time.Duration(selected.TimeoutMS)*time.Millisecond)
			_, err = refreshStateLocked(checkCtx, resolved, nextState)
			cancel()
			if err != nil {
				return ServerSummary{}, newError("MCP_UPDATE_VALIDATION_FAILED", "candidate MCP connection failed; previous configuration and calls retained", true, map[string]any{"server": name}, err)
			}
		}
	}
	committed := false
	defer func() {
		if !committed && nextState != oldState {
			_ = closeState(nextState)
		}
	}()
	// Plugin files may change while candidate discovery is running.
	latestStandalone, err := m.store.load()
	if err != nil {
		return ServerSummary{}, err
	}
	latestBase, err := m.mergeExternalBaseLocked(latestStandalone)
	if err != nil {
		return ServerSummary{}, err
	}
	latestStored, err := m.readOverrides()
	if err != nil {
		return ServerSummary{}, err
	}
	if !reflect.DeepEqual(base, latestBase) || latestStored.Revision != stored.Revision || summaryFor(old, oldState).Revision != expected {
		return ServerSummary{}, newError("MCP_REVISION_CONFLICT", "MCP defaults, catalog or overrides changed during validation; no update committed", false, nil, nil)
	}
	if scope == "persistent" {
		if err = m.writeOverrides(ctx, stored.Revision, nextStored); err != nil {
			return ServerSummary{}, err
		}
	}
	if scope == "runtime" {
		if reset {
			delete(m.runtimeOverrides, name)
		} else {
			m.runtimeOverrides[name] = runtimePatch
		}
	}
	selected.revision = m.nextRevisionLocked()
	m.mu.Lock()
	m.servers[name], m.states[name] = selected, nextState
	m.mu.Unlock()
	committed = true
	if nextState != oldState {
		m.retireState(oldState)
	}
	return summaryFor(selected, nextState), nil
}

func (m *Manager) retireState(state *serverState) {
	if state == nil {
		return
	}
	m.retiredMu.Lock()
	m.retired[state] = true
	m.retiredMu.Unlock()
	m.retiredWG.Add(1)
	go func() {
		defer m.retiredWG.Done()
		_ = closeState(state)
		m.retiredMu.Lock()
		delete(m.retired, state)
		m.retiredMu.Unlock()
	}()
}

func overridePath(home string) string { return filepath.Join(home, "mcp", "overrides.json") }
