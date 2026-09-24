package file

import (
	"bytes"
	"fmt"
	"strings"

	anchoreddiff "github.com/rogpeppe/go-internal/diff"
	"github.com/uvwt/agentdock/internal/textutil"
)

const maxDiffOutputBytes = 64 << 20

type diffStats struct {
	FilesChanged int
	Insertions   int
	Deletions    int
}

func unifiedDiffPreview(path, oldContent, newContent string, maxBytes int) (string, bool, diffStats, error) {
	output := anchoreddiff.Diff("a/"+path, []byte(oldContent), "b/"+path, []byte(newContent))
	if len(output) > 0 {
		// 该实现会额外输出 diff 命令头；工具契约只返回 unified diff。
		if newline := bytes.IndexByte(output, '\n'); newline >= 0 {
			output = output[newline+1:]
		}
	}
	if len(output) > maxDiffOutputBytes {
		return "", false, diffStats{}, fmt.Errorf("diff output exceeds %d bytes (observed %d bytes)", maxDiffOutputBytes, len(output))
	}
	stats := countDiffStats(string(output))
	logicalOld, logicalNew := logicalDiffContent(oldContent), logicalDiffContent(newContent)
	if logicalOld != oldContent || logicalNew != newContent {
		stats = countDiffStats(string(anchoreddiff.Diff("a/"+path, []byte(logicalOld), "b/"+path, []byte(logicalNew))))
	}
	if oldContent != newContent {
		stats.FilesChanged = 1
	}
	truncated := textutil.SafeTruncateBytes(output, maxBytes)
	return truncated.Text, truncated.Truncated, stats, nil
}

func countDiffStats(diffText string) diffStats {
	stats := diffStats{}
	inHunk, changed := false, false
	for _, line := range strings.Split(diffText, "\n") {
		switch {
		case strings.HasPrefix(line, "diff "):
			if changed {
				stats.FilesChanged++
			}
			inHunk, changed = false, false
		case strings.HasPrefix(line, "@@ "):
			inHunk = true
		case inHunk && strings.HasPrefix(line, "+"):
			stats.Insertions++
			changed = true
		case inHunk && strings.HasPrefix(line, "-"):
			stats.Deletions++
			changed = true
		}
	}
	if changed {
		stats.FilesChanged++
	}
	return stats
}

// The preview preserves exact bytes. Statistics compare logical text lines:
// CRLF and LF are equivalent, and a terminator does not add an empty line.
func logicalDiffContent(content string) string {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	return content
}

func logicalLineCount(content string) int {
	return strings.Count(logicalDiffContent(content), "\n")
}
