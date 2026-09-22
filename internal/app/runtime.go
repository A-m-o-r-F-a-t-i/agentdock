package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	acpruntime "github.com/uvwt/agentdock/internal/acp"
	"github.com/uvwt/agentdock/internal/activity"
	"github.com/uvwt/agentdock/internal/config"
	"github.com/uvwt/agentdock/internal/envstore"
	"github.com/uvwt/agentdock/internal/evolution"
	"github.com/uvwt/agentdock/internal/insertion"
	mcpclient "github.com/uvwt/agentdock/internal/mcp/client"
	"github.com/uvwt/agentdock/internal/permission"
	pluginregistry "github.com/uvwt/agentdock/internal/plugin"
	"github.com/uvwt/agentdock/internal/taskstate"
	toolacp "github.com/uvwt/agentdock/internal/tool/acp"
	toolbrowser "github.com/uvwt/agentdock/internal/tool/browser"
	toolcommand "github.com/uvwt/agentdock/internal/tool/command"
	toolcontract "github.com/uvwt/agentdock/internal/tool/contract"
	toolcore "github.com/uvwt/agentdock/internal/tool/core"
	toolfile "github.com/uvwt/agentdock/internal/tool/file"
	toolmcp "github.com/uvwt/agentdock/internal/tool/mcp"
	toolmedia "github.com/uvwt/agentdock/internal/tool/media"
	toolplugin "github.com/uvwt/agentdock/internal/tool/plugin"
	toolrecall "github.com/uvwt/agentdock/internal/tool/recall"
	toolskill "github.com/uvwt/agentdock/internal/tool/skill"
	tooltask "github.com/uvwt/agentdock/internal/tool/task"
	toolworkspace "github.com/uvwt/agentdock/internal/tool/workspace"
	"github.com/uvwt/agentdock/internal/workspace"
)

type Result = toolcore.Result

type Runtime struct {
	display                  *config.DisplayPreferences
	connections              clientConnections
	executionMaintenanceDone chan struct{}
	executionInstance        string
	insertions               *insertion.Store
	capabilityManager        *mcpclient.Manager
	capabilityUpdates        capabilityUpdates
	conversations            *activity.ConversationRegistry
	permissions              *permission.Store
	tasks                    *taskstate.Store
	executionMu              sync.Mutex
	activeCalls              map[string]*liveExecution
	pendingCalls             map[string]*preparedExecution
	executionWG              sync.WaitGroup

	workspaceRegistry *workspace.Registry
	workspaceTools    *toolworkspace.Service
	activity          *activity.Store
	cfg               config.Config
	toolNames         []string
	toolValidators    map[string]*toolcontract.InputValidator
	ws                *workspace.Workspace
	skills            *toolskill.Service
	command           *toolcommand.Service
	files             *toolfile.Service
	dynamicMCP        *toolmcp.Service
	plugins           *toolplugin.Service
	media             *toolmedia.Service
	browser           *toolbrowser.Service
	recall            *toolrecall.Service
	evolution         *evolution.Service
	taskTools         *tooltask.Service
	acp               *toolacp.Service
	lifecycleMu       sync.RWMutex
	commandCtx        context.Context
	commandCancel     context.CancelFunc
	closing           bool
	closeOnce         sync.Once
	closeErr          error
}

