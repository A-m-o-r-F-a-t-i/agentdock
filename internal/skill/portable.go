package skill

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

var portableSkillNamePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?$`)

// LoadPortableSkillDocument reads Agent Skills without requiring the host's
// standalone-package version field. YAML handles quoting, comments and blocks.
func LoadPortableSkillDocument(root string) (SkillDocument, error) {
	path := filepath.Join(root, "SKILL.md")
	info, err := os.Stat(path)
	if err != nil {
		return SkillDocument{}, err
	}
	const limit = 1 << 20
	if !info.Mode().IsRegular() || info.Size() > limit {
		return SkillDocument{}, errors.New("SKILL.md must be a regular file no larger than 1 MiB")
	}
	file, err := os.Open(path)
	if err != nil {
		return SkillDocument{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return SkillDocument{}, err
	}
	if len(data) > limit || !utf8.Valid(data) {
		return SkillDocument{}, errors.New("SKILL.md exceeds its size limit or is not UTF-8")
	}
	doc, err := ParsePortableSkillDocument(data)
	if err != nil {
		return SkillDocument{}, err
	}
	if doc.Name != filepath.Base(root) {
		return SkillDocument{}, errors.New("Skill directory and name must match")
	}
	return doc, nil
}

func ParsePortableSkillDocument(data []byte) (SkillDocument, error) {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	lines := strings.Split(text, "\n")
	if len(lines) < 2 || lines[0] != "---" {
		return SkillDocument{}, errors.New("SKILL.md must start with YAML frontmatter")
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return SkillDocument{}, errors.New("frontmatter is not closed")
	}
	var fields map[string]any
	if err := yaml.Unmarshal([]byte(strings.Join(lines[1:end], "\n")), &fields); err != nil {
		return SkillDocument{}, errors.New("invalid YAML frontmatter")
	}
	name, ok := fields["name"].(string)
	if !ok || !portableSkillNamePattern.MatchString(name) || strings.Contains(name, "--") {
		return SkillDocument{}, errors.New("name must be 1-64 lowercase alphanumeric/hyphen characters without consecutive or edge hyphens")
	}
	description, ok := fields["description"].(string)
	if !ok || strings.TrimSpace(description) == "" || utf8.RuneCountInString(description) > 1024 {
		return SkillDocument{}, errors.New("description must be a non-empty string up to 1024 characters")
	}
	for _, key := range []string{"license", "allowed-tools", "compatibility"} {
		if value, exists := fields[key]; exists {
			text, ok := value.(string)
			if !ok {
				return SkillDocument{}, errors.New(key + " must be a string")
			}
			if key == "compatibility" && (text == "" || utf8.RuneCountInString(text) > 500) {
				return SkillDocument{}, errors.New("compatibility must be 1-500 characters")
			}
		}
	}
	version := ""
	if value, exists := fields["metadata"]; exists {
		metadata, ok := value.(map[string]any)
		if !ok {
			return SkillDocument{}, errors.New("metadata must map string keys to string values")
		}
		for key, value := range metadata {
			text, ok := value.(string)
			if !ok {
				return SkillDocument{}, errors.New("metadata values must be strings")
			}
			if key == "version" {
				version = text
			}
		}
	}
	// Older local packages can be converted without losing their display version.
	// This field is not required and never controls portable Skill discovery.
	if version == "" {
		version, _ = fields["version"].(string)
	}
	return SkillDocument{Name: name, Description: description, Version: version, Body: strings.TrimSpace(strings.Join(lines[end+1:], "\n"))}, nil
}
