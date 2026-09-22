package client

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/uvwt/agentdock/internal/envstore"
)

type Manager struct {
	overridePath     string
	runtimeOverrides map[string]ConfigPatch
	revisionInstance string
	revisionSequence uint64
	retiredMu        sync.Mutex
	retired          map[*serverState]bool
	retiredWG        sync.WaitGroup
	callObserver     atomic.Pointer[toolCallObserver]
	registryMu       sync.Mutex
	closed           atomic.Bool
	mu               sync.RWMutex
	store            *store
	envs             *envstore.Store
	external         ExternalServerProvider
	servers          map[string]ServerConfig
	states           map[string]*serverState
}

// ExternalServerProvider supplies dynamic MCP definitions owned by direct
// heavy-plugin packages. Provider results are merged in memory and are never
// persisted into the standalone mcp/servers.json registry.
type ExternalServerProvider func() (map[string]ServerConfig, error)

type serverState struct {
	snapshot      atomic.Pointer[indexSnapshot]
	discovered    bool
	indexRevision uint64
	serverVersion string
	mu            sync.Mutex
	client        protocolClient
	tools         map[string]Tool
	lastError     string
	lastErrorCode string
	refreshedAt   time.Time
}

func NewManager(agentDockHome string, provided ...*envstore.Store) (*Manager, error) {
	registry := newStore(agentDockHome)
	servers, err := registry.load()
	if err != nil {
		return nil, err
	}
	envs := (*envstore.Store)(nil)
	if len(provided) > 0 {
		envs = provided[0]
	}
	if envs == nil {
		envs, err = envstore.New(agentDockHome)
		if err != nil {
			return nil, err
		}
	}
	states := make(map[string]*serverState, len(servers))
	for name := range servers {
		states[name] = &serverState{}
	}
	instance, err := revisionInstance()
	if err != nil {
		return nil, err
	}
	manager := &Manager{store: registry, envs: envs, servers: servers, states: states, overridePath: overridePath(agentDockHome), runtimeOverrides: map[string]ConfigPatch{}, revisionInstance: instance, retired: map[*serverState]bool{}}
	for name, cfg := range manager.servers {
		cfg.revision = manager.nextRevisionLocked()
		cfg.overrideSource = "default"
		manager.servers[name] = cfg
	}
	if err = manager.syncRegistry(); err != nil {
		return nil, err
	}
	return manager, nil
}

func (m *Manager) SetExternalServerProvider(provider ExternalServerProvider) error {
	m.registryMu.Lock()
	defer m.registryMu.Unlock()
	if err := m.ensureOpenLocked(); err != nil {
		return err
	}
	m.external = provider
	servers, err := m.store.load()
	if err != nil {
		return newError("MCP_REGISTRY_READ_FAILED", "read dynamic MCP registry", true, nil, err)
	}
	combined, err := m.mergeExternalLocked(servers)
	if err != nil {
		return err
	}
	m.mu.Lock()
	staleStates := m.replaceRegistryLocked(combined)
	m.mu.Unlock()
	return closeServerStates(staleStates)
}

func (m *Manager) Add(cfg ServerConfig) (ServerSummary, error) {
	cfg = normalizeServerConfig(cfg)
	if err := validateServerConfig(cfg); err != nil {
		return ServerSummary{}, newError("MCP_CONFIG_INVALID", err.Error(), false, map[string]any{"server": cfg.Name}, err)
	}
	m.registryMu.Lock()
	defer m.registryMu.Unlock()
	if err := m.ensureOpenLocked(); err != nil {
		return ServerSummary{}, err
	}
	external, err := m.externalServersLocked()
	if err != nil {
		return ServerSummary{}, err
	}
	if _, exists := external[cfg.Name]; exists {
		return ServerSummary{}, newError("MCP_SERVER_MANAGED_BY_PLUGIN", "dynamic MCP server is owned by an installed plugin", false, map[string]any{"server": cfg.Name}, nil)
	}
	servers, err := m.store.update(func(servers map[string]ServerConfig) error {
		if _, exists := servers[cfg.Name]; exists {
			return newError("MCP_SERVER_EXISTS", "dynamic MCP server already exists", false, map[string]any{"server": cfg.Name}, nil)
		}
		servers[cfg.Name] = cfg
		return nil
	})
	if err != nil {
		var mcpErr *Error
		if errors.As(err, &mcpErr) {
			return ServerSummary{}, err
		}
		return ServerSummary{}, newError("MCP_REGISTRY_WRITE_FAILED", "persist dynamic MCP server", false, map[string]any{"server": cfg.Name}, err)
	}

	combined, err := m.mergeExternalLocked(servers)
	if err != nil {
		return ServerSummary{}, err
	}
	m.mu.Lock()
	staleStates := m.replaceRegistryLocked(combined)
	state := m.states[cfg.Name]
	cfg = m.servers[cfg.Name]
	m.mu.Unlock()
	closeServerStates(staleStates)
	return summaryFor(cfg, state), nil
}

