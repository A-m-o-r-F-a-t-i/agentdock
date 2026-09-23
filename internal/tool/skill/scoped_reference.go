package skill

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	plugins "github.com/uvwt/agentdock/internal/plugin"
	skills "github.com/uvwt/agentdock/internal/skill"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const (
	managedSourceType   = "managed"
	sharedSourceType    = "shared"
	workspaceSourceType = "workspace"
	pluginSourceType    = "plugin"
	sharedSourceID      = "global"
)

type ResolvedSkill struct {
	Name          string
	SkillRef      string
	SourceType    string
	SourceID      string
	PluginName    string
	Root          string
	ContentDigest string
}

func ManagedSkillRef(name string) string {
	return "skill://managed/" + name
}

func SharedSkillRef(name string) string {
	return "skill://shared/" + name
}

func PluginSkillRef(pluginName, name string) string {
	return "skill://plugin/" + pluginName + "/" + name
}

func (s *Service) WorkspaceSkillRef(workspaceRoot, name string) (skillRef, sourceID string, err error) {
	if !validSkillRefName(name) {
		return "", "", fmt.Errorf("invalid workspace Skill name %q", name)
	}
	root, err := filepath.Abs(strings.TrimSpace(workspaceRoot))
	if err != nil {
		return "", "", err
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", "", err
	}
	s.workspaceMu.Lock()
	defer s.workspaceMu.Unlock()
	if existing := s.workspaceIDByRoot[realRoot]; existing != "" {
		sourceID = existing
	} else {
		raw := make([]byte, 18)
		if _, err := rand.Read(raw); err != nil {
			return "", "", fmt.Errorf("create workspace Skill source id: %w", err)
		}
		sourceID = base64.RawURLEncoding.EncodeToString(raw)
		s.workspaceIDByRoot[realRoot] = sourceID
		s.workspaceByID[sourceID] = realRoot
	}
	issued := s.workspaceIssued[sourceID]
	if issued == nil {
		issued = make(map[string]struct{})
		s.workspaceIssued[sourceID] = issued
	}
	issued[name] = struct{}{}
	return "skill://workspace/" + sourceID + "/" + name, sourceID, nil
}

func (s *Service) Acquire(ctx context.Context, skillRef string) (ResolvedSkill, func(), error) {
	if !strings.HasPrefix(strings.TrimSpace(skillRef), "skill://") {
		name := strings.TrimSpace(skillRef)
		if s.pluginSkill != nil {
			member, found, lookupErr := s.pluginSkill(name)
			if lookupErr != nil {
				return ResolvedSkill{}, nil, lookupErr
			}
			if found {
				skillRef = PluginSkillRef(member.Plugin, name)
			}
		}
		if !strings.HasPrefix(skillRef, "skill://") {
			skillRef = ManagedSkillRef(name)
		}
	}
	ref, err := parseSkillRef(skillRef)
	if err != nil {
		return ResolvedSkill{}, nil, err
	}
	switch ref.SourceType {
	case managedSourceType:
		return s.acquireManaged(ctx, ref)
	case sharedSourceType:
		return s.acquireShared(ctx, ref)
	case workspaceSourceType:
		return s.acquireWorkspace(ctx, ref)
	case pluginSourceType:
		return s.acquirePlugin(ctx, ref)
	default:
		return ResolvedSkill{}, nil, toolErrorDetails("INVALID_SKILL_REF", "unsupported Skill source type", "validation", map[string]any{"skill_ref": skillRef})
	}
}

type parsedSkillRef struct {
	SourceType string
	SourceID   string
	Name       string
}

