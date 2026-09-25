package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestContextBindingWindowsShortPathSelectsSameDirectory(t *testing.T) {
	r := executionTestRuntime(t)
	root := filepath.Join(t.TempDir(), "Context directory with a long name")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	writeInstructionFixture(t, root, "short-path-workspace-rule")
	path, err := windows.UTF16PtrFromString(root)
	if err != nil {
		t.Fatal(err)
	}
	buffer := make([]uint16, 32768)
	n, err := windows.GetShortPathName(path, &buffer[0], uint32(len(buffer)))
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 || n >= uint32(len(buffer)) {
		t.Fatalf("invalid short-path length %d", n)
	}
	short := windows.UTF16ToString(buffer[:n])
	if strings.EqualFold(short, root) {
		t.Skip("test volume does not generate 8.3 directory aliases")
	}
	if !sameExistingTestPath(short, root) {
		t.Fatal("native short path identifies a different directory")
	}
	ctx := scopeHost("short-path-context")
	result, err := r.Call(ctx, "agentdock_context", map[string]any{"workdir": short})
	if err != nil {
		t.Fatal(err)
	}
	selected := assertContextScope(t, result, short, "short-path-workspace-rule")
	assertContextScope(t, result, root, "short-path-workspace-rule")
	again, err := r.Call(ctx, "agentdock_context", map[string]any{"workdir": root})
	if err != nil {
		t.Fatal(err)
	}
	if next := assertContextScope(t, again, root, "short-path-workspace-rule"); next.ID != selected.ID || again["binding_updated"] != false {
		t.Fatal("long and short aliases created different context bindings")
	}
}
