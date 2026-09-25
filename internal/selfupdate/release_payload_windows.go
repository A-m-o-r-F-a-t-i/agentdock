//go:build windows

package selfupdate

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const (
	maxWindowsReleaseEntries          = 16384
	maxWindowsReleaseFileBytes  int64 = 256 << 20
	maxWindowsReleaseTotalBytes int64 = 1 << 30
)

type windowsReleasePayload struct {
	CorePath    string
	BundlePath  string
	DesktopPath string
}

type validatedZipEntry struct {
	file  *zip.File
	name  string
	isDir bool
}

// extractWindowsReleasePayload validates the complete ZIP catalogue once and
// streams only the runtime files required by a generation update. This avoids
// reopening and rescanning the same archive for Core, Skills and desktop files.
func extractWindowsReleasePayload(archiveData []byte, tempDir, executableName, targetVersion string) (windowsReleasePayload, error) {
	reader, err := zip.NewReader(bytes.NewReader(archiveData), int64(len(archiveData)))
	if err != nil {
		return windowsReleasePayload{}, err
	}
	entries, err := validateWindowsReleaseEntries(reader.File)
	if err != nil {
		return windowsReleasePayload{}, err
	}

	payload := windowsReleasePayload{
		CorePath:    filepath.Join(tempDir, executableName),
		BundlePath:  filepath.Join(tempDir, "core-skills"),
		DesktopPath: filepath.Join(tempDir, "windows-desktop"),
	}
	if err := os.MkdirAll(payload.BundlePath, 0o700); err != nil {
		return windowsReleasePayload{}, err
	}
	if err := os.MkdirAll(payload.DesktopPath, 0o700); err != nil {
		return windowsReleasePayload{}, err
	}

	foundCore := false
	foundManifest := false
	foundDesktop := make(map[string]bool, len(windowsDesktopArchiveFiles)+len(windowsGenerationArchiveFiles))
	var skillBytes int64
	for _, entry := range entries {
		if entry.isDir {
			continue
		}
		switch {
		case entry.name == executableName:
			if foundCore {
				return windowsReleasePayload{}, fmt.Errorf("Windows Release ZIP 包含重复文件 %s", executableName)
			}
			if entry.file.UncompressedSize64 == 0 || entry.file.UncompressedSize64 > uint64(maxExtractedPayloadBytes) {
				return windowsReleasePayload{}, fmt.Errorf("Windows Core 大小无效: %d", entry.file.UncompressedSize64)
			}
			if err := writeZipEntry(entry.file, payload.CorePath, 0o755, maxExtractedPayloadBytes, false); err != nil {
				return windowsReleasePayload{}, fmt.Errorf("解压 Windows Core 失败: %w", err)
			}
			foundCore = true

		case strings.HasPrefix(entry.name, coreSkillBundlePrefix):
			relative := strings.TrimPrefix(entry.name, coreSkillBundlePrefix)
			if relative == "" {
				continue
			}
			if entry.file.UncompressedSize64 > uint64(maxExtractedPayloadBytes) || int64(entry.file.UncompressedSize64) > maxExtractedPayloadBytes-skillBytes {
				return windowsReleasePayload{}, fmt.Errorf("Bundle 解压内容超过 %d 字节限制", maxExtractedPayloadBytes)
			}
			target, err := safePayloadTarget(payload.BundlePath, relative)
			if err != nil {
				return windowsReleasePayload{}, fmt.Errorf("Bundle 文件路径越界: %s", entry.name)
			}
			if err := writeZipEntry(entry.file, target, 0o600, maxExtractedPayloadBytes-skillBytes, true); err != nil {
				return windowsReleasePayload{}, fmt.Errorf("解压 Bundle 文件 %s 失败: %w", entry.name, err)
			}
			skillBytes += int64(entry.file.UncompressedSize64)
			if relative == "manifest.json" {
				foundManifest = true
			}

		default:
			mode, wanted := windowsDesktopArchiveFiles[entry.name]
			if !wanted {
				mode, wanted = windowsGenerationArchiveFiles[entry.name]
			}
			if !wanted {
				continue
			}
			if entry.file.UncompressedSize64 == 0 || entry.file.UncompressedSize64 > uint64(maxWindowsDesktopFileBytes) {
				return windowsReleasePayload{}, fmt.Errorf("Windows Release 文件 %s 大小无效", entry.name)
			}
			target, err := safePayloadTarget(payload.DesktopPath, entry.name)
			if err != nil {
				return windowsReleasePayload{}, fmt.Errorf("Windows Release 文件路径越界: %s", entry.name)
			}
			if err := writeZipEntry(entry.file, target, mode, maxWindowsDesktopFileBytes, false); err != nil {
				return windowsReleasePayload{}, fmt.Errorf("解压 Windows Release 文件 %s 失败: %w", entry.name, err)
			}
			foundDesktop[entry.name] = true
		}
	}
	if !foundCore {
		return windowsReleasePayload{}, fmt.Errorf("压缩包缺少 %s", executableName)
	}
	if !foundManifest {
		return windowsReleasePayload{}, errors.New("Release 压缩包缺少核心 Skill manifest")
	}
	for name := range windowsDesktopArchiveFiles {
		if !foundDesktop[name] {
			return windowsReleasePayload{}, fmt.Errorf("Windows Release ZIP 缺少 %s", name)
		}
	}
	version := normalizeVersion(targetVersion)
	if version == "" {
		return windowsReleasePayload{}, errors.New("Windows 桌面组件目标版本为空")
	}
	if err := os.WriteFile(filepath.Join(payload.DesktopPath, windowsDesktopVersionFile), []byte(version+"\n"), 0o644); err != nil {
		return windowsReleasePayload{}, fmt.Errorf("写入 Windows 桌面组件版本标记失败: %w", err)
	}
	return payload, nil
}

