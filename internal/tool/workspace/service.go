// Package workspace exposes the persistent workspace registry through one action tool.
package workspace

import (
	"context"
	"errors"

	"github.com/uvwt/agentdock/internal/tool/core"
	hostworkspace "github.com/uvwt/agentdock/internal/workspace"
)

const ToolManage = "workspace_manage"

type Result = core.Result

type Request struct {
	hostworkspace.RegisterInput
	Action       string `json:"action"`
	TargetKind   string `json:"target_kind,omitempty"`
	Path         string `json:"path,omitempty"`
	TaskID       string `json:"task_id,omitempty"`
	ExternalPath string `json:"external_path,omitempty"`
}

type Service struct{ registry *hostworkspace.Registry }

func New(registry *hostworkspace.Registry) *Service { return &Service{registry: registry} }

func (s *Service) Manage(ctx context.Context, request Request) (Result, error) {
	switch request.Action {
	case "list":
		records, defaultID, err := s.registry.List(ctx)
		if err != nil {
			return nil, registryError(err)
		}
		return Result{"action": "list", "workspaces": records, "count": len(records), "default_workspace_id": defaultID}, nil
	case "get":
		record, err := s.registry.Select(ctx, request.WorkspaceID, request.Project)
		if err != nil {
			return nil, registryError(err)
		}
		return Result{"action": "get", "workspace": record}, nil
	case "register":
		record, err := s.registry.Register(ctx, request.RegisterInput)
		if err != nil {
			return nil, registryError(err)
		}
		return Result{"action": "register", "workspace": record}, nil
	case "resolve":
		record, err := s.registry.Select(ctx, request.WorkspaceID, request.Project)
		if err != nil {
			return nil, registryError(err)
		}
		target, err := hostworkspace.ResolveTarget(record, hostworkspace.TargetRequest{Kind: request.TargetKind, Path: request.Path, TaskID: request.TaskID, ExternalPath: request.ExternalPath})
		if err != nil {
			return nil, registryError(err)
		}
		return Result{"action": "resolve", "target": target}, nil
	default:
		return nil, &core.ToolError{Code: "INVALID_ACTION", Message: "workspace action must be list, get, register or resolve", Category: "validation"}
	}
}

func registryError(err error) error {
	code := "WORKSPACE_ERROR"
	switch {
	case errors.Is(err, hostworkspace.ErrWorkspaceRequired):
		code = "WORKSPACE_REQUIRED"
	case errors.Is(err, hostworkspace.ErrWorkspaceNotFound):
		code = "WORKSPACE_NOT_FOUND"
	case errors.Is(err, hostworkspace.ErrWorkspaceConflict):
		code = "WORKSPACE_REVISION_CONFLICT"
	case errors.Is(err, hostworkspace.ErrWorkspaceBoundary):
		code = "WORKSPACE_PATH_OUTSIDE_ROOT"
	}
	return &core.ToolError{Code: code, Message: err.Error(), Category: "validation"}
}