func (m *Manager) Remove(name string) error {
	name = strings.TrimSpace(name)
	m.registryMu.Lock()
	defer m.registryMu.Unlock()
	if err := m.ensureOpenLocked(); err != nil {
		return err
	}
	external, err := m.externalServersLocked()
	if err != nil {
		return err
	}
	if _, exists := external[name]; exists {
		return newError("MCP_SERVER_MANAGED_BY_PLUGIN", "dynamic MCP server is owned by an installed plugin", false, map[string]any{"server": name}, nil)
	}
	servers, err := m.store.update(func(servers map[string]ServerConfig) error {
		if _, exists := servers[name]; !exists {
			return newError("MCP_SERVER_NOT_FOUND", "dynamic MCP server not found", false, map[string]any{"server": name}, nil)
		}
		delete(servers, name)
		return nil
	})
	if err != nil {
		var mcpErr *Error
		if errors.As(err, &mcpErr) {
			return err
		}
		return newError("MCP_REGISTRY_WRITE_FAILED", "remove dynamic MCP server", false, map[string]any{"server": name}, err)
	}
	combined, err := m.mergeExternalLocked(servers)
	if err != nil {
		return err
	}
	m.mu.Lock()
	staleStates := m.replaceRegistryLocked(combined)
	m.mu.Unlock()
	return closeServerStates(staleStates)
}

func (m *Manager) SetEnabled(name string, enabled bool) (ServerSummary, error) {
	name = strings.TrimSpace(name)
	m.registryMu.Lock()
	defer m.registryMu.Unlock()
	if err := m.ensureOpenLocked(); err != nil {
		return ServerSummary{}, err
	}
	external, err := m.externalServersLocked()
	if err != nil {
		return ServerSummary{}, err
	}
	if _, exists := external[name]; exists {
		return ServerSummary{}, newError("MCP_SERVER_MANAGED_BY_PLUGIN", "dynamic MCP server state is managed by its plugin", false, map[string]any{"server": name}, nil)
	}
	var selected ServerConfig
	servers, err := m.store.update(func(servers map[string]ServerConfig) error {
		cfg, exists := servers[name]
		if !exists {
			return newError("MCP_SERVER_NOT_FOUND", "dynamic MCP server not found", false, map[string]any{"server": name}, nil)
		}
		cfg.Enabled = enabled
		servers[name] = cfg
		selected = cfg
		return nil
	})
	if err != nil {
		var mcpErr *Error
		if errors.As(err, &mcpErr) {
			return ServerSummary{}, err
		}
		return ServerSummary{}, newError("MCP_REGISTRY_WRITE_FAILED", "persist dynamic MCP server state", false, map[string]any{"server": name}, err)
	}
	combined, err := m.mergeExternalLocked(servers)
	if err != nil {
		return ServerSummary{}, err
	}
	m.mu.Lock()
	staleStates := m.replaceRegistryLocked(combined)
	state := m.states[name]
	selected = m.servers[name]
	m.mu.Unlock()
	if err := closeServerStates(staleStates); err != nil {
		return ServerSummary{}, err
	}
	if !enabled {
		if err := closeState(state); err != nil {
			return ServerSummary{}, err
		}
	}
	return summaryFor(selected, state), nil
}

func (m *Manager) replaceRegistryLocked(servers map[string]ServerConfig) []*serverState {
	states := make(map[string]*serverState, len(servers))
	stale := make([]*serverState, 0)
	for name, cfg := range servers {
		if previous, exists := m.servers[name]; exists {
			if sameConfiguration(previous, cfg) {
				cfg.revision = previous.revision
				servers[name] = cfg
				states[name] = m.states[name]
				continue
			}
			cfg.revision = m.nextRevisionLocked()
			servers[name] = cfg
			if sameConnection(previous, cfg) {
				states[name] = m.states[name]
				continue
			}
		} else {
			cfg.revision = m.nextRevisionLocked()
			servers[name] = cfg
		}
		if previousState := m.states[name]; previousState != nil {
			stale = append(stale, previousState)
		}
		states[name] = &serverState{}
	}
	for name, state := range m.states {
		if _, exists := servers[name]; !exists && state != nil {
			stale = append(stale, state)
		}
	}
	m.servers = servers
	m.states = states
	return stale
}