func validateWindowsReleaseEntries(files []*zip.File) ([]validatedZipEntry, error) {
	if len(files) == 0 || len(files) > maxWindowsReleaseEntries {
		return nil, fmt.Errorf("Windows Release ZIP 条目数量无效: %d", len(files))
	}
	entries := make([]validatedZipEntry, 0, len(files))
	kinds := make(map[string]bool, len(files))
	var total int64
	for _, file := range files {
		name, isDir, err := canonicalWindowsReleaseName(file.Name)
		if err != nil {
			return nil, err
		}
		key := strings.ToLower(strings.TrimSuffix(name, "/"))
		if _, exists := kinds[key]; exists {
			return nil, fmt.Errorf("Windows Release ZIP 包含重复条目 %s", name)
		}
		kinds[key] = isDir
		if isDir {
			if !file.FileInfo().IsDir() {
				return nil, fmt.Errorf("Windows Release ZIP 目录类型不一致: %s", name)
			}
		} else {
			mode := file.Mode()
			if !mode.IsRegular() || mode&os.ModeSymlink != 0 {
				return nil, fmt.Errorf("Windows Release ZIP 中 %s 不是普通文件", name)
			}
			size := int64(file.UncompressedSize64)
			if file.UncompressedSize64 > uint64(maxWindowsReleaseFileBytes) || size < 0 || size > maxWindowsReleaseTotalBytes-total {
				return nil, fmt.Errorf("Windows Release ZIP 解压内容超过限制: %s", name)
			}
			total += size
		}
		entries = append(entries, validatedZipEntry{file: file, name: name, isDir: isDir})
	}

	for key := range kinds {
		parts := strings.Split(key, "/")
		for index := 1; index < len(parts); index++ {
			parent := strings.Join(parts[:index], "/")
			if isDir, exists := kinds[parent]; exists && !isDir {
				return nil, fmt.Errorf("Windows Release ZIP 文件/目录冲突: %s", parent)
			}
		}
	}
	return entries, nil
}

func canonicalWindowsReleaseName(raw string) (string, bool, error) {
	if strings.ContainsRune(raw, '\x00') || strings.Contains(raw, "\\") {
		return "", false, fmt.Errorf("Windows Release ZIP 包含非法路径 %q", raw)
	}
	isDir := strings.HasSuffix(raw, "/")
	trimmed := strings.TrimSuffix(raw, "/")
	if trimmed == "" || strings.HasPrefix(trimmed, "/") || strings.Contains(trimmed, ":") {
		return "", false, fmt.Errorf("Windows Release ZIP 包含非法路径 %q", raw)
	}
	clean := path.Clean(trimmed)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || clean != trimmed {
		return "", false, fmt.Errorf("Windows Release ZIP 包含非规范路径 %q", raw)
	}
	for _, segment := range strings.Split(clean, "/") {
		if segment == "" || segment == "." || segment == ".." || strings.HasSuffix(segment, ".") || strings.HasSuffix(segment, " ") || strings.ContainsAny(segment, `<>"|?*`) {
			return "", false, fmt.Errorf("Windows Release ZIP 包含非法路径 %q", raw)
		}
	}
	if isDir {
		clean += "/"
	}
	return clean, isDir, nil
}

func safePayloadTarget(root, relative string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(relative))
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("payload path escapes root")
	}
	root = filepath.Clean(root)
	target := filepath.Join(root, clean)
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("payload path escapes root")
	}
	return target, nil
}

func writeZipEntry(entry *zip.File, target string, mode os.FileMode, limit int64, allowEmpty bool) error {
	if entry == nil || limit < 0 {
		return errors.New("invalid ZIP extraction request")
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	reader, err := entry.Open()
	if err != nil {
		return err
	}
	file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		_ = reader.Close()
		return err
	}
	written, copyErr := io.Copy(file, io.LimitReader(reader, limit+1))
	closeFileErr := file.Close()
	closeReaderErr := reader.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeFileErr != nil {
		return closeFileErr
	}
	if closeReaderErr != nil {
		return closeReaderErr
	}
	if written != int64(entry.UncompressedSize64) || written > limit || (!allowEmpty && written == 0) {
		return fmt.Errorf("ZIP entry size mismatch: wrote=%d expected=%d limit=%d", written, entry.UncompressedSize64, limit)
	}
	return nil
}
