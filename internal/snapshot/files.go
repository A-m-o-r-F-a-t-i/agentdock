package snapshot

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Files watches explicitly selected metadata directories. It never recursively
// scans a repository or parses Skill bodies. Callers add directories while
// doing their one actual metadata build, including the parent of absent files.
type Files struct {
	treeRoot     string
	treeClose    func()
	watcher      *fsnotify.Watcher
	filter       func(string) bool
	mu           sync.Mutex
	dirs         map[string]uint64
	sequence     uint64
	revision     atomic.Uint64
	unhealthy    atomic.Bool
	syncRequests chan chan struct{}
	done         chan struct{}
	once         sync.Once
}

func NewFiles(filter func(string) bool) *Files {
	f := &Files{filter: filter, dirs: map[string]uint64{}, syncRequests: make(chan chan struct{}), done: make(chan struct{})}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		f.unhealthy.Store(true)
		close(f.done)
		return f
	}
	f.watcher = watcher
	go f.readEvents()
	return f
}

// NewTreeFiles uses a single stable-root recursive notification handle on
// Windows. Watching every nested directory would block atomic plugin/Skill
// directory replacement even with FILE_SHARE_DELETE.
func NewTreeFiles(root string, filter func(string) bool) *Files {
	f := NewFiles(filter)
	if closeTree, ok := startTreeWatch(filepath.Clean(root), f.event, func() { f.unhealthy.Store(true); f.revision.Add(1) }); ok {
		f.treeRoot = filepath.Clean(root)
		f.treeClose = closeTree
	}
	return f
}
func (f *Files) readEvents() {
	defer close(f.done)
	for {
		select {
		case event, ok := <-f.watcher.Events:
			if !ok {
				return
			}
			f.event(event)
		case _, ok := <-f.watcher.Errors:
			if !ok {
				return
			}
			f.unhealthy.Store(true)
			f.revision.Add(1)
		case reply := <-f.syncRequests:
			// Process queued events before acknowledging the caller's read barrier.
			for {
				select {
				case event, ok := <-f.watcher.Events:
					if !ok {
						close(reply)
						return
					}
					f.event(event)
				default:
					close(reply)
					goto drained
				}
			}
		drained:
		}
	}
}
func (f *Files) event(e fsnotify.Event) {
	if f.filter == nil || f.filter(e.Name) {
		f.revision.Add(1)
	}
	if e.Has(fsnotify.Remove) || e.Has(fsnotify.Rename) {
		f.mu.Lock()
		delete(f.dirs, filepath.Clean(e.Name))
		f.mu.Unlock()
	}
}
func (f *Files) Add(path string) {
	if f == nil || f.watcher == nil {
		return
	}
	path = filepath.Clean(path)
	if f.treeRoot != "" {
		if rel, err := filepath.Rel(f.treeRoot, path); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			return
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sequence++
	if _, ok := f.dirs[path]; ok {
		f.dirs[path] = f.sequence
		return
	}
	for {
		info, err := os.Lstat(path)
		if err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
			break
		}
		parent := filepath.Dir(path)
		if parent == path {
			f.unhealthy.Store(true)
			return
		}
		path = parent
		if _, ok := f.dirs[path]; ok {
			f.dirs[path] = f.sequence
			return
		}
	}
	// Bound handles across many transient projects. Removing a watch changes the
	// revision; controlled TTL revalidation covers unsupported/overflowed watches.
	if len(f.dirs) >= 2048 {
		victim := ""
		oldest := ^uint64(0)
		for dir, used := range f.dirs {
			if used < oldest {
				victim, oldest = dir, used
			}
		}
		_ = f.watcher.Remove(victim)
		delete(f.dirs, victim)
		f.revision.Add(1)
	}
	if err := f.watcher.Add(path); err != nil {
		f.unhealthy.Store(true)
		return
	}
	f.dirs[path] = f.sequence
}
func (f *Files) Sync(ctx context.Context) error {
	if f == nil || f.watcher == nil {
		return nil
	}
	reply := make(chan struct{})
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-f.done:
		return nil
	case f.syncRequests <- reply:
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-f.done:
		return nil
	case <-reply:
		return nil
	}
}
func (f *Files) Revision() string {
	if f == nil {
		return "none"
	}
	rev := strconv.FormatUint(f.revision.Load(), 10)
	if f.unhealthy.Load() {
		rev += "-revalidate-" + strconv.FormatInt(time.Now().UnixMilli()/250, 10)
	}
	return rev
}
func (f *Files) Invalidate() {
	if f != nil {
		f.revision.Add(1)
	}
}
func (f *Files) Healthy() bool { return f != nil && !f.unhealthy.Load() }
func (f *Files) Close() {
	if f == nil {
		return
	}
	f.once.Do(func() {
		if f.treeClose != nil {
			f.treeClose()
		}
		if f.watcher != nil {
			_ = f.watcher.Close()
		}
		<-f.done
	})
}

// Stamps uses only metadata for a known bounded set of paths. It is not a walk
// or full content hash. File notifications cover same-size/same-mtime rewrites.
func Stamps(paths ...string) string {
	var out strings.Builder
	for _, path := range paths {
		out.WriteString(path)
		out.WriteByte('=')
		info, err := os.Lstat(path)
		if err != nil {
			out.WriteString("missing:")
			if !os.IsNotExist(err) {
				out.WriteString(err.Error())
			}
		} else {
			fmt.Fprintf(&out, "%d/%d/%d", info.Size(), info.ModTime().UnixNano(), info.Mode())
		}
		out.WriteByte('|')
	}
	return out.String()
}

// MetadataPaths returns deterministic rule candidates up to the filesystem
// root, without following links or changing how the rules loader chooses scope.
func MetadataPaths(workdir, global string) []string {
	paths := []string{global}
	for dir := filepath.Clean(workdir); ; dir = filepath.Dir(dir) {
		paths = append(paths, filepath.Join(dir, "AGENTS.md"), filepath.Join(dir, ".git"))
		if parent := filepath.Dir(dir); parent == dir {
			break
		}
	}
	sort.Strings(paths)
	return paths
}
