//go:build windows

package file

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	pathpkg "path"
	"sort"
	"strings"
)

func (svc *Service) fileEditWSL(ctx context.Context, request EditRequest, selection fileRuntimeSelection) (Result, error) {
	action := strings.ToLower(strings.TrimSpace(request.Action))
	if action == "" {
		return nil, toolError("MISSING_ACTION", "file_edit requires action", "validation")
	}
	if action == "patch" {
		return svc.fileEditPatchWSL(ctx, request, selection)
	}
	if action != "replace" && action != "add" && action != "delete" && action != "move" {
		return nil, toolError("INVALID_ACTION", "unsupported file_edit action", "validation")
	}
	path, err := resolveWSLFilePath(request.Path)
	if err != nil {
		return nil, err
	}
	if action == "delete" && request.Recursive {
		return nil, toolError("INVALID_ARGUMENT", "runtime=wsl file_edit only deletes regular UTF-8 files; recursive directory deletion is not supported", "validation")
	}
	stage, err := svc.loadWSLPatchStage(ctx, selection, path, action == "add")
	if err != nil {
		return nil, unchangedEditError(err)
	}
	staged := map[string]*wslPatchStage{path: stage}
	result := Result{"action": action, "path": path, "dry_run": request.DryRun}
	switch action {
	case "replace":
		prepared, updated, err := prepareTextReplacement(path, stage.OldContent, request)
		if err != nil {
			return nil, unchangedEditError(err)
		}
		result = prepared
		stage.NewContent = &updated
	case "add":
		if stage.Existed && !request.Overwrite {
			return nil, toolErrorDetails("FILE_EXISTS", "file already exists; set overwrite=true to replace it", "validation", map[string]any{"path": path})
		}
		prepared, _, err := prepareTextAddition(path, stage.OldContent, request.Content, stage.Existed, request)
		if err != nil {
			return nil, unchangedEditError(err)
		}
		result = prepared
		content := request.Content
		stage.NewContent = &content
	case "delete":
		stage.NewContent = nil
		result["summary"] = "deleted " + path
	case "move":
		destinationPath, err := resolveWSLFilePath(request.NewPath)
		if err != nil {
			return nil, err
		}
		result["new_path"], result["summary"] = destinationPath, "moved "+path+" to "+destinationPath
		if path == destinationPath {
			result["changed"], result["files_changed"], result["insertions"], result["deletions"] = false, 0, 0, 0
			return addFileRuntimeResult(result, selection), nil
		}
		destination, err := svc.loadWSLPatchStage(ctx, selection, destinationPath, true)
		if err != nil {
			return nil, unchangedEditError(err)
		}
		if destination.Existed && !request.Overwrite {
			return nil, toolErrorDetails("FILE_EXISTS", "destination already exists; set overwrite=true to replace it", "validation", map[string]any{"path": destinationPath})
		}
		content := stage.OldContent
		stage.NewContent = nil
		destination.NewContent, destination.MoveFrom = &content, path
		destination.NewMode, destination.NewUID, destination.NewGID = &stage.Mode, &stage.OwnerUID, &stage.OwnerGID
		staged[destinationPath] = destination
	}
	result["action"] = action
	// The same snapshots feed logical statistics and the helper's SHA/mode/owner
	// preconditions. No second read can accidentally count another writer's edit.
	if err := describeWSLStages(result, staged, boundedInt(intValue(request.MaxDiffBytes, 65536), 65536, 1, maxTextOutputBytes)); err != nil {
		return nil, unchangedEditError(err)
	}
	if request.DryRun || result["changed"] == false {
		return addFileRuntimeResult(result, selection), nil
	}
	err = svc.commitWSLStages(ctx, selection, pathpkg.Dir(path), staged, result)
	return addFileRuntimeResult(result, selection), err
}

