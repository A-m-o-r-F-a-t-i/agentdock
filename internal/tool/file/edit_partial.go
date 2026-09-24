package file

import (
	"errors"
	"os"
)

type partialEditStatistics struct {
	totals  diffStats
	files   []editFileStatistics
	unknown []string
}

// Only actual residuals whose ownership can still be established participate in
// partial totals. Foreign replacements and unreadable paths remain explicit.
func residualPatchStatistics(prepared []preparedPatchFile) *partialEditStatistics {
	confirmed := map[string]stagedPatchFile{}
	unknown := []string{}
	for _, item := range prepared {
		file := item.file
		if verifyPatchOriginal(file) == nil {
			if file.OriginalExists {
				content := string(file.Original)
				file.Content = &content
			} else {
				file.Content = nil
			}
			file.MoveFrom, file.NewMode = "", nil
			confirmed[file.Abs] = file
			continue
		}
		if file.Binary {
			unknown = append(unknown, file.Display)
			continue
		}
		if item.installed && inspectInstalledPatchFile(item) == installedPatchUnchanged {
			confirmed[file.Abs] = file
			continue
		}
		_, err := os.Lstat(file.Abs)
		if errors.Is(err, os.ErrNotExist) && item.backupPath != "" && verifyPatchBackup(item) == nil && (!item.installed || item.removedDuringRollback) {
			file.Content, file.MoveFrom = nil, ""
			confirmed[file.Abs] = file
			continue
		}
		unknown = append(unknown, file.Display)
	}
	// A destination whose source was restored is a copy left behind, not a
	// completed move. Likewise, an unknown source cannot justify rename zeros.
	for path, file := range confirmed {
		if file.MoveFrom != "" {
			origin, exists := confirmed[file.MoveFrom]
			if !exists || origin.Content != nil {
				file.MoveFrom = ""
				confirmed[path] = file
			}
		}
	}
	files := []editFileStatistics{}
	_, _, totals, err := stagedDiffPreview(confirmed, 1, &files)
	if err != nil || totals.FilesChanged == 0 {
		return nil
	}
	return &partialEditStatistics{totals: totals, files: files, unknown: unknown}
}
