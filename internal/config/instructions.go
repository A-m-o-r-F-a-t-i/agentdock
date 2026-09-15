package config

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// ReadInstructionsFile validates explicit instructions identically for runtime
// startup and desktop configuration, without modifying storage or environment.
func ReadInstructionsFile(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("InstructionsFile must resolve to an absolute path: %s", path)
	}

	// 先检查文件类型再打开，避免误配设备或命名管道时在 Open 阶段阻塞。
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("stat InstructionsFile %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("InstructionsFile must be a regular file: %s", path)
	}
	if info.Size() > maxInstructionsFileBytes {
		return "", fmt.Errorf("InstructionsFile %s exceeds %d bytes", path, maxInstructionsFileBytes)
	}

	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open InstructionsFile %s: %w", path, err)
	}
	defer file.Close()

	// Stat 只能约束检查瞬间的文件大小；读取仍限制为 max+1，避免文件并发增长时突破边界。
	data, err := io.ReadAll(io.LimitReader(file, int64(maxInstructionsFileBytes)+1))
	if err != nil {
		return "", fmt.Errorf("read InstructionsFile %s: %w", path, err)
	}
	if len(data) > maxInstructionsFileBytes {
		return "", fmt.Errorf("InstructionsFile %s exceeds %d bytes", path, maxInstructionsFileBytes)
	}
	if !utf8.Valid(data) {
		return "", fmt.Errorf("InstructionsFile must contain valid UTF-8: %s", path)
	}
	instructions := strings.TrimSpace(string(data))
	if instructions == "" {
		return "", fmt.Errorf("InstructionsFile must contain non-empty instructions: %s", path)
	}
	return instructions, nil
}
