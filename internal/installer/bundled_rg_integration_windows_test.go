//go:build windows && amd64 && bundled_rg_integration

package installer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/uvwt/agentdock/internal/bundledrg"
	"github.com/uvwt/agentdock/internal/updateengine"
)

func requiredRGPayload(t *testing.T, root string) string {
	t.Helper()
	source := os.Getenv("AGENTDOCK_TEST_RG_BUNDLE")
	if source == "" {
		t.Fatal("required real rg fixture is missing; this suite must not skip")
	}
	payload := filepath.Join(root, "payload")
	bundle := filepath.Join(payload, "share", "agentdock", "bin")
	if err := os.MkdirAll(bundle, 0700); err != nil {
		t.Fatal(err)
	}
	for _, file := range bundledrg.Specification().Files {
		data, err := os.ReadFile(filepath.Join(source, file.Path))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(bundle, file.Path), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	// Other components are explicit inert fixtures. This tests generation bytes
	// and journals, not an actual product install or operating-system adapter.
	for _, name := range []string{"agentdock.exe", "agentdock-tray.exe", "agentdock-arbiter.exe"} {
		if err := os.WriteFile(filepath.Join(payload, name), []byte("fixture-original"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(bundle, "manifest.json"), bundledrg.ManifestBytes(), 0600); err != nil {
		t.Fatal(err)
	}
	return payload
}

func TestRequiredRGGenerationPublishUpgradeRepairAndJournalRollback(t *testing.T) {
	root := t.TempDir()
	payload := requiredRGPayload(t, root)
	runtimeRoot := filepath.Join(root, "runtime")
	request := Request{InstallRoot: runtimeRoot, RuntimeRoot: runtimeRoot, PayloadDir: payload, Version: "v1.1.4000", StartService: false, SkipHealth: true}
	original, err := stageWindowsPayload(request, newJournal(runtimeRoot, "rg-original"))
	if err != nil {
		t.Fatal(err)
	}
	if err := commitWindowsActivePointer(runtimeRoot, "rg-original"); err != nil {
		t.Fatal(err)
	}
	assertBundle := func(generation string) {
		t.Helper()
		present, err := bundledrg.VerifyIfPresent(context.Background(), generation)
		if err != nil || !present {
			t.Fatalf("generation lost verified rg: %s %v", generation, err)
		}
	}
	assertBundle(original.GenerationDir)
	upgraded := request
	upgraded.Version = "v1.1.4001"
	upgradeJournal := newJournal(runtimeRoot, "rg-upgrade")
	target, err := stageWindowsPayload(upgraded, upgradeJournal)
	if err != nil {
		t.Fatal(err)
	}
	assertBundle(target.GenerationDir)
	store, err := updateengine.NewStore(runtimeRoot)
	if err != nil {
		t.Fatal(err)
	}
	active, err := store.ReadActive()
	if err != nil {
		t.Fatal(err)
	}
	if active.State != updateengine.StateTrial || active.FallbackVersion != request.Version {
		t.Fatalf("upgrade lost known-good fallback: %+v", active)
	}
	if err := upgradeJournal.Restore(context.Background(), upgraded); err != nil {
		t.Fatal(err)
	}
	active, err = store.ReadActive()
	if err != nil {
		t.Fatal(err)
	}
	if active.State != updateengine.StateCommitted || active.ActiveVersion != request.Version {
		t.Fatalf("rollback did not restore source generation: %+v", active)
	}
	assertBundle(original.GenerationDir)
	if err := os.WriteFile(filepath.Join(payload, "agentdock.exe"), []byte("fixture-repaired"), 0600); err != nil {
		t.Fatal(err)
	}
	repairJournal := newJournal(runtimeRoot, "rg-repair")
	repaired, err := stageWindowsPayload(request, repairJournal)
	if err != nil {
		t.Fatal(err)
	}
	assertBundle(repaired.GenerationDir)
	if err := repairJournal.Restore(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	assertBundle(original.GenerationDir)
	core, err := os.ReadFile(original.Binary)
	if err != nil || string(core) != "fixture-original" {
		t.Fatalf("repair rollback did not restore matching original components: %q %v", core, err)
	}
}

func TestRequiredRGCorruptPayloadCannotReplaceCommittedGeneration(t *testing.T) {
	root := t.TempDir()
	payload := requiredRGPayload(t, root)
	runtimeRoot := filepath.Join(root, "runtime")
	request := Request{InstallRoot: runtimeRoot, RuntimeRoot: runtimeRoot, PayloadDir: payload, Version: "v1.1.4001", StartService: false, SkipHealth: true}
	original, err := stageWindowsPayload(request, newJournal(runtimeRoot, "rg-good"))
	if err != nil {
		t.Fatal(err)
	}
	if err := commitWindowsActivePointer(runtimeRoot, "rg-good"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(payload, "share", "agentdock", "bin", "rg.exe"), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := stageWindowsPayload(request, newJournal(runtimeRoot, "rg-reject")); !errors.Is(err, bundledrg.ErrIntegrity) {
		t.Fatalf("corrupt repair payload was not rejected: %v", err)
	}
	present, err := bundledrg.VerifyIfPresent(context.Background(), original.GenerationDir)
	if err != nil || !present {
		t.Fatalf("corrupt repair damaged the committed bundle: %v", err)
	}
}

func TestRequiredRGLegacyMigrationPreservesSourceSidecar(t *testing.T) {
	for _, relative := range []string{bundledrg.RelativeDir, "tools/rg"} {
		t.Run(relative, func(t *testing.T) {
			root := t.TempDir()
			legacy := filepath.Join(root, "legacy")
			target := filepath.Join(root, "target")
			fixture := os.Getenv("AGENTDOCK_TEST_RG_BUNDLE")
			if fixture == "" {
				t.Fatal("required pinned rg fixture is missing")
			}
			source := filepath.Join(legacy, filepath.FromSlash(relative))
			if err := os.MkdirAll(source, 0700); err != nil {
				t.Fatal(err)
			}
			for _, file := range bundledrg.Specification().Files {
				data, err := os.ReadFile(filepath.Join(fixture, file.Path))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(source, file.Path), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if relative == bundledrg.RelativeDir {
				if err := os.WriteFile(filepath.Join(source, "manifest.json"), bundledrg.ManifestBytes(), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := preserveLegacySearchBundle(t.Context(), legacy, target); err != nil {
				t.Fatal(err)
			}
			copied := filepath.Join(target, filepath.FromSlash(relative))
			verified, err := bundledrg.OpenLegacy(t.Context(), copied)
			if err != nil {
				t.Fatal(err)
			}
			verified.Close()
			if err := os.WriteFile(filepath.Join(source, "rg.exe"), []byte("changed-source"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := preserveLegacySearchBundle(t.Context(), legacy, filepath.Join(root, "rejected")); !errors.Is(err, bundledrg.ErrIntegrity) {
				t.Fatalf("corrupt source was accepted: %v", err)
			}
		})
	}
}
