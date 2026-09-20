package workspace

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

var taskDirectoryID = regexp.MustCompile(`^tsk_[A-Za-z0-9_-]{1,76}$`)

type TargetRequest struct {
	Kind         string `json:"target_kind,omitempty"`
	Path         string `json:"path,omitempty"`
	TaskID       string `json:"task_id,omitempty"`
	ExternalPath string `json:"external_path,omitempty"`
}

type ResolvedTarget struct {
	WorkspaceID   string `json:"workspace_id"`
	TaskID        string `json:"task_id,omitempty"`
	Kind          string `json:"target_kind"`
	LogicalPath   string `json:"logical_path"`
	ResolvedPath  string `json:"resolved_path"`
	Root          string `json:"root"`
	Runtime       string `json:"runtime"`
	Distribution  string `json:"wsl_distribution,omitempty"`
	RulesRevision int    `json:"rules_revision"`
}

// ResolveTarget is semantic path routing, not a shell sandbox. Command text may still
// access other files using the operating-system permissions of the existing executor.
func ResolveTarget(record Record, request TargetRequest) (ResolvedTarget, error) {
	result := ResolvedTarget{WorkspaceID: record.ID, TaskID: request.TaskID, Kind: request.Kind, LogicalPath: request.Path, Runtime: record.Runtime, Distribution: record.Distribution, RulesRevision: record.RulesRevision}
	if err := validateRecord(record); err != nil {
		return result, err
	}
	if result.Kind == "" {
		result.Kind = "source"
	}
	root := record.Root
	switch result.Kind {
	case "source":
	case "artifact", "scratch":
		if !taskDirectoryID.MatchString(request.TaskID) {
			return result, errors.New("task_id is required for artifact and scratch routing")
		}
		if result.Kind == "artifact" {
			root = record.ArtifactRoot
		} else {
			root = record.ScratchRoot
		}
		if root == "" {
			return result, fmt.Errorf("%w: configure %s_root", ErrWorkspaceRequired, result.Kind)
		}
		if record.Runtime == "wsl" {
			root = path.Join(root, request.TaskID)
		} else {
			root = filepath.Join(root, request.TaskID)
		}
	case "cache":
		root = record.CacheRoot
		if root == "" {
			return result, fmt.Errorf("%w: configure cache_root", ErrWorkspaceRequired)
		}
	case "external":
		if request.ExternalPath == "" {
			return result, errors.New("external_path must explicitly name the one-call external target")
		}
		if record.Runtime == "wsl" {
			if !validPOSIXRoot(request.ExternalPath) {
				return result, ErrWorkspaceRequired
			}
			result.ResolvedPath = path.Clean(request.ExternalPath)
			if request.Path != "" && request.Path != "." && (!validPOSIXRoot(request.Path) || path.Clean(request.Path) != result.ResolvedPath) {
				return result, errors.New("external path must match the explicitly declared one-call target")
			}
		} else {
			var err error
			result.ResolvedPath, err = canonicalNativePath(request.ExternalPath)
			if err != nil {
				return result, err
			}
			if request.Path != "" && request.Path != "." {
				// Preparation captures a canonical path; revalidation must compare
				// that same target with the original spelling (including 8.3 aliases).
				actual, err := canonicalNativePath(request.Path)
				if err != nil {
					return result, err
				}
				if actual != result.ResolvedPath {
					return result, errors.New("external path must match the explicitly declared one-call target")
				}
			}
		}
		result.Root = result.ResolvedPath
		result.LogicalPath = request.ExternalPath
		return result, nil
	default:
		return result, errors.New("target_kind must be source, artifact, scratch, cache or external")
	}
	logical := request.Path
	if logical == "" {
		logical = "."
	}
	if record.Runtime == "wsl" {
		if strings.ContainsRune(logical, 0) || strings.Contains(logical, "\\") || len(logical) > 4096 {
			return result, errors.New("invalid WSL path")
		}
		root = path.Clean(root)
		candidate := logical
		if !path.IsAbs(candidate) {
			candidate = path.Join(root, candidate)
		}
		candidate = path.Clean(candidate)
		if candidate != root && !strings.HasPrefix(candidate, root+"/") {
			return result, ErrWorkspaceBoundary
		}
		result.Root, result.ResolvedPath = root, candidate
	} else {
		canonicalRoot, err := canonicalNativePath(root)
		if err != nil {
			return result, err
		}
		candidate := logical
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(canonicalRoot, candidate)
		}
		candidate, err = canonicalNativePath(candidate)
		if err != nil {
			return result, err
		}
		relative, err := filepath.Rel(canonicalRoot, candidate)
		if err != nil || outsideRelative(filepath.ToSlash(relative)) || filepath.IsAbs(relative) {
			return result, ErrWorkspaceBoundary
		}
		result.Root, result.ResolvedPath = canonicalRoot, candidate
	}
	result.LogicalPath = logical
	return result, nil
}