func closeServerStates(states []*serverState) error {
	var result error
	for _, state := range states {
		result = errors.Join(result, closeState(state))
	}
	return result
}

func (m *Manager) externalServersLocked() (map[string]ServerConfig, error) {
	if m.external == nil {
		return map[string]ServerConfig{}, nil
	}
	provided, err := m.external()
	if err != nil {
		return nil, newError("MCP_PLUGIN_REGISTRY_READ_FAILED", "read plugin-owned MCP servers", true, nil, err)
	}
	servers := make(map[string]ServerConfig, len(provided))
	for key, raw := range provided {
		name := strings.TrimSpace(key)
		cfg := normalizeServerConfig(raw)
		if cfg.Name == "" {
			cfg.Name = name
		}
		if name == "" || cfg.Name != name {
			return nil, newError("MCP_PLUGIN_CONFIG_INVALID", "plugin MCP map key and server name must match", false, map[string]any{"key": key, "server": cfg.Name}, nil)
		}
		if err := validateServerConfig(cfg); err != nil {
			return nil, newError("MCP_PLUGIN_CONFIG_INVALID", err.Error(), false, map[string]any{"server": name}, err)
		}
		servers[name] = cfg
	}
	return servers, nil
}

func (m *Manager) mergeExternalLocked(standalone map[string]ServerConfig) (map[string]ServerConfig, error) {
	base, err := m.mergeExternalBaseLocked(standalone)
	if err != nil {
		return nil, err
	}
	return m.applyOverridesLocked(base)
}

func (m *Manager) mergeExternalBaseLocked(standalone map[string]ServerConfig) (map[string]ServerConfig, error) {
	combined := make(map[string]ServerConfig, len(standalone))
	for name, cfg := range standalone {
		combined[name] = cfg
	}
	external, err := m.externalServersLocked()
	if err != nil {
		return nil, err
	}
	for name, cfg := range external {
		if _, exists := combined[name]; exists {
			return nil, newError("MCP_SERVER_CONFLICT", "plugin MCP server conflicts with a standalone registration", false, map[string]any{"server": name}, nil)
		}
		combined[name] = cfg
	}
	return combined, nil
}

func (m *Manager) syncRegistry() error {
	m.registryMu.Lock()
	defer m.registryMu.Unlock()
	if err := m.ensureOpenLocked(); err != nil {
		return err
	}
	servers, err := m.store.load()
	if err != nil {
		return newError("MCP_REGISTRY_READ_FAILED", "read dynamic MCP registry", true, nil, err)
	}
	combined, err := m.mergeExternalLocked(servers)
	if err != nil {
		return err
	}
	m.mu.Lock()
	staleStates := m.replaceRegistryLocked(combined)
	m.mu.Unlock()
	return closeServerStates(staleStates)
}

func (m *Manager) List() []ServerSummary {
	if err := m.syncRegistry(); err != nil {
		slog.Warn("refresh dynamic MCP registry before list failed", "error", err)
	}
	m.mu.RLock()
	names := make([]string, 0, len(m.servers))
	for name := range m.servers {
		names = append(names, name)
	}
	sort.Strings(names)
	items := make([]ServerSummary, 0, len(names))
	for _, name := range names {
		items = append(items, summaryFor(m.servers[name], m.states[name]))
	}
	m.mu.RUnlock()
	return items
}

func (m *Manager) EnabledIndex() []ServerSummary {
	all := m.List()
	items := make([]ServerSummary, 0, len(all))
	for _, item := range all {
		if item.Enabled {
			items = append(items, item)
		}
	}
	return items
}

func (m *Manager) Inspect(name string) (ServerConfig, ServerSummary, error) {
	if err := m.syncRegistry(); err != nil {
		return ServerConfig{}, ServerSummary{}, err
	}
	m.mu.RLock()
	cfg, exists := m.servers[strings.TrimSpace(name)]
	state := m.states[strings.TrimSpace(name)]
	m.mu.RUnlock()
	if !exists {
		return ServerConfig{}, ServerSummary{}, newError("MCP_SERVER_NOT_FOUND", "dynamic MCP server not found", false, map[string]any{"server": name}, nil)
	}
	return cfg, summaryFor(cfg, state), nil
}