func NewRuntime(cfg config.Config) (*Runtime, error) {
	toolNames, toolValidators, err := compileAvailableToolContracts(cfg)
	if err != nil {
		return nil, fmt.Errorf("initialize tool contracts: %w", err)
	}
	ws, err := workspace.New(cfg.AgentDockDefaultDir)
	if err != nil {
		return nil, err
	}
	workspaceRegistry, err := workspace.NewRegistry(cfg.AgentDockHome, ws.Root())
	if err != nil {
		return nil, err
	}
	envs, err := envstore.New(cfg.AgentDockHome)
	if err != nil {
		return nil, err
	}
	skills, err := toolskill.New(cfg, ws, envs)
	if err != nil {
		return nil, err
	}
	pluginStore, err := pluginregistry.New(cfg.AgentDockHome)
	if err != nil {
		return nil, err
	}
	mcpClients, err := mcpclient.NewManager(cfg.AgentDockHome, envs)
	if err != nil {
		return nil, err
	}
	if err := mcpClients.SetExternalServerProvider(func() (map[string]mcpclient.ServerConfig, error) {
		members, providerErr := pluginStore.MCPServers()
		if providerErr != nil {
			return nil, providerErr
		}
		servers := make(map[string]mcpclient.ServerConfig, len(members))
		for name, member := range members {
			servers[name] = member.Config
		}
		return servers, nil
	}); err != nil {
		_ = mcpClients.Close()
		return nil, fmt.Errorf("initialize plugin MCP provider: %w", err)
	}
	tasks, err := taskstate.New(filepath.Join(cfg.AgentDockHome, "tasks"))
	if err != nil {
		_ = mcpClients.Close()
		return nil, err
	}
	activityStore, err := activity.New(filepath.Join(cfg.AgentDockHome, "tasks", "activity"), activity.Options{}, cfg.AuthToken, cfg.NexusDeviceToken)
	if err != nil {
		_ = mcpClients.Close()
		return nil, fmt.Errorf("initialize activity journal: %w", err)
	}
	conversations, err := activity.NewConversationRegistry(filepath.Join(cfg.AgentDockHome, "execution"))
	if err != nil {
		_ = mcpClients.Close()
		return nil, fmt.Errorf("initialize conversations: %w", err)
	}
	permissions, err := permission.New(filepath.Join(cfg.AgentDockHome, "execution", "permissions"))
	if err != nil {
		_ = mcpClients.Close()
		return nil, fmt.Errorf("initialize permissions: %w", err)
	}
	instance, err := activity.NewExecutionID("call_")
	if err != nil {
		return nil, err
	}
	insertions, err := insertion.New(filepath.Join(cfg.AgentDockHome, "execution", "insertions"), instance, nil)
	if err != nil {
		_ = mcpClients.Close()
		return nil, fmt.Errorf("initialize insertion queue: %w", err)
	}
	commandCtx, commandCancel := context.WithCancel(context.Background())
	runtime := &Runtime{
		display:           config.NewDisplayPreferences(cfg.AgentDockHome, cfg.MCPAppsEnabled),
		executionInstance: instance, insertions: insertions, capabilityManager: mcpClients,
		conversations: conversations, permissions: permissions, tasks: tasks,
		activeCalls: map[string]*liveExecution{}, pendingCalls: map[string]*preparedExecution{},
		workspaceRegistry: workspaceRegistry, workspaceTools: toolworkspace.New(workspaceRegistry),
		cfg: cfg, ws: ws, skills: skills, activity: activityStore,
		toolNames: toolNames, toolValidators: toolValidators,
		commandCtx: commandCtx, commandCancel: commandCancel,
	}
	if err := runtime.skills.SetPluginSkillProvider(
		func(name string) (toolskill.PluginSkill, bool, error) {
			member, found, lookupErr := pluginStore.Skill(name)
			return toolskill.PluginSkill{
				Name: member.Name, Plugin: member.Plugin, Path: member.Path, Enabled: member.Enabled,
			}, found, lookupErr
		},
		func() ([]toolskill.PluginSkill, error) {
			members, lookupErr := pluginStore.Skills()
			if lookupErr != nil {
				return nil, lookupErr
			}
			items := make([]toolskill.PluginSkill, 0, len(members))
			for _, member := range members {
				items = append(items, toolskill.PluginSkill{
					Name: member.Name, Plugin: member.Plugin, Path: member.Path, Enabled: member.Enabled,
				})
			}
			return items, nil
		},
	); err != nil {
		_ = mcpClients.Close()
		return nil, fmt.Errorf("initialize plugin Skill provider: %w", err)
	}
	runtime.command = toolcommand.New(func() config.Config { return runtime.cfg }, ws, envs, skills.ResolveActive, runtime.commandExecutionContext)
	runtime.command.SetActivityStore(activityStore)
	runtime.files = toolfile.New(ws, skills.ResolveResource, runtime.command.CommandEnv)
	mcpClients.SetCallObserver(runtime.observeRemoteTool)
	runtime.dynamicMCP = toolmcp.New(mcpClients, envs)
	runtime.plugins = toolplugin.New(
		pluginStore,
		func(name string) (toolplugin.SkillItem, bool, error) {
			item, found, lookupErr := runtime.skills.CapabilityItem(name)
			return toolplugin.SkillItem{
				Name: item.Name, Description: item.Description, File: item.File,
				Bundled: item.Bundled, Enabled: item.Enabled, Plugin: item.Plugin,
			}, found, lookupErr
		},
		func(ctx context.Context, name string, expand bool) (toolplugin.MCPItem, bool, error) {
			var item toolmcp.CapabilityItem
			var tools []toolmcp.CapabilityToolItem
			var found bool
			var lookupErr error
			if expand {
				item, tools, found, lookupErr = runtime.dynamicMCP.PluginCapabilityItem(ctx, name)
			} else {
				item, found, lookupErr = runtime.dynamicMCP.CapabilityItem(name)
			}
			mappedTools := make([]toolplugin.MCPToolItem, 0, len(tools))
			for _, tool := range tools {
				mappedTools = append(mappedTools, toolplugin.MCPToolItem{
					Name: tool.Name, QualifiedName: tool.QualifiedName, Title: tool.Title,
					Description: tool.Description, Server: tool.Server,
				})
			}
			return toolplugin.MCPItem{
				Name: item.Name, Description: item.Description, Plugin: item.Plugin, Status: item.Status,
				Revision: item.Revision, ServerVersion: item.ServerVersion, ToolCountKnown: item.ToolCountKnown,
				ToolCount: item.ToolCount, LastErrorCode: item.LastErrorCode,
				ToolLoadError: item.ToolLoadError, Enabled: item.Enabled, Tools: mappedTools,
			}, found, lookupErr
		},
	)
	runtime.skills.SetPluginMembershipLookup(func(name string) (string, bool, bool, error) {
		membership, owned, lookupErr := runtime.plugins.SkillMembership(name)
		return membership.Plugin, membership.Enabled, owned, lookupErr
	})
	runtime.dynamicMCP.SetPluginMembershipLookup(func(name string) (string, bool, bool, error) {
		membership, owned, lookupErr := runtime.plugins.MCPMembership(name)
		return membership.Plugin, membership.Enabled, owned, lookupErr
	})
	runtime.dynamicMCP.SetHeavyPluginLookup(func(name string) (bool, error) {
		membership, owned, err := runtime.plugins.MCPMembership(name)
		return owned && membership.Heavy, err
	})
	runtime.media = toolmedia.New(cfg, ws, runtime.command.InternalCommandEnv)
	runtime.browser = toolbrowser.New(
		toolbrowser.Config{AgentDockHome: cfg.AgentDockHome, ExecutablePath: cfg.BrowserExecutablePath, CDPURL: cfg.BrowserCDPURL, ReuseExistingCDP: cfg.BrowserReuseExistingCDP},
		runtime.media.PublishBrowserScreenshot,
	)
	runtime.recall = toolrecall.New(func() config.Config { return runtime.cfg })
	runtime.evolution = evolution.New(func() config.Config { return runtime.cfg }, tasks)
	runtime.taskTools = tooltask.New(func() config.Config { return runtime.cfg }, tasks, runtime.evolution)
	runtime.taskTools.SetActivityStore(activityStore)
	if cfg.ACPEnabled {
		managers := make(map[string]*acpruntime.Manager)
		for _, profile := range cfg.EffectiveACPProfiles() {
			acpEnvironment := make(map[string]string, len(profile.EnvFromEnv))
			for childName, hostName := range profile.EnvFromEnv {
				value, exists := os.LookupEnv(hostName)
				if !exists {
					_ = toolacp.NewMulti(cfg.EffectiveACPDefaultProfile(), managers).Close()
					_ = runtime.Close()
					return nil, fmt.Errorf("required ACP environment variable %s for profile %s is missing", hostName, profile.ID)
				}
				acpEnvironment[childName] = value
			}
			manager, err := acpruntime.NewManager(acpruntime.Options{
				Home:       cfg.AgentDockHome,
				DefaultCWD: cfg.AgentDockDefaultDir,
				Agent: acpruntime.AgentSpec{
					Name: profile.ID, Command: profile.Command, Args: append([]string(nil), profile.Args...), Environment: acpEnvironment,
				},
				MaxConcurrentRuns:  cfg.ACPMaxPrompts,
				InteractionTimeout: time.Duration(cfg.ACPInteractionMS) * time.Millisecond,
			})
			if err != nil {
				_ = toolacp.NewMulti(cfg.EffectiveACPDefaultProfile(), managers).Close()
				_ = runtime.Close()
				return nil, fmt.Errorf("initialize ACP profile %s: %w", profile.ID, err)
			}
			managers[profile.ID] = manager
		}
		runtime.acp = toolacp.NewMulti(cfg.EffectiveACPDefaultProfile(), managers)
	}
	if err = runtime.recoverExecutionState(context.Background()); err != nil {
		_ = runtime.Close()
		return nil, fmt.Errorf("recover execution state: %w", err)
	}
	runtime.startExecutionMaintenance()
	return runtime, nil
}

