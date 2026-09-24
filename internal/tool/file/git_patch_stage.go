package file

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	processcontrol "github.com/uvwt/agentdock/internal/process"
	"github.com/uvwt/agentdock/internal/textutil"
	workspacepkg "github.com/uvwt/agentdock/internal/workspace"
)

const maxGitPatchStagedBytes = 64 << 20

// Git evaluates the supplied patch in an isolated mirror. Only the paths it
// reports are copied, then the resulting bytes use the same protected commit
// as structured patches. No statistics are inferred from the requested hunks.
func (svc *Service) applyGitPatch(ctx context.Context, request EditRequest, workdir workspacepkg.Path) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	metadata, err := gitPatchCommand(ctx, workdir.Abs, request.Patch, "--numstat", "-z")
	if err != nil {
		return nil, unchangedEditError(err)
	}
	targets, moves, err := gitPatchTargets(string(metadata), request.Patch)
	if err != nil {
		return nil, unchangedEditError(err)
	}
	mirror, err := os.MkdirTemp("", "agentdock-patch-evaluate-*")
	if err != nil {
		return nil, unchangedEditError(err)
	}
	defer os.RemoveAll(mirror)
	staged := map[string]stagedPatchFile{}
	byRelative := map[string]string{}
	var total int64
	for _, relative := range targets {
		if err := ctx.Err(); err != nil {
			return nil, unchangedEditError(err)
		}
		path, err := patchPathInBase(workdir.Display, relative)
		if err != nil {
			return nil, unchangedEditError(err)
		}
		resolved, err := svc.ws.ResolveForWrite(path)
		if err != nil {
			return nil, unchangedEditError(err)
		}
		copyPath := filepath.Join(mirror, filepath.FromSlash(relative))
		if !pathIsDescendant(mirror, copyPath) {
			return nil, unchangedEditError(toolError("PATCH_FAILED", "patch path escapes its working directory", "validation"))
		}
		entry := stagedPatchFile{Abs: resolved.Abs, Display: resolved.Display, Mode: 0o644, OriginalExists: resolved.Exists, Binary: true}
		if resolved.Exists {
			read, err := readBoundedFile(resolved.Abs, int64(maxTextFileReadBytes))
			if err != nil {
				return nil, unchangedEditError(err)
			}
			if !read.Info.Mode().IsRegular() || read.TooLarge {
				return nil, unchangedEditError(toolError("PATCH_FAILED", "patch targets must be bounded regular files", "validation"))
			}
			total += int64(len(read.Data))
			if total > maxGitPatchStagedBytes {
				return nil, unchangedEditError(toolError("PATCH_FAILED", "patch snapshot exceeds the 64 MiB transaction budget", "validation"))
			}
			entry.Original, entry.Mode = read.Data, read.Info.Mode().Perm()
			if err := os.MkdirAll(filepath.Dir(copyPath), 0o700); err != nil {
				return nil, unchangedEditError(err)
			}
			if err := os.WriteFile(copyPath, read.Data, entry.Mode); err != nil {
				return nil, unchangedEditError(err)
			}
		}
		byRelative[relative] = resolved.Abs
		staged[resolved.Abs] = entry
	}
	if _, err := gitPatchCommand(ctx, mirror, request.Patch); err != nil {
		return nil, unchangedEditError(err)
	}
	// Reject unexpected files and symlinks before touching any real target.
	if err := filepath.WalkDir(mirror, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(mirror, path)
		if err != nil {
			return err
		}
		if _, exists := byRelative[filepath.ToSlash(relative)]; !exists || !entry.Type().IsRegular() {
			return toolError("PATCH_FAILED", "patch produced an undeclared path or a non-regular file", "validation")
		}
		return nil
	}); err != nil {
		return nil, unchangedEditError(err)
	}
	allText := true
	affected := make([]gitDiffFile, 0, len(targets))
	total = 0
	for _, relative := range targets {
		if err := ctx.Err(); err != nil {
			return nil, unchangedEditError(err)
		}
		abs := byRelative[relative]
		entry := staged[abs]
		read, readErr := readBoundedFile(filepath.Join(mirror, filepath.FromSlash(relative)), int64(maxTextFileReadBytes))
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return nil, unchangedEditError(readErr)
		}
		if readErr == nil {
			if read.TooLarge || !read.Info.Mode().IsRegular() {
				return nil, unchangedEditError(toolError("PATCH_FAILED", "patched file exceeds the text transaction limit", "validation"))
			}
			total += int64(len(read.Data))
			if total > maxGitPatchStagedBytes {
				return nil, unchangedEditError(toolError("PATCH_FAILED", "patched data exceeds the 64 MiB transaction budget", "validation"))
			}
			content := string(read.Data)
			entry.Content = &content
			mode := read.Info.Mode().Perm()
			entry.NewMode = &mode
		}
		if source := moves[relative]; source != "" {
			entry.MoveFrom = byRelative[source]
		}
		entry.Binary = !validStatsText(entry.Original) || entry.Content != nil && !validStatsText([]byte(*entry.Content))
		allText = allText && !entry.Binary
		staged[abs] = entry
		changed := entry.OriginalExists != (entry.Content != nil) || entry.Content != nil && (!bytes.Equal(entry.Original, []byte(*entry.Content)) || entry.NewMode != nil && entry.Mode != *entry.NewMode)
		if !changed && entry.MoveFrom == "" {
			continue
		}
		status := "modified"
		if !entry.OriginalExists {
			status = "added"
		} else if entry.Content == nil {
			status = "deleted"
		}
		affected = append(affected, gitDiffFile{Path: relative, Status: status, Binary: entry.Binary})
	}
	preview := textutil.SafeTruncateString(request.Patch, boundedInt(intValue(request.MaxDiffBytes, 65536), 65536, 1, maxTextOutputBytes))
	result := Result{"dry_run": request.DryRun, "workdir": workdir.Display, "affected_files": affected, "diff_preview": preview.Text, "truncated": preview.Truncated, "files_changed": len(affected), "summary": "patch applied"}
	if request.DryRun {
		result["summary"] = "patch validated"
	}
	if allText {
		fileStatistics := []editFileStatistics{}
		_, _, stats, err := stagedDiffPreview(staged, 1, &fileStatistics)
		if err != nil {
			return nil, unchangedEditError(err)
		}
		result["files_changed"], result["insertions"], result["deletions"] = stats.FilesChanged, stats.Insertions, stats.Deletions
		result["file_statistics"] = fileStatistics
	}
	if request.DryRun {
		return result, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, unchangedEditError(err)
	}
	committed := map[string]stagedPatchFile{}
	for path, entry := range staged {
		changed := entry.OriginalExists != (entry.Content != nil) || entry.Content != nil && (!bytes.Equal(entry.Original, []byte(*entry.Content)) || entry.Mode.Perm() != patchTargetMode(entry))
		if changed {
			committed[path] = entry
		}
	}
	return result, commitStagedPatch(committed)
}

