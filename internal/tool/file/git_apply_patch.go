package file

import (
	"context"
	"os"
	"strings"

	workspacepkg "github.com/uvwt/agentdock/internal/workspace"
)

func (svc *Service) applyPatch(ctx context.Context, request EditRequest) (Result, error) {
	patch := request.Patch
	if patch == "" {
		return nil, toolError("INVALID_ARGUMENT", "patch is required", "validation")
	}
	workdir, err := svc.patchWorkdir(request.Workdir)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(strings.TrimSpace(patch), "*** Begin Patch") {
		return svc.applyEnvelopePatch(patch, request.DryRun, workdir.Display)
	}
	if restrictedFileTools(ctx) {
		return nil, toolError("PERMISSION_DENIED", "restricted profiles require a structured native patch; external Git is not started", "permission")
	}
	return svc.applyGitPatch(ctx, request, workdir)
}

func patchDiagnostic(code, path, message, output, reason string) map[string]any {
	return map[string]any{"code": code, "path": path, "message": message, "output": output, "reason": reason}
}

func (svc *Service) patchWorkdir(requested string) (workspacepkg.Path, error) {
	raw := requested
	if raw == "" {
		raw = "."
	}
	workdir, err := svc.ws.ResolveExisting(raw)
	if err != nil {
		return workspacepkg.Path{}, err
	}
	info, err := os.Stat(workdir.Abs)
	if err != nil {
		return workspacepkg.Path{}, err
	}
	if !info.IsDir() {
		return workspacepkg.Path{}, toolError("NOT_A_DIRECTORY", "workdir is not a directory", "validation")
	}
	return workdir, nil
}
