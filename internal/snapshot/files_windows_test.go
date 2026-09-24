//go:build windows

package snapshot

import (
	"golang.org/x/sys/windows"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTreeWatchMixedShortAndLongPathsDoesNotWatchMembers(t *testing.T) {
	root := filepath.Join(t.TempDir(), "long-plugin-root-for-alias-test")
	member := filepath.Join(root, "member", "skills", "nested")
	if err := os.MkdirAll(member, 0700); err != nil {
		t.Fatal(err)
	}
	pointer, err := windows.UTF16PtrFromString(root)
	if err != nil {
		t.Fatal(err)
	}
	buffer := make([]uint16, 32768)
	count, err := windows.GetShortPathName(pointer, &buffer[0], uint32(len(buffer)))
	if err != nil || count == 0 || count >= uint32(len(buffer)) {
		t.Skipf("short paths unavailable: %v", err)
	}
	short := windows.UTF16ToString(buffer[:count])
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if strings.EqualFold(short, canonical) {
		t.Skip("volume does not expose a short-path alias")
	}
	files := NewTreeFiles(short, nil)
	defer files.Close()
	if files.treeClose == nil {
		t.Fatal("recursive watcher unavailable")
	}
	files.Add(short)
	files.Add(filepath.Join(canonical, "member"))
	files.Add(filepath.Join(canonical, "member", "skills", "nested"))
	files.mu.Lock()
	countDirs := len(files.dirs)
	files.mu.Unlock()
	if countDirs != 0 {
		t.Fatalf("opened %d nested handles for the same tree", countDirs)
	}
	before := files.Revision()
	if err := os.Rename(filepath.Join(canonical, "member"), filepath.Join(canonical, "replaced")); err != nil {
		t.Fatal("watch prevented replacement", err)
	}
	await(t, func() bool { return files.Revision() != before })
}
