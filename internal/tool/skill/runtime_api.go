package skill

import (
	"strings"
	"time"

	skills "github.com/uvwt/agentdock/internal/skill"
)

type CapabilityItem struct {
	Name        string
	Description string
	File        string
	Bundled     bool
	Enabled     bool
	Plugin      string
}

func (s *Service) CapabilityItems() ([]CapabilityItem, error) {
	names, err := s.state.ListSkills()
	if err != nil {
		return nil, err
	}
	if s.pluginSkills != nil {
		members, listErr := s.pluginSkills()
		if listErr != nil {
			return nil, listErr
		}
		for _, member := range members {
			names = append(names, member.Name)
		}
	}
	items := make([]CapabilityItem, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		item, found, itemErr := s.CapabilityItem(name)
		if itemErr != nil {
			return nil, itemErr
		}
		if !found || !item.Enabled {
			continue
		}
		if err := s.ensureAvailable(name); err != nil {
			continue
		}
		items = append(items, item)
	}
	return items, nil
}

// CapabilityItem returns one installed document Skill without applying the
// plugin-level overlay. plugin_load calls it only after validating the plugin.
func (s *Service) CapabilityItem(name string) (CapabilityItem, bool, error) {
	name = strings.TrimSpace(name)
	if s.pluginSkill != nil {
		member, found, err := s.pluginSkill(name)
		if err != nil {
			return CapabilityItem{}, false, err
		}
		if found {
			doc, loadErr := skills.LoadPortableSkillDocument(member.Path)
			if loadErr != nil {
				return CapabilityItem{}, false, nil
			}
			return CapabilityItem{
				Name: name, Description: strings.TrimSpace(doc.Description), File: "skill://" + name + "/SKILL.md",
				Bundled: false, Enabled: member.Enabled, Plugin: member.Plugin,
			}, true, nil
		}
	}
	packageDir, resolveErr := s.state.Resolve(name, "")
	if resolveErr != nil || skills.ValidatePackage(packageDir) != nil {
		return CapabilityItem{}, false, nil
	}
	doc, loadErr := skills.LoadSkillDocument(packageDir)
	if loadErr != nil {
		return CapabilityItem{}, false, nil
	}
	bundled, err := s.state.IsBundled(name)
	if err != nil {
		return CapabilityItem{}, false, err
	}
	enabled, err := s.baseEnabled(name)
	if err != nil {
		return CapabilityItem{}, false, err
	}
	return CapabilityItem{
		Name: name, Description: strings.TrimSpace(doc.Description), File: "skill://" + name + "/SKILL.md",
		Bundled: bundled, Enabled: enabled,
	}, true, nil
}

func (s *Service) RuntimeSkills() (Result, error) {
	return s.runtimeSkillInventory(true)
}

// RuntimeSkillSummaries is the control panel's list-only path. File trees stay
// behind the existing detail endpoint; the default API retains file_count.
func (s *Service) RuntimeSkillSummaries() (Result, error) {
	return s.runtimeSkillInventory(false)
}

func (s *Service) runtimeSkillInventory(includeFiles bool) (Result, error) {
	started := time.Now()
	var members []PluginSkill
	if s.pluginSkills != nil {
		var err error
		members, err = s.pluginSkills()
		if err != nil {
			return nil, skillToolError(err)
		}
	}
	memberByName := make(map[string]PluginSkill, len(members))
	for _, member := range members {
		memberByName[member.Name] = member
	}
	pluginScanFinished := time.Now()
	result, err := s.listWithPluginMembers(members)
	if err != nil {
		return nil, err
	}
	listFinished := time.Now()
	var documentTime, fileTime time.Duration
	items, _ := result["skills"].([]map[string]any)
	for _, item := range items {
		skill, _ := item["skill"].(string)
		version, _ := item["active_version"].(string)
		_, owned := item["plugin"]
		if strings.TrimSpace(skill) == "" || (!owned && strings.TrimSpace(version) == "") {
			continue
		}
		packageDir := ""
		if member, found := memberByName[skill]; found {
			packageDir = member.Path
		}
		if packageDir == "" {
			var pathErr error
			packageDir, pathErr = s.state.InstalledPath(skill, version)
			if pathErr != nil {
				return nil, skillToolError(pathErr)
			}
		}
		// Plugin document metadata and enabled state are already in the list.
		// Standalone selection is also taken from that same snapshot.
		if !owned {
			documentStarted := time.Now()
			document, loadErr := skills.LoadSkillDocument(packageDir)
			if loadErr != nil {
				return nil, skillToolError(loadErr)
			}
			item["name"] = document.Name
			item["description"] = document.Description
			documentTime += time.Since(documentStarted)
		}
		if includeFiles {
			fileStarted := time.Now()
			files, fileErr := collectRuntimeSkillFiles(packageDir)
			if fileErr != nil {
				return nil, fileErr
			}
			item["file_count"] = len(files)
			fileTime += time.Since(fileStarted)
		}
	}
	result["source"] = runtimeAPISource
	result["summary"] = !includeFiles
	result["timing_ms"] = map[string]int64{
		"plugin_scan": pluginScanFinished.Sub(started).Milliseconds(),
		"local_list":  listFinished.Sub(pluginScanFinished).Milliseconds(),
		"documents":   documentTime.Milliseconds(), "files": fileTime.Milliseconds(),
		"total": time.Since(started).Milliseconds(),
	}
	return result, nil
}

func (s *Service) RuntimeSkill(skill string) (Result, error) {
	result, err := s.inspect(InspectRequest{Skill: skill})
	if err != nil {
		return nil, err
	}
	result["source"] = runtimeAPISource
	result["files"] = []runtimeSkillFile{}
	result["file_count"] = 0
	version, _ := result["version"].(string)
	_, owned := result["plugin"]
	if !owned && strings.TrimSpace(version) == "" {
		return result, nil
	}
	packageDir, _, err := s.runtimeSkillPackageDir(skill)
	if err != nil {
		return nil, skillToolError(err)
	}
	files, err := collectRuntimeSkillFiles(packageDir)
	if err != nil {
		return nil, err
	}
	result["files"] = files
	result["file_count"] = len(files)
	return result, nil
}