func (m *Manager) Refresh(ctx context.Context, name string) (ServerSummary, []ToolSummary, error) {
	if err := m.syncRegistry(); err != nil {
		return ServerSummary{}, nil, err
	}
	cfg, state, unlockState, err := m.lockServer(strings.TrimSpace(name))
	if err != nil {
		return ServerSummary{}, nil, err
	}
	defer unlockState()
	if !cfg.Enabled {
		return ServerSummary{}, nil, newError("MCP_SERVER_DISABLED", "dynamic MCP server is disabled", false, map[string]any{"server": cfg.Name}, nil)
	}
	runtimeCfg, err := m.runtimeConfig(cfg)
	if err != nil {
		recordStateError(state, err)
		return ServerSummary{}, nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(cfg.TimeoutMS)*time.Millisecond)
	defer cancel()
	tools, err := refreshStateLocked(ctx, runtimeCfg, state)
	summary := summaryForLocked(cfg, state)
	if err != nil {
		return summary, nil, err
	}
	return summary, summarizeTools(cfg.Name, tools), nil
}

func (m *Manager) Search(ctx context.Context, query, server string, limit int) ([]ToolSummary, error) {
	return m.SearchFiltered(ctx, query, server, limit, nil)
}

// ListTools resolves one enabled server and returns its complete lazy-loaded
// tool index. Heavy plugins use this only after their first-level description
// has been selected, so MCP tool descriptions stay out of the root context.
func (m *Manager) ListTools(ctx context.Context, server string) ([]ToolSummary, error) {
	if err := m.syncRegistry(); err != nil {
		return nil, err
	}
	server = strings.TrimSpace(server)
	configs, err := m.searchServers(server)
	if err != nil {
		return nil, err
	}
	if len(configs) != 1 {
		return nil, newError("MCP_SERVER_REQUIRED", "one dynamic MCP server is required", false, map[string]any{"server": server}, nil)
	}
	tools, err := m.ensureTools(ctx, configs[0].Name)
	if err != nil {
		return nil, err
	}
	return summarizeTools(configs[0].Name, tools), nil
}

// SearchFiltered applies an optional server predicate before any connection or
// tools/list request. It lets higher-level capability containers hide members
// until their container is explicitly loaded without changing MCP persistence.
func (m *Manager) SearchFiltered(ctx context.Context, query, server string, limit int, allow func(string) bool) ([]ToolSummary, error) {
	if err := m.syncRegistry(); err != nil {
		return nil, err
	}
	query = strings.ToLower(strings.TrimSpace(query))
	server = strings.TrimSpace(server)
	if query == "" {
		return nil, newError("MCP_QUERY_REQUIRED", "MCP tool search query is required", false, nil, nil)
	}
	if limit <= 0 {
		limit = 10
	}
	if limit > 100 {
		limit = 100
	}

	configs, err := m.searchServers(server)
	if err != nil {
		return nil, err
	}
	if allow != nil {
		filtered := configs[:0]
		for _, cfg := range configs {
			if allow(cfg.Name) {
				filtered = append(filtered, cfg)
			}
		}
		configs = filtered
		if server != "" && len(configs) == 0 {
			return nil, newError("MCP_SERVER_HIDDEN", "dynamic MCP server is hidden by its capability container", false, map[string]any{"server": server}, nil)
		}
	}
	type scoredTool struct {
		score int
		item  ToolSummary
	}
	matches := make([]scoredTool, 0)
	var firstErr error
	for _, cfg := range configs {
		tools, ensureErr := m.ensureTools(ctx, cfg.Name)
		if ensureErr != nil {
			if server != "" {
				return nil, ensureErr
			}
			if firstErr == nil {
				firstErr = ensureErr
			}
			continue
		}
		for _, tool := range tools {
			score := toolMatchScore(query, tool)
			if score == 0 {
				continue
			}
			matches = append(matches, scoredTool{score: score, item: toolSummary(cfg.Name, tool)})
		}
	}
	if len(matches) == 0 && firstErr != nil {
		return nil, firstErr
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score > matches[j].score
		}
		return matches[i].item.QualifiedName < matches[j].item.QualifiedName
	})
	if len(matches) > limit {
		matches = matches[:limit]
	}
	items := make([]ToolSummary, 0, len(matches))
	for _, match := range matches {
		items = append(items, match.item)
	}
	return items, nil
}

