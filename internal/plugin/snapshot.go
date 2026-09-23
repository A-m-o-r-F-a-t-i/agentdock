package plugin

import (
	"context"
	"errors"
	"fmt"
	"github.com/uvwt/agentdock/internal/fs/filelock"
	"github.com/uvwt/agentdock/internal/snapshot"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var errDirectoryChanged = errors.New("plugin directory changed during snapshot build")

type BuildMetrics struct {
	PluginScans    int     `json:"plugin_scans"`
	SkillDocuments int     `json:"skill_documents"`
	LockWaitMS     float64 `json:"lock_wait_ms"`
	ElapsedMS      float64 `json:"build_elapsed_ms"`
}
type Directory struct {
	Revision        string
	Metrics         BuildMetrics
	records         map[string]packageRecord
	skills          map[string]SkillMember
	orderedSkills   []SkillMember
	skillMembership map[string]Membership
	mcpMembership   map[string]Membership
}

func (d *Directory) Skills() []SkillMember                 { return append([]SkillMember(nil), d.orderedSkills...) }
func (d *Directory) Skill(name string) (SkillMember, bool) { v, ok := d.skills[name]; return v, ok }
func (d *Directory) SkillMembership(name string) (Membership, bool) {
	v, ok := d.skillMembership[name]
	return v, ok
}
func (d *Directory) MCPMembership(name string) (Membership, bool) {
	v, ok := d.mcpMembership[name]
	return v, ok
}
func (d *Directory) Definitions() []Definition {
	out := make([]Definition, 0, len(d.records))
	for _, name := range sortedPackageNames(d.records) {
		out = append(out, cloneDefinition(d.records[name].definition))
	}
	return out
}

type storeSnapshots struct {
	cache         *snapshot.Cache[*Directory]
	files         *snapshot.Files
	keyMu         sync.Mutex
	stamp         string
	stampAt       time.Time
	stampRevision string
	once          sync.Once
}

func newStoreSnapshots(root string, budget time.Duration) *storeSnapshots {
	filter := func(path string) bool {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return true
		}
		first := strings.Split(filepath.ToSlash(rel), "/")[0]
		return first != ".locks" && first != ".tmp" && first != ".data"
	}
	resources := &storeSnapshots{cache: snapshot.New[*Directory](4, budget, time.Minute), files: snapshot.NewTreeFiles(root, filter)}
	for _, dir := range []string{root, filepath.Join(root, ".state"), filepath.Join(root, ".config")} {
		resources.files.Add(dir)
	}
	return resources
}
func (s *storeSnapshots) Close() { s.once.Do(func() { s.cache.Close(); s.files.Close() }) }
func (s *Store) Close() {
	if s != nil && s.snapshots != nil {
		s.snapshots.Close()
	}
}
func (s *Store) Invalidate() {
	if s.snapshots == nil {
		return
	}
	s.snapshots.keyMu.Lock()
	s.snapshots.stampAt = time.Time{}
	s.snapshots.files.Invalidate()
	s.snapshots.cache.Invalidate()
	s.snapshots.keyMu.Unlock()
}
func (s *Store) SnapshotStats() snapshot.Stats { return s.snapshots.cache.Stats() }
func (s *Store) revision(ctx context.Context, force bool) (string, error) {
	if err := s.snapshots.files.Sync(ctx); err != nil {
		return "", err
	}
	revision := s.snapshots.files.Revision()
	s.snapshots.keyMu.Lock()
	defer s.snapshots.keyMu.Unlock()
	// The bounded stamp checks cover cross-instance atomic writes. Repeated
	// member lookups during one pass do not perform per-member filesystem I/O.
	if force || revision != s.snapshots.stampRevision || time.Since(s.snapshots.stampAt) > 10*time.Millisecond {
		s.snapshots.stamp = snapshot.Stamps(s.root, filepath.Join(s.root, ".state"), filepath.Join(s.root, ".config"))
		s.snapshots.stampAt = time.Now()
		s.snapshots.stampRevision = revision
	}
	return revision + "|" + s.snapshots.stamp, nil
}
func (s *Store) Snapshot(ctx context.Context) (*Directory, snapshot.Info, error) {
	ctx, cancel := context.WithTimeout(ctx, s.budget)
	defer cancel()
	var info snapshot.Info
	for attempt := 0; attempt < 3; attempt++ {
		revision, err := s.revision(ctx, false)
		if err != nil {
			return nil, info, err
		}
		value, current, err := s.snapshots.cache.Get(ctx, revision, func(buildCtx context.Context) (*Directory, error) { return s.buildSnapshot(buildCtx, revision) })
		info = current
		if errors.Is(err, errDirectoryChanged) {
			continue
		}
		if err != nil {
			return nil, info, err
		}
		now, err := s.revision(ctx, false)
		if err != nil {
			return nil, info, err
		}
		if now == value.Revision {
			return value, info, nil
		}
	}
	return nil, info, errDirectoryChanged
}
func (s *Store) acquireSnapshot(ctx context.Context, metrics *BuildMetrics) (func(), error) {
	start := time.Now()
	lock, err := filelock.Acquire(ctx, s.lockPath)
	metrics.LockWaitMS += float64(time.Since(start)) / float64(time.Millisecond)
	if err != nil {
		return nil, err
	}
	return lock, nil
}
func (s *Store) buildSnapshot(ctx context.Context, requested string) (*Directory, error) {
	start := time.Now()
	metrics := BuildMetrics{}
	release, err := s.acquireSnapshot(ctx, &metrics)
	if err != nil {
		return nil, err
	}
	before, err := s.revision(ctx, true)
	if err != nil {
		release()
		return nil, err
	}
	if before != requested {
		release()
		return nil, errDirectoryChanged
	}
	entries, err := os.ReadDir(s.root)
	release()
	if err != nil {
		return nil, err
	}
	records := map[string]packageRecord{}
	// Disk parsing is outside the writer lock; publishing checks the same version
	// again. A cancelled scan checks ctx at each package and Skill boundary.
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if strings.HasPrefix(entry.Name(), ".") || !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		root := filepath.Join(s.root, entry.Name())
		metrics.PluginScans++
		record, err := readPackageContext(ctx, root, true, true, s.snapshots.files.Add, &metrics)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err != nil {
			records[entry.Name()] = packageRecord{root: root, manifest: Manifest{Name: entry.Name()}, definition: Definition{Name: entry.Name(), Path: root, Enabled: false, Diagnostics: []string{err.Error()}}}
			continue
		}
		if record.manifest.Name != entry.Name() {
			return nil, newError("PLUGIN_DIRECTORY_MISMATCH", "plugin directory name must equal manifest name", map[string]any{"directory": entry.Name()}, nil)
		}
		if err := ensureUniqueOwnership(records, record, ""); err != nil {
			return nil, err
		}
		records[entry.Name()] = record
	}
	release, err = s.acquireSnapshot(ctx, &metrics)
	if err != nil {
		return nil, err
	}
	after, err := s.revision(ctx, true)
	release()
	if err != nil {
		return nil, err
	}
	if before != after {
		return nil, errDirectoryChanged
	}
	directory := &Directory{Revision: after, records: records, skills: map[string]SkillMember{}, skillMembership: map[string]Membership{}, mcpMembership: map[string]Membership{}}
	for _, name := range sortedPackageNames(records) {
		record := records[name]
		for _, skill := range sortedKeys(record.skillPaths) {
			enabled := record.state.Enabled && memberEnabled(record.state.Skills, skill)
			member := SkillMember{Name: skill, Plugin: name, Path: record.skillPaths[skill], Description: record.skillDescriptions[skill], Heavy: record.definition.Heavy, Enabled: enabled}
			directory.skills[skill] = member
			directory.orderedSkills = append(directory.orderedSkills, member)
			directory.skillMembership[skill] = Membership{Plugin: name, Enabled: enabled, Heavy: record.definition.Heavy}
		}
		for mcp, cfg := range record.mcpConfigs {
			directory.mcpMembership[mcp] = Membership{Plugin: name, Enabled: cfg.Enabled && record.state.Enabled && memberEnabled(record.state.MCPServers, mcp), Heavy: record.definition.Heavy}
		}
	}
	metrics.ElapsedMS = float64(time.Since(start)) / float64(time.Millisecond)
	directory.Metrics = metrics
	return directory, nil
}
func (s *Store) SnapshotRevision(ctx context.Context) (string, error) {
	d, _, err := s.Snapshot(ctx)
	if err != nil {
		return "", err
	}
	return fmt.Sprint(d.Revision), nil
}
