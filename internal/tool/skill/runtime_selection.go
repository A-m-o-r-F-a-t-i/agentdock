package skill

import (
	"context"
	skills "github.com/uvwt/agentdock/internal/skill"
	"path/filepath"
	"strings"
	"time"
)

type runtimeSelection struct {
	ResolvedSkill
	Version string
}

func (s *Service) runtimeSelection(raw string) (runtimeSelection, func(), error) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "skill://") {
		if raw == "" || filepath.Base(raw) != raw || strings.ContainsAny(raw, `/\`) || strings.Contains(raw, "..") {
			return runtimeSelection{}, nil, toolErrorDetails("INVALID_SKILL", "invalid skill name", "validation", nil)
		}
		ref := ManagedSkillRef(raw)
		if s.pluginSkill != nil {
			member, found, err := s.pluginSkill(raw)
			if err != nil {
				return runtimeSelection{}, nil, err
			}
			if found {
				ref = PluginSkillRef(member.Plugin, raw)
			}
		}
		raw = ref
	}
	ref, err := parseSkillRef(raw)
	if err != nil {
		return runtimeSelection{}, nil, err
	}
	release := func() {}
	resolved := ResolvedSkill{Name: ref.Name, SkillRef: raw, SourceType: ref.SourceType, SourceID: ref.SourceID}
	switch ref.SourceType {
	case managedSourceType:
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		release, err = s.state.AcquireRead(ctx, ref.Name)
		cancel()
		if err != nil {
			return runtimeSelection{}, nil, err
		}
		selection, err := s.state.Snapshot(ref.Name)
		if err != nil {
			release()
			return runtimeSelection{}, nil, err
		}
		if selection.ActiveVersion == "" {
			release()
			return runtimeSelection{}, nil, toolErrorDetails("SKILL_NOT_ACTIVE", "skill has no active version", "not_found", nil)
		}
		root, err := s.state.InstalledPath(ref.Name, selection.ActiveVersion)
		if err != nil {
			release()
			return runtimeSelection{}, nil, err
		}
		resolved.Root = root
		return runtimeSelection{ResolvedSkill: resolved, Version: selection.ActiveVersion}, release, nil
	case pluginSourceType:
		if s.pluginSkill == nil {
			return runtimeSelection{}, nil, toolErrorDetails("SKILL_NOT_AVAILABLE", "Plugin provider is unavailable", "not_found", nil)
		}
		member, found, err := s.pluginSkill(ref.Name)
		if err != nil {
			return runtimeSelection{}, nil, err
		}
		if !found || member.Plugin != ref.SourceID {
			return runtimeSelection{}, nil, toolErrorDetails("SKILL_NOT_AVAILABLE", "Plugin member not found", "not_found", nil)
		}
		resolved.Root = member.Path
		resolved.PluginName = member.Plugin
	default:
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		resolved, release, err = s.Acquire(ctx, raw)
		cancel()
		if err != nil {
			return runtimeSelection{}, nil, err
		}
	}
	doc, err := skills.LoadPortableSkillDocument(resolved.Root)
	if err != nil {
		release()
		return runtimeSelection{}, nil, err
	}
	return runtimeSelection{ResolvedSkill: resolved, Version: doc.Version}, release, nil
}
func addRuntimeSelection(result Result, selection runtimeSelection) Result {
	result["skill_ref"] = selection.SkillRef
	result["source_type"] = selection.SourceType
	result["source_id"] = selection.SourceID
	if selection.PluginName != "" {
		result["plugin_name"] = selection.PluginName
	}
	return result
}
