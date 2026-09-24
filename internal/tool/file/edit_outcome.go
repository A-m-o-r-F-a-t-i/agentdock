package file

import (
	"errors"
	"os"
	"runtime"
	"unicode/utf8"
)

func validStatsText(content []byte) bool { return utf8.Valid(content) && !looksBinary(content) }

func patchModeMatches(actual, expected os.FileMode) bool {
	if runtime.GOOS == "windows" {
		// Windows chmod represents the read-only attribute, not Unix group or
		// executable bits. Compare the actual supported permission here.
		return actual.Perm()&0o200 == expected.Perm()&0o200
	}
	return actual.Perm() == expected.Perm()
}

func patchTargetMode(file stagedPatchFile) os.FileMode {
	if file.NewMode != nil {
		return file.NewMode.Perm()
	}
	return file.Mode.Perm()
}

func readCommitOriginal(path string, binary bool) (os.FileInfo, []byte, error) {
	if !binary {
		return readPatchFile(path)
	}
	read, err := readBoundedFile(path, int64(maxTextFileReadBytes))
	if err != nil {
		return nil, nil, err
	}
	if read.TooLarge || !read.Info.Mode().IsRegular() {
		return nil, nil, toolError("PATCH_CONFLICT", "patch original is no longer a bounded regular file", "runtime")
	}
	return read.Info, read.Data, nil
}

// commitOutcomeError preserves the original error identity while distinguishing
// a fully restored transaction from an outcome that must be inspected.
type commitOutcomeError struct {
	cause     error
	unchanged bool
	partial   *partialEditStatistics
}

func (e *commitOutcomeError) Error() string { return e.cause.Error() }
func (e *commitOutcomeError) Unwrap() error { return e.cause }

func unchangedEditError(err error) error {
	if err == nil {
		return nil
	}
	return &commitOutcomeError{cause: err, unchanged: true}
}

// Only producers that own the actual commit may assert a known result. Missing
// counts, a transport interruption, and unverified rollback remain nullable.
func finishEditStatistics(result Result, failure error, dryRun bool) Result {
	defer func() {
		if result == nil || result["file_statistics"] != nil {
			return
		}
		path, _ := result["path"].(string)
		added, okAdd := result["insertions"].(int)
		removed, okRemoved := result["deletions"].(int)
		if path != "" && okAdd && okRemoved {
			operation, _ := result["action"].(string)
			moveTo, _ := result["new_path"].(string)
			result["file_statistics"] = []editFileStatistics{{Path: path, Operation: operation, MoveTo: moveTo, Insertions: added, Deletions: removed}}
		}
	}()
	if result == nil {
		result = Result{}
	}
	if dryRun {
		result["stats_state"] = "preview"
		return result
	}
	if failure != nil {
		delete(result, "file_statistics")
		var outcome *commitOutcomeError
		var validation *ToolError
		unchanged := false
		if errors.As(failure, &outcome) {
			unchanged = outcome.unchanged
			if outcome.partial != nil {
				partial := outcome.partial
				result["stats_state"], result["changed"] = "partial", true
				result["insertions"], result["deletions"], result["files_changed"] = partial.totals.Insertions, partial.totals.Deletions, partial.totals.FilesChanged
				result["file_statistics"], result["unverified_files"] = partial.files, partial.unknown
				return result
			}
		} else {
			unchanged = errors.As(failure, &validation) && validation.Category == "validation"
		}
		if unchanged {
			result["stats_state"], result["changed"] = "known", false
			result["files_changed"], result["insertions"], result["deletions"] = 0, 0, 0
		} else {
			result["stats_state"] = "unknown"
			delete(result, "insertions")
			delete(result, "deletions")
			delete(result, "changed")
		}
		return result
	}
	_, added := result["insertions"]
	_, removed := result["deletions"]
	if added && removed {
		result["stats_state"] = "known"
	} else if result["stats_state"] == nil {
		result["stats_state"] = "unsupported"
	}
	return result
}
