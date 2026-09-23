package skill

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceSkillRefMustBeIssuedByCurrentRuntime(t *testing.T) {
	runtime, _ := newSkillTestService(t)
	workspaceRoot := t.TempDir()
	packageRoot := filepath.Join(workspaceRoot, ".agents", "skills", "workspace-skill")
	if err := os.MkdirAll(packageRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	document := "---\nname: workspace-skill\ndescription: Workspace Skill.\n---\n\n# Workspace\n"
	if err := os.WriteFile(filepath.Join(packageRoot, "SKILL.md"), []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}

	skillRef, sourceID, err := runtime.WorkspaceSkillRef(workspaceRoot, "workspace-skill")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(skillRef, workspaceRoot) || sourceID == "" {
		t.Fatalf("workspace skill_ref leaks host path or source id is empty: ref=%q id=%q", skillRef, sourceID)
	}
	secondRef, secondID, err := runtime.WorkspaceSkillRef(workspaceRoot, "workspace-skill")
	if err != nil {
		t.Fatal(err)
	}
	if secondRef != skillRef || secondID != sourceID {
		t.Fatalf("same runtime/workspace did not reuse source id: first=%q/%q second=%q/%q", skillRef, sourceID, secondRef, secondID)
	}

	resolved, release, err := runtime.Acquire(context.Background(), skillRef)
	if err != nil {
		t.Fatal(err)
	}
	release()
	resolvedInfo, err := os.Stat(resolved.Root)
	if err != nil {
		t.Fatal(err)
	}
	packageInfo, err := os.Stat(packageRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(resolvedInfo, packageInfo) || resolved.SourceType != "workspace" {
		t.Fatalf("workspace skill_ref resolved incorrectly: %#v", resolved)
	}

	forged := "skill://workspace/AAAAAAAAAAAAAAAAAAAAAAAA/workspace-skill"
	if _, _, err := runtime.Acquire(context.Background(), forged); err == nil || !strings.Contains(err.Error(), "was not issued") {
		t.Fatalf("forged workspace skill_ref was accepted: %v", err)
	}
	forgedName := "skill://workspace/" + sourceID + "/other-skill"
	if _, _, err := runtime.Acquire(context.Background(), forgedName); err == nil || !strings.Contains(err.Error(), "was not issued") {
		t.Fatalf("unissued workspace Skill name was accepted for a valid source id: %v", err)
	}
}