func (m *Manager) InspectTool(ctx context.Context, qualifiedName string) (string, Tool, error) {
	if err := m.syncRegistry(); err != nil {
		return "", Tool{}, err
	}
	server, name, err := splitQualifiedToolName(qualifiedName)
	if err != nil {
		return "", Tool{}, newError("MCP_TOOL_NAME_INVALID", err.Error(), false, map[string]any{"tool": qualifiedName}, err)
	}
	tools, err := m.ensureTools(ctx, server)
	if err != nil {
		return "", Tool{}, err
	}
	tool, exists := tools[name]
	if !exists {
		return "", Tool{}, newError("MCP_TOOL_NOT_FOUND", "MCP tool not found", false, map[string]any{"tool": qualifiedName}, nil)
	}
	return server, tool, nil
}

func (m *Manager) Call(ctx context.Context, qualifiedName string, arguments map[string]any) (map[string]any, error) {
	if err := m.syncRegistry(); err != nil {
		return nil, err
	}
	server, name, err := splitQualifiedToolName(qualifiedName)
	if err != nil {
		return nil, newError("MCP_TOOL_NAME_INVALID", err.Error(), false, map[string]any{"tool": qualifiedName}, err)
	}
	cfg, state, unlockState, err := m.lockServer(server)
	if err != nil {
		return nil, err
	}
	defer unlockState()
	if !cfg.Enabled {
		return nil, newError("MCP_SERVER_DISABLED", "dynamic MCP server is disabled", false, map[string]any{"server": server}, nil)
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(cfg.TimeoutMS)*time.Millisecond)
	defer cancel()
	if _, approved := ctx.Value(approvedToolTargetKey{}).(string); approved {
		resolved, targetErr := m.runtimeConfig(cfg)
		if targetErr != nil {
			return nil, targetErr
		}
		if targetErr = checkApprovedToolTarget(ctx, resolved); targetErr != nil {
			return nil, targetErr
		}
	}
	if state.client == nil || !state.discovered {
		runtimeCfg, err := m.runtimeConfig(cfg)
		if err != nil {
			recordStateError(state, err)
			return nil, err
		}
		if _, err := refreshStateLocked(ctx, runtimeCfg, state); err != nil {
			return nil, err
		}
	}
	tool, exists := state.tools[name]
	if !exists {
		return nil, newError("MCP_TOOL_NOT_FOUND", "MCP tool not found", false, map[string]any{"tool": qualifiedName}, nil)
	}
	if err := validateToolArguments(tool, arguments); err != nil {
		return nil, err
	}
	observer := m.callObserver.Load()
	dispatch := func(callCtx context.Context) (map[string]any, error) {
		return state.client.callTool(callCtx, name, arguments)
	}
	var result map[string]any
	if observer != nil {
		result, err = observer.call(ctx, qualifiedName, dispatch)
	} else {
		result, err = dispatch(ctx)
	}
	if err != nil {
		// 工具调用失败是请求级结果，不代表 MCP server 的连接或发现状态失效。
		// server 的 lastError 只记录 refresh / initialize / tools/list 生命周期故障。
		return nil, err
	}
	return result, nil
}

func (m *Manager) Close() error {
	m.registryMu.Lock()
	defer m.registryMu.Unlock()
	if m.closed.Swap(true) {
		return nil
	}
	m.mu.RLock()
	states := make([]*serverState, 0, len(m.states))
	for _, state := range m.states {
		states = append(states, state)
	}
	m.mu.RUnlock()
	var result error
	for _, state := range states {
		result = errors.Join(result, closeState(state))
	}
	m.retiredWG.Wait()
	return result
}

func (m *Manager) lockServer(name string) (ServerConfig, *serverState, func(), error) {
	for {
		if m.closed.Load() {
			return ServerConfig{}, nil, nil, newError("MCP_MANAGER_CLOSED", "dynamic MCP manager is closed", false, nil, nil)
		}
		m.mu.RLock()
		_, exists := m.servers[name]
		state := m.states[name]
		m.mu.RUnlock()
		if !exists {
			return ServerConfig{}, nil, nil, newError("MCP_SERVER_NOT_FOUND", "dynamic MCP server not found", false, map[string]any{"server": name}, nil)
		}
		// Never hold registry RLock while waiting for another tool invocation. Metadata
		// updates remain instantaneous even when multiple calls queue on this server.
		state.mu.Lock()
		if m.closed.Load() {
			state.mu.Unlock()
			return ServerConfig{}, nil, nil, newError("MCP_MANAGER_CLOSED", "dynamic MCP manager is closed", false, nil, nil)
		}
		m.mu.RLock()
		cfg, exists := m.servers[name]
		current := m.states[name]
		m.mu.RUnlock()
		if !exists || current != state {
			state.mu.Unlock()
			continue
		}
		return cfg, state, state.mu.Unlock, nil
	}
}

