package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/uvwt/agentdock/internal/agentinstructions"
	"github.com/uvwt/agentdock/internal/config"
)

func newContextRegressionRuntime(t *testing.T) *Runtime {
	t.Helper()
	cfg := config.Config{AgentDockHome: t.TempDir(), AgentDockDefaultDir: t.TempDir()}
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	rt, err := newUnrestrictedTestRuntime(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	return rt
}

func TestCommonContextCacheCopiesAndSameTimestampInvalidation(t *testing.T) {
	home := t.TempDir()
	setUserHomeForTest(t, home)
	root := filepath.Join(home, ".agents", "skills")
	writeCommonSkillForTest(t, root, "shared-test", "shared-test", "before")
	rt := newContextRegressionRuntime(t)
	first, _, err := rt.cachedCommonSkillIndex(t.Context())
	if err != nil || len(first.Items) != 1 {
		t.Fatalf("first index: %#v %v", first, err)
	}
	first.Items[0].Description = "must not enter shared cache"
	second, info, err := rt.cachedCommonSkillIndex(t.Context())
	if err != nil || !info.CacheHit || second.Items[0].Description != "before" {
		t.Fatalf("cache copy: %#v %#v %v", second, info, err)
	}
	path := filepath.Join(root, "shared-test", "SKILL.md")
	stat, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	revision := rt.contextSnapshots.commonFiles.Revision()
	if err := os.WriteFile(path, []byte(strings.Replace(string(data), "before", "after!", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, stat.ModTime(), stat.ModTime()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for rt.contextSnapshots.commonFiles.Revision() == revision && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	updated, _, err := rt.cachedCommonSkillIndex(t.Context())
	if err != nil || updated.Items[0].Description != "after!" {
		t.Fatalf("stale same-stamp index: %#v %v", updated, err)
	}
}

func TestRuntimeCloseReleasesAllContextCaches(t *testing.T) {
	rt := newContextRegressionRuntime(t)
	if err := rt.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := rt.contextSnapshots.rules.Get(t.Context(), "closed", func(context.Context) (agentinstructions.Snapshot, error) {
		t.Fatal("closed cache started builder")
		return agentinstructions.Snapshot{}, nil
	}); err == nil {
		t.Fatal("rules cache remains open")
	}
	if _, _, err := rt.pluginStore.Snapshot(t.Context()); err == nil {
		t.Fatal("plugin cache remains open")
	}
}

func TestFilesystemContextScansRespectCancelledRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := scanCommonFilesystemSkillsContext(ctx, t.TempDir(), nil); err == nil {
		t.Fatal("common scan ignored cancellation")
	}
	if _, err := scanWorkspaceFilesystemSkillsContext(ctx, t.TempDir()); err == nil {
		t.Fatal("workspace scan ignored cancellation")
	}
}
