package app

import (
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	skills "github.com/uvwt/agentdock/internal/skill"
)

const (
	filesystemSkillIndexLimit       = 50
	filesystemSkillDescriptionBytes = 120
	filesystemSkillDocumentMaxBytes = 1 << 20
)

type filesystemSkillScanOptions struct {
	// common Skills historically allow package directories to be symlinks.
	// workspace-local Skills do not follow package-directory symlinks outside
	// the selected workspace root.
	AllowPackageSymlinks bool
}

type filesystemSkillItem struct {
	Name        string
	Description string
	File        string
}

type filesystemSkillIndex struct {
	Items     []filesystemSkillItem
	Total     int
	Truncated bool
}

// scanFilesystemSkills shares metadata parsing, stable ordering, and truncation.
// The caller chooses only the package-directory symlink policy so existing
// common Skill behavior is preserved without weakening workspace isolation.
func scanFilesystemSkills(root string, options filesystemSkillScanOptions) (filesystemSkillIndex, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return filesystemSkillIndex{Items: []filesystemSkillItem{}}, nil
		}
		return filesystemSkillIndex{}, err
	}

	items := make([]filesystemSkillItem, 0, len(entries))
	for _, entry := range entries {
		packageDir := filepath.Join(root, entry.Name())
		var info os.FileInfo
		var statErr error
		if options.AllowPackageSymlinks {
			info, statErr = os.Stat(packageDir)
		} else {
			info, statErr = os.Lstat(packageDir)
		}
		if statErr != nil || !info.IsDir() {
			continue
		}
		documentPath := filepath.Join(packageDir, "SKILL.md")
		data, readErr := readFilesystemSkillDocument(packageDir, options.AllowPackageSymlinks)
		if readErr != nil {
			continue
		}
		metadata, parseErr := skills.ParseSkillMetadata(data)
		if parseErr != nil {
			continue
		}
		items = append(items, filesystemSkillItem{
			Name:        metadata.Name,
			Description: truncateString(strings.TrimSpace(metadata.Description), filesystemSkillDescriptionBytes),
			File:        documentPath,
		})
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].Name == items[j].Name {
			return items[i].File < items[j].File
		}
		return items[i].Name < items[j].Name
	})
	index := filesystemSkillIndex{Items: items, Total: len(items)}
	if index.Total > filesystemSkillIndexLimit {
		index.Truncated = true
		index.Items = index.Items[:filesystemSkillIndexLimit]
	}
	return index, nil
}

func readFilesystemSkillDocument(packageDir string, allowSymlinks bool) ([]byte, error) {
	if allowSymlinks {
		// Preserve the historical common-Skill behavior: package/document
		// symlinks are allowed, but reads are still bounded for indexing.
		file, err := os.Open(filepath.Join(packageDir, "SKILL.md"))
		if err != nil {
			return nil, err
		}
		defer file.Close()
		return readBoundedSkillDocument(file)
	}

	// Workspace-local Skills are indexes for the selected repository, so do
	// not let a leaf symlink escape the package directory.
	root, err := os.OpenRoot(packageDir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	before, err := root.Lstat("SKILL.md")
	if err != nil || !before.Mode().IsRegular() {
		return nil, os.ErrInvalid
	}
	file, err := root.Open("SKILL.md")
	if err != nil {
		return nil, err
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) {
		return nil, os.ErrInvalid
	}
	return readBoundedSkillDocument(file)
}

func readBoundedSkillDocument(file *os.File) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(file, filesystemSkillDocumentMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > filesystemSkillDocumentMaxBytes {
		return nil, os.ErrInvalid
	}
	return data, nil
}
