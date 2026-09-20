package command

import (
	"context"
	"sync"

	"github.com/uvwt/agentdock/internal/activity"
	"github.com/uvwt/agentdock/internal/config"
	"github.com/uvwt/agentdock/internal/envstore"
	"github.com/uvwt/agentdock/internal/tool/command/session"
	"github.com/uvwt/agentdock/internal/workspace"
)

type ConfigProvider func() config.Config
type SkillResolver func(skill string) (string, error)
type CommandContext func() (context.Context, error)

type Service struct {
	activityMu     sync.Mutex
	activeCommands map[string]*session.Session
	activity       *activity.Store
	activityWG     sync.WaitGroup
	config         ConfigProvider
	ws             *workspace.Workspace
	envs           *envstore.Store
	sessions       *session.Store
	resolveSkill   SkillResolver
	commandContext CommandContext
}

func New(configProvider ConfigProvider, ws *workspace.Workspace, envs *envstore.Store, resolveSkill SkillResolver, commandContext CommandContext) *Service {
	return &Service{
		activeCommands: map[string]*session.Session{},
		config:         configProvider, ws: ws, envs: envs, sessions: session.NewStore(),
		resolveSkill: resolveSkill, commandContext: commandContext,
	}
}

func (s *Service) CommandEnv(skillName string, extra map[string]string) ([]string, error) {
	return s.commandEnv(skillName, extra)
}

func (s *Service) InternalCommandEnv(extra map[string]string) ([]string, error) {
	return s.internalCommandEnv(extra)
}

// MaxOutputBytes is the public exec/session output contract limit used by schema generation.
const MaxOutputBytes = maxCommandOutputBytes
