package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExternalTargetAcceptsOnlyEquivalentCanonicalPath(t *testing.T) {
	_, record := registryFixture(t)
	parent := t.TempDir()
	canonical, err := canonicalNativePath(parent)
	if err != nil {
		t.Fatal(err)
	}
	declared := parent + string(os.PathSeparator) + "." + string(os.PathSeparator) + "explicit.txt"
	resolved := filepath.Join(canonical, "explicit.txt")
	// The first preparation preserves the user spelling; execution revalidation
	// receives the canonical absolute path captured by that preparation.
	initial, err := ResolveTarget(record, TargetRequest{Kind: "external", Path: declared, ExternalPath: declared})
	if err != nil || initial.ResolvedPath != resolved {
		t.Fatalf("prepare: %+v %v", initial, err)
	}
	check, err := ResolveTarget(record, TargetRequest{Kind: "external", Path: resolved, ExternalPath: declared})
	if err != nil || check.ResolvedPath != initial.ResolvedPath {
		t.Fatalf("revalidate same target: %+v %v", check, err)
	}
	for _, other := range []string{"other.txt", filepath.Join(canonical, "other.txt"), filepath.Join(t.TempDir(), "explicit.txt")} {
		if _, err := ResolveTarget(record, TargetRequest{Kind: "external", Path: other, ExternalPath: declared}); err == nil {
			t.Fatalf("different target accepted: %q", other)
		}
	}
	if _, err := os.Stat(resolved); !os.IsNotExist(err) {
		t.Fatal("target validation wrote a file")
	}
}
