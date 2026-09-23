package plugin

import (
	"context"
	"fmt"
	"github.com/uvwt/agentdock/internal/fs/filelock"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestHeavySnapshotDoesNotReadMemberDocumentsAndHotReadsDoNotScan(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "plugins", "heavy-test")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "plugin.json"), []byte(`{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"heavy-test","version":"1.0.0","description":"Fixture","extensions":{"io.github.uvwt.agentdock":{"heavy":true}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	for i := range 200 {
		dir := filepath.Join(root, "skills", fmt.Sprintf("skill-%03d", i))
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("not parsed until explicitly selected"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	store, err := New(home)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	d, _, err := store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Skills()) != 200 || d.Metrics.SkillDocuments != 0 || d.Metrics.PluginScans != 1 {
		t.Fatalf("eager Heavy reads: %#v skills=%d", d.Metrics, len(d.Skills()))
	}
	before := store.SnapshotStats().Builds
	for range 100 {
		if _, ok, err := store.SkillMembership("skill-050"); err != nil || !ok {
			t.Fatalf("membership: %v %t", err, ok)
		}
	}
	if store.SnapshotStats().Builds != before {
		t.Fatalf("hot lookup rescanned: %#v", store.SnapshotStats())
	}
}
func TestTenColdPluginSnapshotsShareOneBuild(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.Invalidate()
	release, err := filelock.Acquire(t.Context(), store.lockPath)
	if err != nil {
		t.Fatal(err)
	}
	before := store.SnapshotStats()
	var wg sync.WaitGroup
	errors := make(chan error, 10)
	for range 10 {
		wg.Add(1)
		go func() { defer wg.Done(); _, _, err := store.Snapshot(t.Context()); errors <- err }()
	}
	deadline := time.Now().Add(time.Second)
	for store.SnapshotStats().Shared-before.Shared < 9 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	release()
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	after := store.SnapshotStats()
	if after.Builds-before.Builds != 1 {
		t.Fatalf("concurrent rebuilds: before=%#v after=%#v", before, after)
	}
}
func TestPluginLockWaitUsesCallerDeadlineAndStops(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.Invalidate()
	release, err := filelock.Acquire(t.Context(), store.lockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, _, err := store.Snapshot(ctx); err == nil {
		t.Fatal("blocked lock reported success")
	}
	if time.Since(start) > time.Second {
		t.Fatal("caller deadline did not bound lock wait")
	}
	waitCtx, waitCancel := context.WithTimeout(t.Context(), time.Second)
	defer waitCancel()
	if err := store.snapshots.cache.Wait(waitCtx); err != nil {
		t.Fatalf("cancelled builder kept waiting: %v", err)
	}
}
