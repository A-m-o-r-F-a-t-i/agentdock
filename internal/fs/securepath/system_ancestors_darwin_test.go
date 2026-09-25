package securepath

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCanonicalDarwinSystemAncestorsDoNotResolveUserLinks(t *testing.T) {
	for _, name := range []string{"var", "tmp", "etc"} {
		alias := "/" + name
		if _, err := os.Readlink(alias); err != nil {
			t.Fatalf("expected Darwin system alias %s: %v", alias, err)
		}
		if got := CanonicalSystemAncestors(alias + "/workbench/not-created"); got != "/private/"+name+"/workbench/not-created" {
			t.Fatalf("system ancestor %s=%s", alias, got)
		}
		if got := CanonicalSystemAncestors(alias); got != alias {
			t.Fatalf("final link component was normalized: %s", got)
		}
	}
	root := t.TempDir()
	link := filepath.Join(root, "user-link")
	if err := os.Symlink("/private/var", link); err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(link, "workbench")
	got := CanonicalSystemAncestors(input)
	if filepath.Base(filepath.Dir(got)) != "user-link" {
		t.Fatal("user link was silently resolved")
	}
	info, err := os.Lstat(filepath.Dir(got))
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("user link no longer available to strict ancestor checks")
	}
	for _, path := range []string{"/variable/new", "/tmp-other/new", "relative/var/new", "/private/var/new"} {
		if CanonicalSystemAncestors(path) != path {
			t.Fatalf("unexpected canonicalization: %s", path)
		}
	}
}