func gitPatchCommand(ctx context.Context, dir, patch string, options ...string) ([]byte, error) {
	args := append([]string{"-c", "core.autocrlf=false", "-c", "core.safecrlf=false", "apply", "--whitespace=nowarn"}, options...)
	cmd := exec.CommandContext(ctx, "git", append(args, "-")...)
	cmd.Dir, cmd.Stdin = dir, strings.NewReader(patch)
	// Evaluation must not inherit a caller's unrelated Git worktree/index.
	for _, variable := range os.Environ() {
		key, _, _ := strings.Cut(variable, "=")
		switch strings.ToUpper(key) {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR", "GIT_CEILING_DIRECTORIES":
			continue
		}
		cmd.Env = append(cmd.Env, variable)
	}
	cmd.Env = append(cmd.Env, "GIT_CEILING_DIRECTORIES="+filepath.Dir(dir))
	processcontrol.Configure(cmd)
	output, total, truncated, err := runBoundedCombinedOutput(cmd, 1<<20)
	if err != nil || truncated {
		if err == nil {
			err = fmt.Errorf("git apply metadata exceeded output limit")
		}
		return nil, toolErrorCause("PATCH_FAILED", "git apply failed", "runtime", map[string]any{"output": redactSecrets(string(output), nil), "output_total_bytes": total, "output_truncated": truncated}, errors.Join(err, ctx.Err()))
	}
	return output, nil
}

func gitPatchTargets(numstat, patch string) ([]string, map[string]string, error) {
	paths := map[string]bool{}
	moves := map[string]string{}
	for _, record := range strings.Split(numstat, "\x00") {
		if record == "" {
			continue
		}
		fields := strings.SplitN(record, "\t", 3)
		if len(fields) != 3 {
			return nil, nil, fmt.Errorf("invalid git apply numstat response")
		}
		paths[fields[2]] = true
	}
	var from string
	for _, line := range strings.Split(patch, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if strings.HasPrefix(line, "diff --git ") {
			from = ""
		}
		if strings.HasPrefix(line, "rename from ") || strings.HasPrefix(line, "copy from ") {
			raw := strings.TrimPrefix(strings.TrimPrefix(line, "rename from "), "copy from ")
			decoded, err := decodeGitPath(raw)
			if err != nil {
				return nil, nil, err
			}
			from = decoded
			paths[from] = true
		}
		if strings.HasPrefix(line, "rename to ") {
			to, err := decodeGitPath(strings.TrimPrefix(line, "rename to "))
			if err != nil {
				return nil, nil, err
			}
			if from == "" || !paths[to] {
				return nil, nil, fmt.Errorf("rename metadata does not match git apply targets")
			}
			moves[to] = from
		}
	}
	if len(paths) == 0 || len(paths) > 10000 {
		return nil, nil, fmt.Errorf("patch must have between 1 and 10000 file targets")
	}
	result := make([]string, 0, len(paths))
	for path := range paths {
		if strings.ContainsRune(path, 0) || filepath.IsAbs(path) || filepath.VolumeName(path) != "" || strings.HasPrefix(path, "/") || path == "." || path == ".." || strings.HasPrefix(filepath.ToSlash(filepath.Clean(path)), "../") {
			return nil, nil, fmt.Errorf("invalid relative patch path %q", path)
		}
		result = append(result, path)
	}
	sort.Strings(result)
	return result, moves, nil
}

func decodeGitPath(path string) (string, error) {
	if strings.HasPrefix(path, "\"") {
		return strconv.Unquote(path)
	}
	return path, nil
}
