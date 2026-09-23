package app

import (
	"context"
	"errors"
	"fmt"
	"github.com/uvwt/agentdock/internal/agentinstructions"
	"github.com/uvwt/agentdock/internal/config"
	"github.com/uvwt/agentdock/internal/plugin"
	"github.com/uvwt/agentdock/internal/snapshot"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var errContextVersionChanged = errors.New("context source changed during build")

type skillIndexSnapshot struct {
	Items         []capabilitySkillItem
	DocumentReads int
}
type contextSnapshots struct {
	rules       *snapshot.Cache[agentinstructions.Snapshot]
	skills      *snapshot.Cache[skillIndexSnapshot]
	common      *snapshot.Cache[*capabilityCommonSkillIndex]
	commonFiles *snapshot.Files
	ruleFiles   *snapshot.Files
	skillFiles  *snapshot.Files
	once        sync.Once
}

func newContextSnapshots(cfg config.Config) *contextSnapshots {
	ruleFilter := func(path string) bool {
		name := filepath.Base(path)
		return name == "AGENTS.md" || name == ".git" || path == cfg.InstructionsFile
	}
	skillRoot := filepath.Join(cfg.AgentDockHome, "skills")
	skillFilter := func(path string) bool {
		relative, err := filepath.Rel(skillRoot, path)
		if err != nil {
			return true
		}
		first := strings.Split(filepath.ToSlash(relative), "/")[0]
		return first != ".locks" && first != ".tmp" && first != ".cache"
	}
	resources := &contextSnapshots{rules: snapshot.New[agentinstructions.Snapshot](32, cfg.ContextBudget(), time.Minute), skills: snapshot.New[skillIndexSnapshot](4, cfg.ContextBudget(), time.Minute), ruleFiles: snapshot.NewFiles(ruleFilter), skillFiles: snapshot.NewTreeFiles(skillRoot, skillFilter)}
	resources.common = snapshot.New[*capabilityCommonSkillIndex](4, cfg.ContextBudget(), time.Minute)
	resources.commonFiles = snapshot.NewFiles(nil)
	for _, dir := range []string{skillRoot, filepath.Join(skillRoot, ".state"), filepath.Join(skillRoot, ".system"), filepath.Join(skillRoot, ".versions")} {
		resources.skillFiles.Add(dir)
	}
	return resources
}
func (s *contextSnapshots) Close() {
	s.once.Do(func() {
		s.rules.Close()
		s.skills.Close()
		s.common.Close()
		s.ruleFiles.Close()
		s.skillFiles.Close()
		s.commonFiles.Close()
	})
}
func (r *Runtime) cachedInstructions(ctx context.Context, options agentinstructions.Options) (agentinstructions.Snapshot, error) {
	if r.contextSnapshots == nil {
		return agentinstructions.Load(ctx, options)
	}
	files := r.contextSnapshots.ruleFiles
	global := options.GlobalFile
	if global == "" {
		global = filepath.Join(options.Home, "AGENTS.md")
	}
	paths := snapshot.MetadataPaths(options.Workdir, global)
	for _, path := range paths {
		files.Add(filepath.Dir(path))
	}
	keyFor := func() (string, error) {
		if err := files.Sync(ctx); err != nil {
			return "", err
		}
		return fmt.Sprintf("%s|%s|%t|%s|%s", options.Workdir, options.DefaultDir, options.DisableAutoLoad, files.Revision(), snapshot.Stamps(paths...)), nil
	}
	for attempt := 0; attempt < 3; attempt++ {
		key, err := keyFor()
		if err != nil {
			return agentinstructions.Snapshot{}, err
		}
		value, _, err := r.contextSnapshots.rules.Get(ctx, key, func(buildCtx context.Context) (agentinstructions.Snapshot, error) {
			before := files.Revision()
			value, err := agentinstructions.Load(buildCtx, options)
			if err != nil {
				return value, err
			}
			if before != files.Revision() {
				return value, errContextVersionChanged
			}
			return value, nil
		})
		if errors.Is(err, errContextVersionChanged) {
			continue
		}
		if err != nil {
			return value, err
		}
		after, err := keyFor()
		if err != nil {
			return value, err
		}
		if key != after {
			continue
		}
		// Callers may add per-request warnings; never hand out the shared slice.
		value.Files = append([]agentinstructions.File{}, value.Files...)
		return value, nil
	}
	return agentinstructions.Snapshot{}, errContextVersionChanged
}
func (r *Runtime) contextSkillIndex(ctx context.Context, directory *plugin.Directory, includeHeavy bool) ([]capabilitySkillItem, snapshot.Info, error) {
	if r.contextSnapshots == nil {
		return nil, snapshot.Info{}, errors.New("context cache is unavailable")
	}
	files := r.contextSnapshots.skillFiles
	paths := r.skills.CapabilityStatePaths()
	keyFor := func() (string, error) {
		if err := files.Sync(ctx); err != nil {
			return "", err
		}
		return fmt.Sprintf("%s|%t|%s|%s", directory.Revision, includeHeavy, files.Revision(), snapshot.Stamps(paths...)), nil
	}
	for attempt := 0; attempt < 3; attempt++ {
		key, err := keyFor()
		if err != nil {
			return nil, snapshot.Info{}, err
		}
		value, info, err := r.contextSnapshots.skills.Get(ctx, key, func(buildCtx context.Context) (skillIndexSnapshot, error) {
			before := files.Revision()
			items, reads, err := r.skills.BuildCapabilityItems(buildCtx, directory, includeHeavy, files.Add)
			if err != nil {
				return skillIndexSnapshot{}, err
			}
			if before != files.Revision() {
				return skillIndexSnapshot{}, errContextVersionChanged
			}
			out := make([]capabilitySkillItem, 0, len(items))
			for _, item := range items {
				out = append(out, capabilitySkillItem{Name: item.Name, Description: truncateString(strings.TrimSpace(item.Description), 160), File: item.File, SkillRef: item.SkillRef, SourceType: item.SourceType, SourceID: item.SourceID, PluginName: item.PluginName, ContentDigest: item.ContentDigest})
			}
			return skillIndexSnapshot{Items: out, DocumentReads: reads}, nil
		})
		if errors.Is(err, errContextVersionChanged) {
			continue
		}
		if err != nil {
			return nil, info, err
		}
		after, err := keyFor()
		if err != nil {
			return nil, info, err
		}
		if key != after {
			continue
		}
		reads := value.DocumentReads
		info.DocumentReads = &reads
		return append([]capabilitySkillItem{}, value.Items...), info, nil
	}
	return nil, snapshot.Info{}, errContextVersionChanged
}
