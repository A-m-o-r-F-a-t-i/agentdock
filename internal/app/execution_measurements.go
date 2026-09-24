package app

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/uvwt/agentdock/internal/activity"
)

func (r *Runtime) recordRPCReturn(binding activity.Binding, tool string, received time.Time, result Result, failure error) error {
	return r.recordRPCReturnStatus(binding, tool, received, rpcReturnStatus(result, failure))
}

func rpcReturnStatus(result Result, failure error) string {
	status := "succeeded"
	if failure != nil || resultReportsFailure(result) || result["command_ok"] == false {
		status = "failed"
	}
	if errors.Is(failure, context.Canceled) {
		status = "cancelled"
	}
	if stringArg(result, "status") == "pending_approval" {
		status = "pending_approval"
	}
	return status
}

func (r *Runtime) recordRPCReturnStatus(binding activity.Binding, tool string, received time.Time, status string) error {
	completed := time.Now()
	elapsed := completed.Sub(received).Milliseconds()
	stamp := completed.UTC()
	event := activity.Event{Binding: binding, Kind: "call.rpc_returned", ToolName: tool}
	event.RPCCompletedAt, event.RPCElapsedMS, event.RPCStatus = &stamp, &elapsed, status
	return r.appendExecution(event)
}

func measuredInt(result Result, key string) *int {
	switch value := result[key].(type) {
	case int:
		return &value
	case int64:
		converted := int(value)
		return &converted
	case float64:
		converted := int(value)
		if float64(converted) == value {
			return &converted
		}
	}
	return nil
}

func (r *Runtime) fileEditDetails(args map[string]any, result Result, state executionObservation, failure error) *activity.FileEditDetails {
	details := &activity.FileEditDetails{Action: stringArg(args, "action"), Path: stringArg(args, "path"), NewPath: stringArg(args, "new_path"), DryRun: args["dry_run"] == true, Recursive: args["recursive"] == true, Executed: state.executed}
	if !state.executed || details.DryRun {
		changed := false
		details.Changed = &changed
	} else if changed, found := result["changed"].(bool); found {
		details.Changed = &changed
	} else if failure == nil {
		if count := measuredInt(result, "files_changed"); count != nil {
			changed := *count > 0
			details.Changed = &changed
		}
	}
	details.Insertions, details.Deletions = measuredInt(result, "insertions"), measuredInt(result, "deletions")
	details.StatsState = stringArg(result, "stats_state")
	if details.DryRun {
		details.StatsState = "preview"
		details.ProposedInsertions, details.ProposedDeletions = details.Insertions, details.Deletions
		details.Insertions, details.Deletions = nil, nil
	} else if !state.executed && failure != nil {
		zero := 0
		details.StatsState = "known"
		details.Insertions, details.Deletions = &zero, &zero
	} else if details.StatsState == "" {
		details.StatsState = "unknown"
		if details.Insertions != nil && details.Deletions != nil && failure == nil {
			details.StatsState = "known"
		}
	}
	if details.StatsState != "known" && details.StatsState != "partial" {
		details.Insertions, details.Deletions = nil, nil
	}
	details.DiffPreview = r.executionRedactor(args).Text(stringArg(result, "diff_preview"), 2048)
	details.DiffTruncated = result["truncated"] == true || len(stringArg(result, "diff_preview")) > 2048
	rawFiles := result["file_statistics"]
	if rawFiles == nil {
		rawFiles = result["affected_files"]
	}
	value := reflect.ValueOf(rawFiles)
	if value.IsValid() && value.Kind() == reflect.Slice {
		count := value.Len()
		details.AffectedCount = &count
		details.FilesTruncated = count > activity.MaxRecordedAffectedFiles
		for index := 0; index < min(count, activity.MaxRecordedAffectedFiles); index++ {
			entry := value.Index(index).Interface()
			if name, ok := entry.(string); ok {
				details.AffectedFiles = append(details.AffectedFiles, activity.AffectedFile{Path: name})
				continue
			}
			data, err := json.Marshal(entry)
			if err != nil {
				details.FilesTruncated = true
				continue
			}
			var file activity.AffectedFile
			if json.Unmarshal(data, &file) != nil {
				details.FilesTruncated = true
				continue
			}
			details.AffectedFiles = append(details.AffectedFiles, file)
		}
	} else if state.executed && failure == nil && details.Path != "" {
		count := 1
		details.AffectedCount = &count
		details.AffectedFiles = []activity.AffectedFile{{Path: details.Path, Operation: details.Action, MoveTo: details.NewPath}}
	}
	relative := func(value string) string {
		if state.selected != nil && value != "" && state.selected.Runtime != "wsl" {
			if rel, err := filepath.Rel(state.selected.Root, value); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				value = filepath.ToSlash(rel)
			}
		}
		return r.executionRedactor(args).Text(value, 512)
	}
	details.Path, details.NewPath = relative(details.Path), relative(details.NewPath)
	for index := range details.AffectedFiles {
		file := &details.AffectedFiles[index]
		file.Path, file.MoveTo = relative(file.Path), relative(file.MoveTo)
		file.StatsState = details.StatsState
		if details.DryRun {
			file.ProposedInsertions, file.ProposedDeletions = file.Insertions, file.Deletions
			file.Insertions, file.Deletions = nil, nil
		}
		if details.StatsState != "known" && details.StatsState != "partial" {
			file.Insertions, file.Deletions = nil, nil
		}
	}
	return details
}
