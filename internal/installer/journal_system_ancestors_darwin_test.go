package installer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDarwinRestoreAliasesShareBoundaryWithoutFollowingUserLinks(t *testing.T) {
	root := t.TempDir()
	if strings.HasPrefix(root, "/private/var/") {
		root = strings.TrimPrefix(root, "/private")
	}
	if !strings.HasPrefix(root, "/var/") {
		t.Skip("temporary directory does not use Darwin var alias")
	}
	physical := "/private" + root
	if err := validateRestorePath(filepath.Join(root, "new-target")); err != nil {
		t.Fatal(err)
	}
	if !restoreWithin(filepath.Join(root, "new-target"), physical) || !restoreWithin(filepath.Join(physical, "new-target"), root) || restorePathKey(root) != restorePathKey(physical) {
		t.Fatal("system aliases bypassed transaction overlap detection")
	}
	link := filepath.Join(root, "user-link")
	if err := os.Symlink(physical, link); err != nil {
		t.Fatal(err)
	}
	if validateRestorePath(filepath.Join(link, "new-target")) == nil {
		t.Fatal("ordinary linked ancestor was admitted")
	}
	if validateRestorePath("/var") == nil {
		t.Fatal("restoring the system alias itself was admitted")
	}
}
