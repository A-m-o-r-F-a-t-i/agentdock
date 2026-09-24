package file

import (
	"fmt"
	"sort"
	"strings"
)

type editFileStatistics struct {
	Path       string `json:"path"`
	Operation  string `json:"operation"`
	MoveTo     string `json:"move_to,omitempty"`
	Insertions int    `json:"insertions"`
	Deletions  int    `json:"deletions"`
}

func stagedDiffPreview(staged map[string]stagedPatchFile, maxBytes int, collectors ...*[]editFileStatistics) (string, bool, diffStats, error) {
	paths := make([]string, 0, len(staged))
	for path := range staged {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	var builder strings.Builder
	total := diffStats{}
	movedSources := map[string]bool{}
	for _, file := range staged {
		if file.MoveFrom != "" {
			movedSources[file.MoveFrom] = true
		}
	}
	for _, path := range paths {
		file := staged[path]
		if movedSources[path] {
			continue
		}
		oldContent := string(file.Original)
		originPath := path
		if file.MoveFrom != "" {
			source, seen := file.MoveFrom, map[string]bool{path: true}
			for {
				if seen[source] {
					return "", false, diffStats{}, fmt.Errorf("cyclic move in file statistics")
				}
				seen[source] = true
				origin, exists := staged[source]
				if !exists {
					return "", false, diffStats{}, fmt.Errorf("missing move origin in file statistics")
				}
				oldContent = string(origin.Original)
				originPath = source
				if origin.MoveFrom == "" {
					break
				}
				source = origin.MoveFrom
			}
		}
		newContent := ""
		if file.Content != nil {
			newContent = *file.Content
		}
		diff, _, stats, err := unifiedDiffPreview(file.Display, oldContent, newContent, 0)
		if err != nil {
			return "", false, diffStats{}, err
		}
		if file.MoveFrom != "" || file.OriginalExists != (file.Content != nil) || file.Content != nil && file.Mode.Perm() != patchTargetMode(file) {
			stats.FilesChanged = 1
		}
		operation := "update"
		if !file.OriginalExists {
			operation = "add"
		} else if file.Content == nil {
			operation = "delete"
		}
		detail := editFileStatistics{Path: file.Display, Operation: operation, Insertions: stats.Insertions, Deletions: stats.Deletions}
		if file.MoveFrom != "" {
			detail.Path, detail.MoveTo, detail.Operation = staged[originPath].Display, file.Display, "move"
		}
		for _, collector := range collectors {
			*collector = append(*collector, detail)
		}
		if file.MoveFrom != "" && file.OriginalExists {
			// The source is relocated, and the overwritten destination is
			// removed independently of any change to the source's own content.
			removedLines := logicalLineCount(string(file.Original))
			stats.Deletions += removedLines
			for _, collector := range collectors {
				*collector = append(*collector, editFileStatistics{Path: file.Display, Operation: "overwrite", Deletions: removedLines})
			}
			removed, _, _, err := unifiedDiffPreview(file.Display+" (overwritten destination)", string(file.Original), "", 0)
			if err != nil {
				return "", false, diffStats{}, err
			}
			builder.WriteString(removed)
			total.FilesChanged++
		}
		builder.WriteString(diff)
		if diff != "" && !strings.HasSuffix(diff, "\n") {
			builder.WriteString("\n")
		}
		if stats.FilesChanged > 0 {
			total.FilesChanged++
		}
		total.Insertions += stats.Insertions
		total.Deletions += stats.Deletions
	}
	text := builder.String()
	truncated := truncateString(text, maxBytes)
	return truncated, maxBytes > 0 && len([]byte(text)) > maxBytes, total, nil
}

func patchNearbyContext(lines, oldLines []string) []map[string]any {
	if len(lines) == 0 {
		return nil
	}
	needle := ""
	for _, line := range oldLines {
		if strings.TrimSpace(line) != "" {
			needle = strings.TrimSpace(line)
			break
		}
	}
	if needle == "" {
		return []map[string]any{{"line": 1, "context_start_line": 1, "context": firstLines(lines, 20)}}
	}
	for i, line := range lines {
		if strings.Contains(line, needle) {
			return []map[string]any{lineContext(lines, i)}
		}
	}
	return []map[string]any{{"line": 1, "context_start_line": 1, "context": firstLines(lines, 20)}}
}

func patchContextsForMatches(lines []string, indexes []int) []map[string]any {
	out := make([]map[string]any, 0)
	for _, idx := range indexes {
		out = append(out, lineContext(lines, idx))
		if len(out) >= 5 {
			break
		}
	}
	return out
}

func lineContext(lines []string, idx int) map[string]any {
	start := idx - 10
	if start < 0 {
		start = 0
	}
	end := idx + 11
	if end > len(lines) {
		end = len(lines)
	}
	return map[string]any{"line": idx + 1, "context_start_line": start + 1, "context": append([]string(nil), lines[start:end]...)}
}

func firstLines(lines []string, limit int) []string {
	if len(lines) > limit {
		lines = lines[:limit]
	}
	return append([]string(nil), lines...)
}
