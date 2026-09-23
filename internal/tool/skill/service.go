package skill

import (
	"strings"
	"sync"

	"github.com/uvwt/agentdock/internal/config"
	"github.com/uvwt/agentdock/internal/envstore"
	skills "github.com/uvwt/agentdock/internal/skill"
	skillstate "github.com/uvwt/agentdock/internal/skill/state"
	"github.com/uvwt/agentdock/internal/workspace"
)

const runtimeAPISource = "agentdock-api"

type PluginMembershipLookup func(string) (plugin string, enabled bool, owned bool, err error)

type PluginSkill struct {
	Name    string
	Plugin  string
	Path    string
	Enabled bool
}

type PluginSkillLookup func(string) (PluginSkill, bool, error)
type PluginSkillsLookup func() ([]PluginSkill, error)

type Service struct {
	workspaceMu       sync.RWMutex
	workspaceByID     map[string]string
	workspaceIDByRoot map[string]string
	workspaceIssued   map[string]map[string]struct{}

	manager          *skills.Manager
	state            *skillstate.Store
	ws               *workspace.Workspace
	envs             *envstore.Store
	pluginMembership PluginMembershipLookup
	pluginSkill      PluginSkillLookup
	pluginSkills     PluginSkillsLookup
}

func New(cfg config.Config, ws *workspace.Workspace, envs *envstore.Store) (*Service, error) {
	stateDir, err := config.SkillStateDir(cfg)
	if err != nil {
		return nil, err
	}
	state, err := skillstate.New(stateDir)
	if err != nil {
		return nil, err
	}
	manager, err := skills.New(state)
	if err != nil {
		return nil, err
	}
	return &Service{manager: manager, state: state, ws: ws, envs: envs, workspaceByID: map[string]string{}, workspaceIDByRoot: map[string]string{}, workspaceIssued: map[string]map[string]struct{}{}}, nil
}

func (s *Service) SetPluginMembershipLookup(lookup PluginMembershipLookup) {
	s.pluginMembership = lookup
}

func (s *Service) SetPluginSkillProvider(lookup PluginSkillLookup, list PluginSkillsLookup) error {
	if list != nil {
		members, err := list()
		if err != nil {
			return err
		}
		standalone, err := s.state.ListSkills()
		if err != nil {
			return err
		}
		names := make(map[string]struct{}, len(standalone))
		for _, name := range standalone {
			names[name] = struct{}{}
		}
		for _, member := range members {
			if _, exists := names[member.Name]; exists {
				return toolErrorDetails("PLUGIN_SKILL_CONFLICT", "plugin Skill conflicts with an installed standalone Skill", "validation", map[string]any{"skill": member.Name, "plugin": member.Plugin})
			}
		}
	}
	s.pluginSkill = lookup
	s.pluginSkills = list
	return nil
}

func (s *Service) ResolveActive(skill string) (string, error) {
	skill = strings.TrimSpace(skill)
	if s.pluginSkill != nil {
		member, found, err := s.pluginSkill(skill)
		if err != nil {
			return "", toolErrorCause("PLUGIN_STATE_INVALID", "resolve plugin Skill", "runtime", map[string]any{"skill": skill}, err)
		}
		if found {
			if !member.Enabled {
				return "", toolErrorDetails("PLUGIN_MEMBER_DISABLED", "plugin Skill is disabled", "validation", map[string]any{"skill": skill, "plugin": member.Plugin})
			}
			return member.Path, nil
		}
	}
	if err := s.ensureAvailable(skill); err != nil {
		return "", err
	}
	path, err := s.state.Resolve(skill, "")
	if err != nil {
		return "", toolErrorDetails("SKILL_CONTEXT_INVALID", "resolve active Skill directory: "+err.Error(), "validation", map[string]any{"skill": skill, "reason": err.Error()})
	}
	return path, nil
}

