package client

import (
	"strings"
	"unicode"
)

// OneLineDescription is deterministic, not a model summarization. The complete
// original text remains in inspect; directory items contain only two fields.
func OneLineDescription(description string) string {
	lines := []string{}
	inCode := false
	for _, line := range strings.Split(strings.ReplaceAll(description, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
			inCode = !inCode
			continue
		}
		if !inCode && line != "" {
			lines = append(lines, strings.TrimLeft(line, "#>*- "))
		}
	}
	text := strings.Join(strings.Fields(strings.Join(lines, " ")), " ")
	runes := []rune(text)
	for i, char := range runes {
		if char == '。' || char == '！' || char == '？' || (char == '.' || char == '!' || char == '?') && (i+1 == len(runes) || unicode.IsSpace(runes[i+1])) {
			return strings.TrimSpace(string(runes[:i+1]))
		}
	}
	return text
}

// CachedSummary does not deep-copy all schemas simply to append two columns to
// a business response. It reads an immutable catalog without I/O or server locks.
func (m *Manager) CachedSummary(server string) (map[string]any, error) {
	catalog, fresh, err := m.catalogSnapshot(strings.TrimSpace(server))
	if err != nil {
		return nil, err
	}
	tools := make([]map[string]any, 0, len(catalog.Tools))
	for _, name := range catalog.Names() {
		tools = append(tools, map[string]any{"name": server + ":" + name, "description": OneLineDescription(catalog.Tools[name].Description)})
	}
	return map[string]any{"server": server, "catalog_revision": catalog.Revision, "complete": catalog.Complete, "stale": !fresh, "total": len(tools), "tools": tools}, nil
}
