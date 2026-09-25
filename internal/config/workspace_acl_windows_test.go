//go:build windows

package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func configTestDACL(t *testing.T, path string) *windows.SECURITY_DESCRIPTOR {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	return sd
}
func TestNormalizeWindowsWorkspaceACLBoundary(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "existing project 한글")
	child := filepath.Join(workspace, "nested", "keep.txt")
	if err := os.MkdirAll(filepath.Dir(child), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(child, []byte("unchanged workspace content"), 0600); err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	// A deliberately non-private DACL, confined to this disposable fixture.
	sd, err := windows.SecurityDescriptorFromString(fmt.Sprintf("D:P(A;OICI;FA;;;%s)(A;OICI;FR;;;WD)", user.User.Sid.String()))
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(workspace, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
	paths := []string{workspace, filepath.Dir(child), child}
	before := map[string]string{}
	for _, path := range paths {
		before[path] = configTestDACL(t, path).String()
	}
	if !strings.Contains(before[workspace], "WD") {
		t.Fatal("fixture lacks an independently observable workspace ACE")
	}
	home := filepath.Join(root, "private-home")
	cfg := Config{AgentDockHome: home, AgentDockDefaultDir: workspace}
	for i := 0; i < 2; i++ {
		if err := cfg.Normalize(); err != nil {
			t.Fatal(err)
		}
		for _, path := range paths {
			if got := configTestDACL(t, path).String(); got != before[path] {
				t.Fatalf("Normalize rewrote project DACL %s: before=%s after=%s", path, before[path], got)
			}
		}
	}
	data, err := os.ReadFile(child)
	if err != nil || string(data) != "unchanged workspace content" {
		t.Fatal("workspace file changed", err)
	}
	private := configTestDACL(t, home)
	control, _, err := private.Control()
	if err != nil {
		t.Fatal(err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatal("AgentDockHome is not protected")
	}
	privateACL, _, err := private.DACL()
	if err != nil || privateACL == nil {
		t.Fatal("AgentDockHome has no DACL", err)
	}
	allowed := map[string]bool{user.User.Sid.String(): true, "S-1-5-18": true, "S-1-5-32-544": true}
	if privateACL.AceCount != 3 {
		t.Fatalf("private home has unexpected ACE count %d", privateACL.AceCount)
	}
	for i := uint32(0); i < uint32(privateACL.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(privateACL, i, &ace); err != nil {
			t.Fatal(err)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String()
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || !allowed[sid] {
			t.Fatalf("unexpected private home ACE %s", sid)
		}
	}
}
func TestNormalizeWindowsStillCreatesAndValidatesWorkspace(t *testing.T) {
	root := t.TempDir()
	cfg := Config{AgentDockHome: filepath.Join(root, "private"), AgentDockDefaultDir: filepath.Join(root, "new", "workspace")}
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(cfg.AgentDockDefaultDir); err != nil || !info.IsDir() {
		t.Fatal("new workspace was not created", err)
	}
	file := filepath.Join(root, "not-directory")
	if err := os.WriteFile(file, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg.AgentDockDefaultDir = file
	if err := cfg.Normalize(); err == nil {
		t.Fatal("file accepted as workspace")
	}
	cfg.AgentDockDefaultDir = "relative-workspace"
	if err := cfg.Normalize(); err == nil {
		t.Fatal("relative workspace accepted")
	}
}