func (s *Service) ensureAvailable(skill string) error {
	skill = strings.TrimSpace(skill)
	if s.pluginSkill != nil {
		member, found, err := s.pluginSkill(skill)
		if err != nil {
			return toolErrorCause("PLUGIN_STATE_INVALID", "read plugin Skill availability", "runtime", map[string]any{"skill": skill}, err)
		}
		if found {
			if !member.Enabled {
				return toolErrorDetails("PLUGIN_MEMBER_DISABLED", "plugin Skill is disabled", "validation", map[string]any{"skill": skill, "plugin": member.Plugin})
			}
			return nil
		}
	}
	selection, err := s.state.Snapshot(skill)
	if err != nil {
		return toolErrorDetails("SKILL_STATE_INVALID", "read Skill availability: "+err.Error(), "runtime", map[string]any{"skill": skill})
	}
	if selection.Disabled {
		return toolErrorDetails("SKILL_DISABLED", "Skill is disabled", "validation", map[string]any{"skill": skill})
	}
	if s.pluginMembership == nil {
		return nil
	}
	plugin, enabled, owned, err := s.pluginMembership(skill)
	if err != nil {
		return toolErrorCause("PLUGIN_STATE_INVALID", "read Skill plugin availability", "runtime", map[string]any{"skill": skill}, err)
	}
	if owned && !enabled {
		return toolErrorDetails("PLUGIN_DISABLED", "Skill belongs to a disabled plugin", "validation", map[string]any{"skill": skill, "plugin": plugin})
	}
	return nil
}

func (s *Service) baseEnabled(skill string) (bool, error) {
	if s.pluginSkill != nil {
		member, found, err := s.pluginSkill(strings.TrimSpace(skill))
		if err != nil {
			return false, err
		}
		if found {
			return member.Enabled, nil
		}
	}
	selection, err := s.state.Snapshot(strings.TrimSpace(skill))
	if err != nil {
		return false, err
	}
	return !selection.Disabled, nil
}

func (s *Service) rejectPluginManagedSkill(skill, action string) error {
	if s.pluginSkill == nil {
		return nil
	}
	member, found, err := s.pluginSkill(strings.TrimSpace(skill))
	if err != nil {
		return toolErrorCause("PLUGIN_STATE_INVALID", "inspect plugin Skill ownership", "runtime", map[string]any{"skill": skill}, err)
	}
	if !found {
		return nil
	}
	return toolErrorDetails(
		"PLUGIN_MEMBER_MANAGED",
		"plugin-contained Skills are installed, updated, removed, and switched through plugin_manage",
		"validation",
		map[string]any{"skill": skill, "plugin": member.Plugin, "action": action},
	)
}

func (s *Service) scopedEnvAction(kind envstore.ScopeKind, name, action string, request PackageRequest) (Result, error) {
	scope := envstore.Scope{Kind: kind, Name: strings.TrimSpace(name)}
	switch action {
	case "env_set":
		key := strings.TrimSpace(request.Key)
		if key == "" || request.Value == nil {
			return nil, toolErrorDetails("VALIDATION_ERROR", "key and value are required for env_set", "validation", map[string]any{"scope": scope.Name})
		}
		if config.IsReservedCommandEnvironmentKey(key) {
			return nil, toolErrorDetails("VALIDATION_ERROR", "environment variable is reserved by the runtime", "validation", map[string]any{"key": key})
		}
		text := *request.Value
		if err := s.envs.Set(scope, key, text); err != nil {
			return nil, skillEnvError(scope, err)
		}
		return Result{"action": action, "name": scope.Name, "key": key, "configured": text != ""}, nil
	case "env_unset":
		key := strings.TrimSpace(request.Key)
		if key == "" {
			return nil, toolErrorDetails("VALIDATION_ERROR", "key is required for env_unset", "validation", map[string]any{"scope": scope.Name})
		}
		removed, err := s.envs.Unset(scope, key)
		if err != nil {
			return nil, skillEnvError(scope, err)
		}
		return Result{"action": action, "name": scope.Name, "key": key, "removed": removed}, nil
	case "env_list":
		items, err := s.envs.List(scope)
		if err != nil {
			return nil, skillEnvError(scope, err)
		}
		return Result{"action": action, "name": scope.Name, "items": items, "count": len(items)}, nil
	default:
		return nil, toolErrorDetails("INVALID_ACTION", "unsupported environment action", "validation", map[string]any{"action": action})
	}
}

func skillEnvError(scope envstore.Scope, err error) error {
	return toolErrorDetails("ENV_STORE_ERROR", "manage scoped environment", "validation", map[string]any{"kind": scope.Kind, "name": scope.Name, "reason": err.Error()})
}
