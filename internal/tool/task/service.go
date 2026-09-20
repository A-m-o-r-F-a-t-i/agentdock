package task

import (
	"context"

	"github.com/uvwt/agentdock/internal/activity"
	"github.com/uvwt/agentdock/internal/config"
	"github.com/uvwt/agentdock/internal/taskstate"
)

type ConfigProvider func() config.Config

type EvolutionProvider interface {
	Guidance(context.Context, taskstate.Task) ([]taskstate.EvolutionContextItem, error)
	Candidates(context.Context, taskstate.Task) ([]taskstate.EvolutionContextItem, error)
	ValidateBindings(context.Context, taskstate.Task, []taskstate.EvolutionBinding) ([]taskstate.EvolutionBinding, error)
	ResolveBindings(context.Context, taskstate.Task) error
}

type Service struct {
	activity  *activity.Store
	config    ConfigProvider
	tasks     *taskstate.Store
	evolution EvolutionProvider
}

func New(configProvider ConfigProvider, tasks *taskstate.Store, evolution ...EvolutionProvider) *Service {
	service := &Service{config: configProvider, tasks: tasks}
	if len(evolution) > 0 {
		service.evolution = evolution[0]
	}
	return service
}