type wslPatchStage struct {
	OldContent    string
	NewContent    *string
	Mode          int
	OwnerUID      int
	OwnerGID      int
	PreserveOwner bool
	Existed       bool
	MoveFrom      string
	NewMode       *int
	NewUID        *int
	NewGID        *int
}

func resolveWSLPatchPath(basePath, rawPath string) (string, error) {
	clean := pathpkg.Clean(strings.TrimSpace(rawPath))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") {
		return "", toolErrorDetails("PATCH_FAILED", "WSL patch paths must stay relative to workdir", "validation", map[string]any{"path": rawPath})
	}
	resolved := pathpkg.Join(basePath, clean)
	if resolved != basePath && !strings.HasPrefix(resolved, strings.TrimSuffix(basePath, "/")+"/") {
		return "", toolErrorDetails("PATCH_FAILED", "WSL patch path escaped workdir", "validation", map[string]any{"path": rawPath})
	}
	return resolved, nil
}

func (svc *Service) loadWSLPatchStage(ctx context.Context, selection fileRuntimeSelection, path string, allowMissing bool) (*wslPatchStage, error) {
	loaded, err := svc.callWSLFileHelper(ctx, selection, map[string]any{"action": "read", "path": path, "reject_symlink": true, "allow_missing": allowMissing})
	if err != nil {
		return nil, err
	}
	exists, _ := loaded["exists"].(bool)
	content, _ := loaded["content"].(string)
	mode := resultInt(loaded, "mode")
	if loaded["mode"] == nil {
		mode = 0o644
	}
	copyContent := content
	return &wslPatchStage{OldContent: content, NewContent: &copyContent, Mode: mode, OwnerUID: resultInt(loaded, "uid"), OwnerGID: resultInt(loaded, "gid"), PreserveOwner: exists, Existed: exists}, nil
}

func describeWSLStages(result Result, staged map[string]*wslPatchStage, maxDiffBytes int) error {
	generic := map[string]stagedPatchFile{}
	for path, stage := range staged {
		entry := stagedPatchFile{Abs: path, Display: path, Original: []byte(stage.OldContent), OriginalExists: stage.Existed, Content: stage.NewContent, Mode: os.FileMode(stage.Mode), MoveFrom: stage.MoveFrom}
		if stage.NewMode != nil {
			mode := os.FileMode(*stage.NewMode)
			entry.NewMode = &mode
		}
		generic[path] = entry
	}
	fileStatistics := []editFileStatistics{}
	preview, truncated, stats, err := stagedDiffPreview(generic, maxDiffBytes, &fileStatistics)
	if err != nil {
		return err
	}
	result["diff_preview"], result["truncated"], result["file_statistics"] = preview, truncated, fileStatistics
	result["files_changed"], result["insertions"], result["deletions"], result["changed"] = stats.FilesChanged, stats.Insertions, stats.Deletions, stats.FilesChanged > 0
	return nil
}