func (m *Manager) ensureOpenLocked() error {
	if !m.closed.Load() {
		return nil
	}
	return newError("MCP_MANAGER_CLOSED", "dynamic MCP manager is closed", false, nil, nil)
}

func (m *Manager) runtimeConfig(cfg ServerConfig) (ServerConfig, error) {
	values, err := m.envs.Load(envstore.Scope{Kind: envstore.ScopeMCP, Name: cfg.Name})
	if err != nil {
		return ServerConfig{}, newError(
			"MCP_ENV_READ_FAILED",
			"read dynamic MCP environment",
			false,
			map[string]any{"server": cfg.Name},
			err,
		)
	}
	cfg.RuntimeEnv = values
	return cfg, nil
}

func (m *Manager) searchServers(name string) ([]ServerConfig, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if name != "" {
		cfg, exists := m.servers[name]
		if !exists {
			return nil, newError("MCP_SERVER_NOT_FOUND", "dynamic MCP server not found", false, map[string]any{"server": name}, nil)
		}
		if !cfg.Enabled {
			return nil, newError("MCP_SERVER_DISABLED", "dynamic MCP server is disabled", false, map[string]any{"server": name}, nil)
		}
		return []ServerConfig{cfg}, nil
	}
	configs := make([]ServerConfig, 0, len(m.servers))
	for _, cfg := range m.servers {
		if cfg.Enabled {
			configs = append(configs, cfg)
		}
	}
	sort.Slice(configs, func(i, j int) bool { return configs[i].Name < configs[j].Name })
	return configs, nil
}

func (m *Manager) ensureTools(ctx context.Context, name string) (map[string]Tool, error) {
	cfg, state, unlockState, err := m.lockServer(name)
	if err != nil {
		return nil, err
	}
	defer unlockState()
	if !cfg.Enabled {
		return nil, newError("MCP_SERVER_DISABLED", "dynamic MCP server is disabled", false, map[string]any{"server": name}, nil)
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(cfg.TimeoutMS)*time.Millisecond)
	defer cancel()
	if state.client == nil || !state.discovered {
		runtimeCfg, err := m.runtimeConfig(cfg)
		if err != nil {
			recordStateError(state, err)
			return nil, err
		}
		return refreshStateLocked(ctx, runtimeCfg, state)
	}
	return cloneTools(state.tools), nil
}

func initializeStateLocked(ctx context.Context, cfg ServerConfig, state *serverState) (map[string]Tool, error) {
	if state.client != nil {
		_ = state.client.close()
	}
	state.client = nil
	state.tools = nil
	client, err := newProtocolClient(cfg)
	if err != nil {
		recordStateError(state, err)
		return nil, err
	}
	if err := client.initialize(ctx); err != nil {
		_ = client.close()
		recordStateError(state, err)
		return nil, err
	}
	listed, err := client.listTools(ctx)
	if err != nil {
		_ = client.close()
		recordStateError(state, err)
		return nil, err
	}
	tools := make(map[string]Tool, len(listed))
	for _, tool := range listed {
		tool.Name = strings.TrimSpace(tool.Name)
		if tool.Name == "" {
			_ = client.close()
			err := newError("MCP_INVALID_RESPONSE", "MCP tools/list returned an empty tool name", false, map[string]any{"server": cfg.Name}, nil)
			recordStateError(state, err)
			return nil, err
		}
		if _, duplicate := tools[tool.Name]; duplicate {
			_ = client.close()
			err := newError("MCP_INVALID_RESPONSE", "MCP tools/list returned duplicate tool names", false, map[string]any{"server": cfg.Name, "tool": tool.Name}, nil)
			recordStateError(state, err)
			return nil, err
		}
		if tool.InputSchema == nil {
			tool.InputSchema = map[string]any{"type": "object", "additionalProperties": true}
		}
		validator, err := compileToolInputSchema(tool.InputSchema)
		if err != nil {
			_ = client.close()
			schemaErr := newError(
				"MCP_SCHEMA_INVALID",
				"MCP tools/list returned an invalid input schema",
				false,
				map[string]any{"server": cfg.Name, "tool": tool.Name, "reason": err.Error()},
				err,
			)
			recordStateError(state, schemaErr)
			return nil, schemaErr
		}
		tool.inputValidator = validator
		tools[tool.Name] = tool
	}
	state.client = client
	state.tools = tools
	state.lastError = ""
	state.lastErrorCode = ""
	state.refreshedAt = time.Now().UTC()
	state.discovered = true
	if info, ok := client.(interface{ ServerVersion() string }); ok {
		state.serverVersion = info.ServerVersion()
	}
	publishStateLocked(state)
	return cloneTools(tools), nil
}