func (r *Runtime) Config() config.Config           { return r.cfg }
func (r *Runtime) Workspace() *workspace.Workspace { return r.ws }

func (r *Runtime) Close() error {
	if r == nil {
		return nil
	}
	r.closeOnce.Do(func() {
		var closeErrors []error
		r.lifecycleMu.Lock()
		r.closing = true
		commandCancel := r.commandCancel
		r.lifecycleMu.Unlock()

		// 先禁止新的 command reservation，并等已经拿到 reservation 的启动流程离开
		// cmd.Start/平台进程控制器建立窗口。此处不能持有 lifecycleMu 等待，否则启动路径
		// 一旦需要读取 Runtime 生命周期状态就会形成锁顺序死锁。
		if r.command != nil {
			r.command.BeginClose()
			if err := r.command.WaitForStarts(); err != nil {
				closeErrors = append(closeErrors, err)
			}
		}
		r.cancelPendingOnClose()
		if commandCancel != nil {
			commandCancel()
		}
		if r.acp != nil {
			if err := r.acp.Close(); err != nil {
				closeErrors = append(closeErrors, fmt.Errorf("close ACP runtime: %w", err))
			}
		}
		if r.browser != nil {
			if err := r.browser.Close(); err != nil {
				closeErrors = append(closeErrors, fmt.Errorf("close browser runtime: %w", err))
			}
		}
		if r.command != nil {
			if err := r.command.Close(); err != nil {
				closeErrors = append(closeErrors, err)
			}
		}
		if r.dynamicMCP != nil {
			if err := r.dynamicMCP.Close(); err != nil {
				closeErrors = append(closeErrors, fmt.Errorf("close dynamic MCP clients: %w", err))
			}
		}
		if err := r.drainExecutionsOnClose(); err != nil {
			closeErrors = append(closeErrors, err)
		}
		r.closeErr = errors.Join(closeErrors...)
	})
	return r.closeErr
}