// ResolveCommandPreview resolves an execution target without creating auxiliary
// directories. The dispatcher calls ResolveCommandDirectory only after approval.
func ResolveCommandPreview(record Record, request TargetRequest) (ResolvedTarget, error) {
	if request.Path == "" && (request.Kind == "" || request.Kind == "source") {
		request.Path = record.DefaultWorkdir
	}
	target, err := ResolveTarget(record, request)
	if err != nil {
		return target, err
	}
	if record.Runtime != "wsl" && target.Kind != "artifact" && target.Kind != "scratch" && target.Kind != "cache" {
		if err = directoryExists(target.ResolvedPath); err != nil {
			return target, err
		}
	}
	return target, nil
}

func ResolveCommandDirectory(record Record, request TargetRequest) (ResolvedTarget, error) {
	if request.Path == "" && (request.Kind == "" || request.Kind == "source") {
		request.Path = record.DefaultWorkdir
	}
	target, err := ResolveTarget(record, request)
	if err != nil {
		return target, err
	}
	// Creating source directories is an explicit registry operation. Auxiliary directories
	// are created only when the caller explicitly selects their semantic destination.
	if record.Runtime != "wsl" {
		if target.Kind == "artifact" || target.Kind == "scratch" || target.Kind == "cache" {
			if err = os.MkdirAll(target.ResolvedPath, 0755); err != nil {
				return target, err
			}
		}
		if err = directoryExists(target.ResolvedPath); err != nil {
			return target, err
		}
	}
	return target, nil
}

func canonicalNativePath(value string) (string, error) {
	if value == "" || !filepath.IsAbs(value) || strings.ContainsRune(value, 0) || len(value) > 4096 {
		return "", ErrWorkspaceRequired
	}
	if strings.HasPrefix(value, `\\?\`) || strings.HasPrefix(value, `\\.\`) {
		return "", errors.New("device namespace paths are not supported by workspace routing")
	}
	candidate := filepath.Clean(value)
	if filepath.Dir(candidate) == candidate {
		return "", ErrWorkspaceRequired
	}
	// Avoid alternate data streams and drive-relative path syntax on Windows.
	if HostRuntime() == "windows" && strings.Contains(strings.TrimPrefix(candidate, filepath.VolumeName(candidate)), ":") {
		return "", errors.New("alternate data stream paths are not supported")
	}
	ancestor := candidate
	suffix := []string{}
	for {
		info, err := os.Lstat(ancestor)
		if err == nil {
			resolved, err := filepath.EvalSymlinks(ancestor)
			if err != nil {
				return "", err
			}
			if len(suffix) > 0 {
				actual, err := os.Stat(resolved)
				if err != nil {
					return "", err
				}
				if !actual.IsDir() {
					return "", errors.New("workspace path has a non-directory ancestor")
				}
			} else if info.Mode()&os.ModeSymlink == 0 && !info.IsDir() && !info.Mode().IsRegular() {
				return "", errors.New("workspace target is not a regular filesystem path")
			}
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return filepath.Clean(resolved), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return "", fmt.Errorf("workspace parent not found: %w", err)
		}
		suffix = append(suffix, filepath.Base(ancestor))
		ancestor = parent
	}
}

func directoryExists(value string) error {
	info, err := os.Stat(value)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("workspace root or workdir must be a directory")
	}
	return nil
}
func outsideRelative(value string) bool { return value == ".." || strings.HasPrefix(value, "../") }
func validPOSIXRoot(value string) bool {
	return path.IsAbs(value) && path.Clean(value) != "/" && !strings.ContainsAny(value, "\\\x00") && len(value) <= 4096
}
