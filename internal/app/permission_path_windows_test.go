package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestProfileProtectedHomeRecognizesWindowsShortPathAlias(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "Protected AgentDock home with a long name")
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	path, err := windows.UTF16PtrFromString(home)
	if err != nil {
		t.Fatal(err)
	}
	buffer := make([]uint16, 32768)
	n, err := windows.GetShortPathName(path, &buffer[0], uint32(len(buffer)))
	if err != nil || n == 0 || n >= uint32(len(buffer)) {
		t.Fatalf("short path lookup failed: %d %v", n, err)
	}
	short := windows.UTF16ToString(buffer[:n])
	if strings.EqualFold(short, home) {
		t.Skip("volume does not generate distinct short aliases")
	}
	if !pathOverlaps(filepath.Join(home, "owned.txt"), short) || !pathOverlaps(filepath.Join(short, "nested", "new.txt"), home) {
		t.Fatal("short/long alias bypassed protected home check")
	}
	if !pathOverlaps(root, short) {
		t.Fatal("parent rename could cover protected home")
	}
	if pathOverlaps(filepath.Join(root, "independent.txt"), short) {
		t.Fatal("unrelated workspace path was incorrectly rejected")
	}
}