func parseSkillRef(raw string) (parsedSkillRef, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return parsedSkillRef{}, toolErrorDetails("INVALID_SKILL_REF", "skill_ref is required", "validation", nil)
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "skill" {
		return parsedSkillRef{}, toolErrorDetails("INVALID_SKILL_REF", "skill_ref must be a skill:// reference returned by AgentDock", "validation", map[string]any{"skill_ref": raw})
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Port() != "" {
		return parsedSkillRef{}, toolErrorDetails("INVALID_SKILL_REF", "skill_ref cannot contain credentials, query, fragment, or port", "validation", map[string]any{"skill_ref": raw})
	}
	resourcePath, err := url.PathUnescape(parsed.EscapedPath())
	if err != nil {
		return parsedSkillRef{}, toolErrorDetails("INVALID_SKILL_REF", "skill_ref has invalid URL encoding", "validation", map[string]any{"skill_ref": raw})
	}
	parts := splitSkillURIPath(resourcePath)
	switch parsed.Host {
	case managedSourceType:
		if len(parts) != 1 || !validSkillRefName(parts[0]) {
			return parsedSkillRef{}, invalidSkillRef(raw)
		}
		return parsedSkillRef{SourceType: managedSourceType, SourceID: parts[0], Name: parts[0]}, nil
	case sharedSourceType:
		if len(parts) != 1 || !validSkillRefName(parts[0]) {
			return parsedSkillRef{}, invalidSkillRef(raw)
		}
		return parsedSkillRef{SourceType: sharedSourceType, SourceID: sharedSourceID, Name: parts[0]}, nil
	case workspaceSourceType:
		if len(parts) != 2 || parts[0] == "" || !validSkillRefName(parts[1]) {
			return parsedSkillRef{}, invalidSkillRef(raw)
		}
		sourceBytes, decodeErr := base64.RawURLEncoding.DecodeString(parts[0])
		if decodeErr != nil || len(sourceBytes) != 18 || base64.RawURLEncoding.EncodeToString(sourceBytes) != parts[0] {
			return parsedSkillRef{}, invalidSkillRef(raw)
		}
		return parsedSkillRef{SourceType: workspaceSourceType, SourceID: parts[0], Name: parts[1]}, nil
	case pluginSourceType:
		if len(parts) != 2 || plugins.ValidateName(parts[0]) != nil || !validSkillRefName(parts[1]) {
			return parsedSkillRef{}, invalidSkillRef(raw)
		}
		return parsedSkillRef{SourceType: pluginSourceType, SourceID: parts[0], Name: parts[1]}, nil
	default:
		return parsedSkillRef{}, invalidSkillRef(raw)
	}
}

func invalidSkillRef(raw string) error {
	return toolErrorDetails("INVALID_SKILL_REF", "skill_ref has an invalid or unsupported shape", "validation", map[string]any{"skill_ref": raw})
}

func splitSkillURIPath(value string) []string {
	value = strings.TrimPrefix(value, "/")
	if value == "" {
		return nil
	}
	return strings.Split(value, "/")
}

func validSkillRefName(name string) bool {
	return name != "" && name == path.Base(name) && !strings.ContainsAny(name, `/\\?#`) && name != "." && name != ".."
}

func (s *Service) acquireManaged(ctx context.Context, ref parsedSkillRef) (ResolvedSkill, func(), error) {
	release, err := s.state.AcquireRead(ctx, ref.Name)
	if err != nil {
		return ResolvedSkill{}, nil, toolErrorDetails("SKILL_CONTEXT_INVALID", "acquire managed Skill read lock: "+err.Error(), "runtime", map[string]any{"skill_ref": ManagedSkillRef(ref.Name)})
	}
	ok := false
	defer func() {
		if !ok {
			release()
		}
	}()

	if err := s.ensureAvailable(ref.Name); err != nil {
		return ResolvedSkill{}, nil, err
	}
	root, err := s.state.Resolve(ref.Name, "")
	if err != nil {
		return ResolvedSkill{}, nil, toolErrorDetails("SKILL_NOT_AVAILABLE", "managed Skill is not installed", "not_found", map[string]any{"skill_ref": ManagedSkillRef(ref.Name)})
	}
	if err := verifyResolvedSkillDocument(root, ref.Name); err != nil {
		return ResolvedSkill{}, nil, err
	}
	ok = true
	return ResolvedSkill{
		Name: ref.Name, SkillRef: ManagedSkillRef(ref.Name), SourceType: managedSourceType,
		SourceID: ref.Name, Root: root,
	}, release, nil
}

func (s *Service) acquireShared(ctx context.Context, ref parsedSkillRef) (ResolvedSkill, func(), error) {
	if err := ctx.Err(); err != nil {
		return ResolvedSkill{}, nil, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ResolvedSkill{}, nil, toolErrorDetails("SKILL_CONTEXT_INVALID", "resolve shared Skill home: "+err.Error(), "runtime", map[string]any{"skill_ref": SharedSkillRef(ref.Name)})
	}
	root := filepath.Join(home, ".agents", "skills", ref.Name)
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return ResolvedSkill{}, nil, toolErrorDetails("SKILL_NOT_AVAILABLE", "shared Skill is not available", "not_found", map[string]any{"skill_ref": SharedSkillRef(ref.Name)})
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return ResolvedSkill{}, nil, toolErrorDetails("SKILL_CONTEXT_INVALID", "resolve shared Skill root: "+err.Error(), "runtime", map[string]any{"skill_ref": SharedSkillRef(ref.Name)})
	}
	if err := verifyResolvedSkillDocument(realRoot, ref.Name); err != nil {
		return ResolvedSkill{}, nil, err
	}
	return ResolvedSkill{
		Name: ref.Name, SkillRef: SharedSkillRef(ref.Name), SourceType: sharedSourceType,
		SourceID: sharedSourceID, Root: realRoot,
	}, func() {}, nil
}

