//go:build windows && amd64 && bundled_rg_integration

package selfupdate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/uvwt/agentdock/internal/bundledrg"
)

func TestRequiredRGArchiveExtractionPreservesPinnedComponent(t *testing.T) {
	source := os.Getenv("AGENTDOCK_TEST_RG_BUNDLE")
	if source == "" {
		t.Fatal("required real bundle archive test has no fixture; skipping is forbidden")
	}
	files := map[string][]byte{"agentdock-tray.exe": []byte("inert fixture"), "agentdock.ico": []byte("inert icon")}
	for _, expected := range bundledrg.Specification().Files {
		data, err := os.ReadFile(filepath.Join(source, expected.Path))
		if err != nil {
			t.Fatal(err)
		}
		files[bundledrg.RelativeDir+"/"+expected.Path] = data
	}
	files[bundledrg.RelativeDir+"/manifest.json"] = bundledrg.ManifestBytes()
	root, err := extractDesktopUpdateArchive(context.Background(), makeWindowsDesktopArchive(t, files), t.TempDir(), "1.1.4001")
	if err != nil {
		t.Fatal(err)
	}
	present, err := bundledrg.VerifyIfPresent(context.Background(), root)
	if err != nil || !present {
		t.Fatalf("archive omitted/changed rg: %v", err)
	}
	files[bundledrg.RelativeDir+"/rg.exe"] = []byte("tampered")
	if _, err := extractDesktopUpdateArchive(context.Background(), makeWindowsDesktopArchive(t, files), t.TempDir(), "1.1.4001"); !errors.Is(err, bundledrg.ErrIntegrity) {
		t.Fatalf("corrupt rg archive accepted: %v", err)
	}
	delete(files, bundledrg.RelativeDir+"/rg.exe")
	if _, err := extractDesktopUpdateArchive(context.Background(), makeWindowsDesktopArchive(t, files), t.TempDir(), "1.1.4001"); !errors.Is(err, bundledrg.ErrIntegrity) {
		t.Fatalf("partial rg archive accepted: %v", err)
	}
}

func TestRequiredRGLegacyUpdaterDoesNotDiscardGenerationComponent(t *testing.T) {
	source := os.Getenv("AGENTDOCK_TEST_RG_BUNDLE")
	if source == "" {
		t.Fatal("required real rg fixture missing")
	}
	staging := t.TempDir()
	root := filepath.Join(staging, "share", "agentdock", "bin")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	for _, expected := range bundledrg.Specification().Files {
		data, err := os.ReadFile(filepath.Join(source, expected.Path))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, expected.Path), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "manifest.json"), bundledrg.ManifestBytes(), 0600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "bin", "agentdock.exe")
	_, err := applyPlatformUpdate(context.Background(), applyRequest{CurrentPath: target, DesktopStagedPath: staging})
	if err == nil || !strings.Contains(err.Error(), "offline-setup-required") {
		t.Fatalf("legacy updater silently discarded or attempted installation of rg: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(target)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected update mutated target: %v", err)
	}
}