func (r *Runtime) commandExecutionContext() (context.Context, error) {
	r.lifecycleMu.RLock()
	defer r.lifecycleMu.RUnlock()
	if r.closing || r.commandCtx == nil {
		return nil, toolError("RUNTIME_CLOSING", "AgentDock runtime is shutting down", "runtime")
	}
	return r.commandCtx, nil
}

func (r *Runtime) ToolNames() []string {
	return append([]string(nil), r.toolNames...)
}

func (r *Runtime) ToolDefinitions() []ToolDefinition {
	definitions := make([]ToolDefinition, 0, len(r.toolNames))
	for _, name := range r.toolNames {
		definition, _ := toolDefinitionForConfig(name, r.cfg)
		definitions = append(definitions, definition)
	}
	return definitions
}

func (r *Runtime) ToolDefinition(name string) (ToolDefinition, bool) {
	if _, available := r.toolValidators[name]; !available {
		return ToolDefinition{}, false
	}
	return toolDefinitionForConfig(name, r.cfg)
}

func (r *Runtime) Call(ctx context.Context, name string, args map[string]any) (Result, error) {
	if args == nil {
		args = map[string]any{}
	}
	spec, ok := toolSpecByName(name)
	if !ok {
		spec = ToolSpec{Name: name}
	}
	return r.callObserved(ctx, spec, args)
}

func (r *Runtime) validateToolArguments(name string, args map[string]any) error {
	validator, available := r.toolValidators[name]
	if !available {
		return toolErrorDetails("UNKNOWN_TOOL", "tool is not available", "validation", map[string]any{"tool": name})
	}
	if args == nil {
		args = map[string]any{}
	}
	if err := validator.Validate(args); err != nil {
		return toolErrorDetails(
			"INVALID_ARGUMENT",
			"tool arguments do not match the declared input schema",
			"validation",
			map[string]any{"tool": name, "reason": toolcontract.CompactValidationError(err)},
		)
	}
	return nil
}