func (s *Service) acquireWorkspace(ctx context.Context, ref parsedSkillRef) (ResolvedSkill, func(), error) {
	if err := ctx.Err(); err != nil {
		return ResolvedSkill{}, nil, err
	}
	s.workspaceMu.RLock()
	workspaceRoot, issued := s.workspaceByID[ref.SourceID]
	_, nameIssued := s.workspaceIssued[ref.SourceID][ref.Name]
	s.workspaceMu.RUnlock()
	if !issued || !nameIssued {
		return ResolvedSkill{}, nil, toolErrorDetails("INVALID_SKILL_REF", "workspace skill_ref was not issued by this AgentDock runtime", "validation", map[string]any{"source_id": ref.SourceID})
	}
	realWorkspaceRoot, err := filepath.EvalSymlinks(workspaceRoot)
	if err != nil {
		return ResolvedSkill{}, nil, toolErrorDetails("SKILL_CONTEXT_INVALID", "workspace source is unavailable", "not_found", map[string]any{"skill_ref": "skill://workspace/" + ref.SourceID + "/" + ref.Name})
	}
	packageRoot := filepath.Join(realWorkspaceRoot, ".agents", "skills", ref.Name)
	info, err := os.Lstat(packageRoot)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ResolvedSkill{}, nil, toolErrorDetails("SKILL_NOT_AVAILABLE", "workspace Skill is not available as a regular directory", "not_found", map[string]any{"skill_ref": "skill://workspace/" + ref.SourceID + "/" + ref.Name})
	}
	realPackageRoot, err := filepath.EvalSymlinks(packageRoot)
	if err != nil {
		return ResolvedSkill{}, nil, toolErrorDetails("SKILL_CONTEXT_INVALID", "resolve workspace Skill root: "+err.Error(), "runtime", map[string]any{"skill_ref": "skill://workspace/" + ref.SourceID + "/" + ref.Name})
	}
	rel, err := filepath.Rel(realWorkspaceRoot, realPackageRoot)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return ResolvedSkill{}, nil, toolErrorDetails("SKILL_PATH_ESCAPE", "workspace Skill resolves outside its workspace", "validation", map[string]any{"skill_ref": "skill://workspace/" + ref.SourceID + "/" + ref.Name})
	}
	if err := verifyResolvedSkillDocument(realPackageRoot, ref.Name); err != nil {
		return ResolvedSkill{}, nil, err
	}
	skillRef := "skill://workspace/" + ref.SourceID + "/" + ref.Name
	return ResolvedSkill{
		Name: ref.Name, SkillRef: skillRef, SourceType: workspaceSourceType,
		SourceID: ref.SourceID, Root: realPackageRoot,
	}, func() {}, nil
}

func (s *Service) acquirePlugin(ctx context.Context, ref parsedSkillRef) (ResolvedSkill, func(), error) {
	if err := ctx.Err(); err != nil {
		return ResolvedSkill{}, nil, err
	}
	if s.pluginSkill == nil {
		return ResolvedSkill{}, nil, toolErrorDetails("SKILL_NOT_AVAILABLE", "Plugin Skill runtime is unavailable", "not_found", nil)
	}
	member, found, err := s.pluginSkill(ref.Name)
	if err != nil {
		return ResolvedSkill{}, nil, err
	}
	if !found || member.Plugin != ref.SourceID {
		return ResolvedSkill{}, nil, toolErrorDetails("SKILL_NOT_AVAILABLE", "Plugin member is unavailable", "not_found", nil)
	}
	if !member.Enabled {
		return ResolvedSkill{}, nil, toolErrorDetails("PLUGIN_MEMBER_DISABLED", "Plugin Skill is disabled", "permission", map[string]any{"skill": ref.Name, "plugin": ref.SourceID})
	}
	if err := verifyResolvedSkillDocument(member.Path, ref.Name); err != nil {
		return ResolvedSkill{}, nil, err
	}
	return ResolvedSkill{Name: ref.Name, SkillRef: PluginSkillRef(ref.SourceID, ref.Name), SourceType: pluginSourceType, SourceID: ref.SourceID, PluginName: ref.SourceID, Root: member.Path}, func() {}, nil
}

func verifyResolvedSkillDocument(root, expectedName string) error {
	doc, err := skills.LoadPortableSkillDocument(root)
	if err != nil {
		return skillToolError(err)
	}
	if doc.Name != expectedName {
		return toolErrorDetails("SKILL_CONTEXT_INVALID", "Skill document name does not match its source identity", "runtime", map[string]any{"skill": expectedName})
	}
	return nil
}