func wslTransactionChanges(staged map[string]*wslPatchStage) []map[string]any {
	paths := make([]string, 0, len(staged))
	for path := range staged {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	changes := make([]map[string]any, 0, len(paths))
	for _, path := range paths {
		stage := staged[path]
		change := map[string]any{"path": path, "expected_exists": stage.Existed, "new_exists": stage.NewContent != nil}
		if stage.Existed {
			sum := sha256.Sum256([]byte(stage.OldContent))
			change["expected_sha256"], change["expected_mode"], change["expected_uid"], change["expected_gid"] = fmt.Sprintf("%x", sum), stage.Mode, stage.OwnerUID, stage.OwnerGID
		}
		if stage.NewContent != nil {
			sum := sha256.Sum256([]byte(*stage.NewContent))
			mode := stage.Mode
			if stage.NewMode != nil {
				mode = *stage.NewMode
			}
			change["content"], change["sha256"], change["mode"] = *stage.NewContent, fmt.Sprintf("%x", sum), mode
			if stage.NewUID != nil && stage.NewGID != nil {
				change["owner_uid"], change["owner_gid"] = *stage.NewUID, *stage.NewGID
			} else if stage.PreserveOwner {
				change["owner_uid"], change["owner_gid"] = stage.OwnerUID, stage.OwnerGID
			}
		}
		changes = append(changes, change)
	}
	return changes
}

func (svc *Service) commitWSLStages(ctx context.Context, selection fileRuntimeSelection, workdir string, staged map[string]*wslPatchStage, result Result) error {
	response, err := svc.callWSLFileHelper(ctx, selection, map[string]any{"action": "patch_transaction", "workdir": workdir, "changes": wslTransactionChanges(staged)})
	if err != nil {
		var failure *ToolError
		if errors.As(err, &failure) {
			if failure.Details["edit_outcome"] == "unchanged" {
				return unchangedEditError(err)
			}
			if residuals, ok := failure.Details["residual_outcomes"].(map[string]any); ok {
				confirmed := map[string]stagedPatchFile{}
				unknown := []string{}
				for path, stage := range staged {
					entry := stagedPatchFile{Abs: path, Display: path, Original: []byte(stage.OldContent), OriginalExists: stage.Existed, Content: stage.NewContent, Mode: os.FileMode(stage.Mode), MoveFrom: stage.MoveFrom}
					switch residuals[path] {
					case "unchanged":
						if stage.Existed {
							content := stage.OldContent
							entry.Content = &content
						} else {
							entry.Content = nil
						}
						entry.MoveFrom = ""
					case "committed":
						if stage.NewMode != nil {
							mode := os.FileMode(*stage.NewMode)
							entry.NewMode = &mode
						}
					case "deleted":
						entry.Content, entry.MoveFrom = nil, ""
					default:
						unknown = append(unknown, path)
						continue
					}
					confirmed[path] = entry
				}
				for path, entry := range confirmed {
					if entry.MoveFrom != "" {
						source, exists := confirmed[entry.MoveFrom]
						if !exists || source.Content != nil {
							entry.MoveFrom = ""
							confirmed[path] = entry
						}
					}
				}
				files := []editFileStatistics{}
				_, _, totals, statsErr := stagedDiffPreview(confirmed, 1, &files)
				if statsErr == nil && totals.FilesChanged > 0 {
					return &commitOutcomeError{cause: err, partial: &partialEditStatistics{totals: totals, files: files, unknown: unknown}}
				}
			}
		}
		// A killed helper or an unverified rollback is never a known zero.
		return &commitOutcomeError{cause: err}
	}
	if response["cleanup_pending"] == true {
		result["cleanup_pending"], result["transaction_id"] = true, response["transaction_id"]
	}
	return nil
}

func (svc *Service) fileEditPatchWSL(ctx context.Context, request EditRequest, selection fileRuntimeSelection) (Result, error) {
	patch := request.Patch
	if patch == "" {
		return nil, toolError("INVALID_ARGUMENT", "patch is required", "validation")
	}
	if !strings.HasPrefix(strings.TrimSpace(patch), "*** Begin Patch") {
		return nil, toolError("WSL_PATCH_FORMAT_REQUIRED", "runtime=wsl file_edit patch requires the structured *** Begin Patch envelope so every target can be safety-checked", "validation")
	}
	workdir, err := resolveWSLFilePath(request.Workdir)
	if err != nil {
		return nil, err
	}
	operations, err := parseEnvelopePatch(patch)
	if err != nil {
		return nil, err
	}
	if err := validateWSLPatchOperations(operations); err != nil {
		return nil, err
	}
	staged := map[string]*wslPatchStage{}
	affected := make([]map[string]any, 0, len(operations))
	summaries := make([]string, 0, len(operations))
	for _, operation := range operations {
		if err := ctx.Err(); err != nil {
			return nil, unchangedEditError(err)
		}
		sourcePath, err := resolveWSLPatchPath(workdir, operation.Path)
		if err != nil {
			return nil, err
		}
		switch operation.Kind {
		case "add", "delete":
			if _, exists := staged[sourcePath]; exists {
				return nil, toolErrorDetails("PATCH_FAILED", "patch contains conflicting operations for the same path", "validation", map[string]any{"path": sourcePath})
			}
			stage, err := svc.loadWSLPatchStage(ctx, selection, sourcePath, operation.Kind == "add")
			if err != nil {
				return nil, unchangedEditError(err)
			}
			if operation.Kind == "add" {
				if stage.Existed {
					return nil, toolErrorDetails("PATCH_FAILED", "cannot add file that already exists", "validation", map[string]any{"path": sourcePath})
				}
				content := operation.AddContent
				stage.NewContent = &content
				summaries = append(summaries, "A "+sourcePath)
			} else {
				stage.NewContent = nil
				summaries = append(summaries, "D "+sourcePath)
			}
			staged[sourcePath] = stage
			affected = append(affected, map[string]any{"path": sourcePath, "operation": operation.Kind})
		case "update":
			stage := staged[sourcePath]
			if stage == nil {
				stage, err = svc.loadWSLPatchStage(ctx, selection, sourcePath, false)
				if err != nil {
					return nil, unchangedEditError(err)
				}
			} else if !stage.Existed {
				return nil, toolErrorDetails("PATCH_FAILED", "cannot update a file added earlier in the same patch", "validation", map[string]any{"path": sourcePath})
			}
			if stage.NewContent == nil {
				return nil, toolErrorDetails("PATCH_FAILED", "cannot update a deleted file", "validation", map[string]any{"path": sourcePath})
			}
			updated, err := applyUpdateHunks(*stage.NewContent, operation.Chunks, sourcePath)
			if err != nil {
				return nil, err
			}
			destinationPath := sourcePath
			if operation.MoveTo != "" {
				destinationPath, err = resolveWSLPatchPath(workdir, operation.MoveTo)
				if err != nil {
					return nil, err
				}
			}
			if destinationPath == sourcePath {
				stage.NewContent = &updated
				staged[sourcePath] = stage
				affected = append(affected, map[string]any{"path": sourcePath, "operation": "update"})
				summaries = append(summaries, "M "+sourcePath)
				continue
			}
			if _, exists := staged[destinationPath]; exists {
				return nil, toolErrorDetails("PATCH_FAILED", "patch contains conflicting operations for the same path", "validation", map[string]any{"path": destinationPath})
			}
			destination, err := svc.loadWSLPatchStage(ctx, selection, destinationPath, true)
			if err != nil {
				return nil, unchangedEditError(err)
			}
			if destination.Existed {
				return nil, toolErrorDetails("PATCH_FAILED", "cannot move over an existing file", "validation", map[string]any{"path": destinationPath})
			}
			stage.NewContent = nil
			staged[sourcePath] = stage
			destination.NewContent, destination.MoveFrom = &updated, sourcePath
			destination.NewMode, destination.NewUID, destination.NewGID = &stage.Mode, &stage.OwnerUID, &stage.OwnerGID
			staged[destinationPath] = destination
			affected = append(affected, map[string]any{"path": sourcePath, "operation": "move", "move_to": destinationPath})
			summaries = append(summaries, "R "+sourcePath+" -> "+destinationPath)
		}
	}
	if len(staged) == 0 {
		return nil, toolError("PATCH_FAILED", "no files were modified", "validation")
	}
	result := Result{"action": "patch", "dry_run": request.DryRun, "workdir": workdir, "affected_files": affected, "summary": strings.Join(summaries, "\n")}
	if err := describeWSLStages(result, staged, boundedInt(intValue(request.MaxDiffBytes, 65536), 65536, 1, maxTextOutputBytes)); err != nil {
		return nil, unchangedEditError(err)
	}
	if !request.DryRun && result["changed"] != false {
		err = svc.commitWSLStages(ctx, selection, workdir, staged, result)
	}
	return addFileRuntimeResult(result, selection), err
}