func newProtocolClient(cfg ServerConfig) (protocolClient, error) {
	switch cfg.Transport {
	case TransportStreamableHTTP:
		return newStreamableHTTPClient(cfg), nil
	case TransportStdio:
		return newStdioClient(cfg), nil
	default:
		return nil, newError("MCP_TRANSPORT_UNSUPPORTED", fmt.Sprintf("unsupported MCP transport %q", cfg.Transport), false, map[string]any{"server": cfg.Name}, nil)
	}
}

func closeState(state *serverState) error {
	if state == nil {
		return nil
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	var err error
	if state.client != nil {
		err = state.client.close()
	}
	state.client = nil
	state.tools = nil
	state.lastError = ""
	state.lastErrorCode = ""
	state.refreshedAt = time.Time{}
	state.discovered = false
	publishStateLocked(state)
	return err
}

func summaryFor(cfg ServerConfig, state *serverState) ServerSummary {
	if state != nil {
		if snapshot := state.snapshot.Load(); snapshot != nil {
			return summaryForSnapshot(cfg, *snapshot)
		}
	}
	return summaryForSnapshot(cfg, indexSnapshot{})
}

func summaryForLocked(cfg ServerConfig, state *serverState) ServerSummary {
	return summaryForSnapshot(cfg, stateSnapshotLocked(state))
}

func recordStateError(state *serverState, err error) {
	state.lastError = err.Error()
	state.lastErrorCode = "MCP_ERROR"
	var mcpErr *Error
	if errors.As(err, &mcpErr) {
		state.lastErrorCode = mcpErr.Code
	}
	publishStateLocked(state)
}

func summarizeTools(server string, tools map[string]Tool) []ToolSummary {
	items := make([]ToolSummary, 0, len(tools))
	for _, tool := range tools {
		items = append(items, toolSummary(server, tool))
	}
	sort.Slice(items, func(i, j int) bool { return items[i].QualifiedName < items[j].QualifiedName })
	return items
}

func toolSummary(server string, tool Tool) ToolSummary {
	return ToolSummary{
		Name:          tool.Name,
		QualifiedName: qualifiedToolName(server, tool.Name),
		Title:         tool.Title,
		Description:   tool.Description,
		Server:        server,
	}
}

func cloneTools(input map[string]Tool) map[string]Tool {
	out := make(map[string]Tool, len(input))
	for name, tool := range input {
		out[name] = tool
	}
	return out
}

func toolMatchScore(query string, tool Tool) int {
	if query == "*" {
		return 1
	}
	name := strings.ToLower(tool.Name)
	title := strings.ToLower(tool.Title)
	description := strings.ToLower(tool.Description)
	score := 0
	if name == query {
		score += 100
	} else if strings.Contains(name, query) {
		score += 60
	}
	if strings.Contains(title, query) {
		score += 30
	}
	if strings.Contains(description, query) {
		score += 20
	}
	for _, token := range strings.Fields(query) {
		if strings.Contains(name, token) {
			score += 10
		}
		if strings.Contains(title, token) || strings.Contains(description, token) {
			score += 5
		}
	}
	return score
}

type toolCallObserver struct {
	call func(context.Context, string, func(context.Context) (map[string]any, error)) (map[string]any, error)
}

func (m *Manager) SetCallObserver(observer func(context.Context, string, func(context.Context) (map[string]any, error)) (map[string]any, error)) {
	if observer == nil {
		m.callObserver.Store(nil)
	} else {
		m.callObserver.Store(&toolCallObserver{call: observer})
	}
}
