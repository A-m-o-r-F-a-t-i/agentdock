package skill

import (
	"context"
	registry "github.com/uvwt/agentdock/internal/plugin"
	skills "github.com/uvwt/agentdock/internal/skill"
	"path/filepath"
	"sort"
	"strings"
)

// BuildCapabilityItems consumes one Plugin directory. It never re-enters a
// plugin store for each standalone or plugin member and filters Heavy first.
func (s *Service) BuildCapabilityItems(ctx context.Context, directory *registry.Directory, includeHeavy bool, watch func(string)) ([]CapabilityItem, int, error) {
	names, err := s.state.ListSkills()
	if err != nil {
		return nil, 0, err
	}
	out := make([]CapabilityItem, 0, len(names))
	reads := 0
	owned := map[string]struct{}{}
	if directory != nil {
		for _, member := range directory.Skills() {
			owned[member.Name] = struct{}{}
		}
	}
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return nil, reads, err
		}
		if _, found := owned[name]; found {
			continue
		}
		release, err := s.state.AcquireRead(ctx, name)
		if err != nil {
			return nil, reads, err
		}
		func() {
			defer release()
			selection, err := s.state.Snapshot(name)
			if err != nil || selection.Disabled || selection.ActiveVersion == "" {
				return
			}
			root, err := s.state.InstalledPath(name, selection.ActiveVersion)
			if err != nil {
				return
			}
			if watch != nil {
				watch(root)
			}
			if err := skills.ValidatePackage(root); err != nil {
				return
			}
			reads++
			doc, err := skills.LoadSkillDocument(root)
			if err != nil {
				return
			}
			out = append(out, CapabilityItem{Name: name, Description: strings.TrimSpace(doc.Description), File: ManagedSkillRef(name) + "/SKILL.md", Enabled: true, Bundled: selection.System, SkillRef: ManagedSkillRef(name), SourceType: managedSourceType, SourceID: name})
		}()
	}
	if directory != nil {
		for _, member := range directory.Skills() {
			if err := ctx.Err(); err != nil {
				return nil, reads, err
			}
			if !member.Enabled || member.Heavy && !includeHeavy {
				continue
			}
			description := member.Description
			if description == "" {
				reads++
				doc, err := skills.LoadPortableSkillDocument(member.Path)
				if err != nil {
					continue
				}
				if doc.Name != member.Name {
					continue
				}
				description = doc.Description
			}
			ref := PluginSkillRef(member.Plugin, member.Name)
			out = append(out, CapabilityItem{Name: member.Name, Description: strings.TrimSpace(description), File: ref + "/SKILL.md", Enabled: true, Plugin: member.Plugin, PluginName: member.Plugin, SkillRef: ref, SourceType: pluginSourceType, SourceID: member.Plugin})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, reads, ctx.Err()
}
func (s *Service) CapabilityStatePaths() []string {
	return []string{s.state.Root(), filepath.Join(s.state.Root(), ".state"), filepath.Join(s.state.Root(), ".system"), filepath.Join(s.state.Root(), ".versions")}
}
