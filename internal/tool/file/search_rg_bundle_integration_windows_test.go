//go:build windows && amd64 && bundled_rg_integration

package file

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/uvwt/agentdock/internal/bundledrg"
)

func realRGGeneration(t *testing.T, parent, version string) string {
	t.Helper()
	source := os.Getenv("AGENTDOCK_TEST_RG_BUNDLE")
	if source == "" {
		t.Fatal("required real bundled ripgrep suite has no fixture; skipping is forbidden")
	}
	generation := filepath.Join(parent, "versions", version)
	root := filepath.Join(generation, "share", "agentdock", "bin")
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
	return filepath.Join(generation, "agentdock-core.exe")
}

func TestRequiredBundledRGWinsOverConflictingPATHAndTracksRunningGeneration(t *testing.T) {
	root := t.TempDir()
	current := realRGGeneration(t, root, "v1.1.4001")
	previous := realRGGeneration(t, root, "v1.1.4000")
	// The active pointer may already have changed while an older process exits.
	if err := os.WriteFile(filepath.Join(root, "active-version.json"), []byte(`{"active_version":"v1.1.4001"}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, executable := range []string{current, previous} {
		selection, err := selectRGForExecutable(context.Background(), executable, "windows", "amd64", func(string) (string, error) {
			t.Fatal("PATH took precedence over the bundled tool")
			return "", exec.ErrNotFound
		})
		if err != nil {
			t.Fatal(err)
		}
		if selection.path != filepath.Join(filepath.Dir(executable), "share", "agentdock", "bin", "rg.exe") || selection.source != "bundled" || selection.version != "15.2.0" {
			t.Fatalf("generation selection: %+v", selection)
		}
		selection.close()
	}
}

func TestRequiredBundledRGActualSearchCases(t *testing.T) {
	svc, root := newCodeToolsRuntime(t)
	executable := realRGGeneration(t, t.TempDir(), "v1.1.4001")
	// Neither selection nor execution relies on the developer's PATH.
	t.Setenv("PATH", t.TempDir())
	config := filepath.Join(t.TempDir(), "rg.conf")
	if err := os.WriteFile(config, []byte("--invert-match\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RIPGREP_CONFIG_PATH", config)
	selection, err := selectRGForExecutable(context.Background(), executable, "windows", "amd64", exec.LookPath)
	if err != nil {
		t.Fatal(err)
	}
	defer selection.close()
	directory := filepath.Join(root, "한글 경로 with spaces")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(directory, "-sample file.txt")
	for _, query := range []string{"--help", "-needle", "--", "--files", "--한글", "ordinary needle"} {
		for _, regex := range []bool{false, true} {
			if err := os.WriteFile(file, []byte("before\n"+query+"\nafter\n"), 0600); err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{directory, file} {
				resolved, err := svc.ws.ResolveExisting(path)
				if err != nil {
					t.Fatal(err)
				}
				opts := SearchOptions{Query: query, Regex: regex, CaseSensitive: true, IncludeIgnored: true, IncludeHidden: true, MaxResults: 10}
				result, available, err := svc.searchTextSelectedRG(context.Background(), resolved, opts, selection)
				if err != nil || !available || result["engine_source"] != "bundled" || result["engine_version"] != "15.2.0" || result["total_matches"] != 1 {
					t.Fatalf("real bundled query %q: %#v %v", query, result, err)
				}
				matches := result["matches"].([]map[string]any)
				expected, _ := svc.ws.Relative(file)
				if matches[0]["path"] != expected || matches[0]["line"] != 2 || matches[0]["column"] != 1 || matches[0]["match_text"] != query {
					t.Fatalf("config/path/position corrupted: %#v", matches)
				}
			}
		}
	}
	resolved, err := svc.ws.ResolveExisting(file)
	if err != nil {
		t.Fatal(err)
	}
	result, available, err := svc.searchTextSelectedRG(context.Background(), resolved, SearchOptions{Query: "absent", MaxResults: 10}, selection)
	if err != nil || !available || result["total_matches"] != 0 || result["engine_source"] != "bundled" {
		t.Fatalf("exit 1: %#v %v", result, err)
	}
	_, available, err = svc.searchTextSelectedRG(context.Background(), resolved, SearchOptions{Query: "[", Regex: true, MaxResults: 10}, selection)
	var toolErr *ToolError
	if !available || !errors.As(err, &toolErr) || toolErr.Code != "SEARCH_FAILED" {
		t.Fatalf("regex error became fallback/success: %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = svc.searchTextSelectedRG(cancelled, resolved, SearchOptions{Query: "needle"}, selection)
	if !errors.As(err, &toolErr) || toolErr.Code != "SEARCH_CANCELED" {
		t.Fatalf("cancel: %v", err)
	}
	expired, finish := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer finish()
	_, _, err = svc.searchTextSelectedRG(expired, resolved, SearchOptions{Query: "needle"}, selection)
	if !errors.As(err, &toolErr) || toolErr.Code != "RESOURCE_LIMIT" {
		t.Fatalf("deadline: %v", err)
	}
}

func TestRequiredLegacyBundleNeverOverridesCanonicalIntegrity(t *testing.T) {
	executable := realRGGeneration(t, t.TempDir(), "v1.1.6")
	canonical := filepath.Join(filepath.Dir(executable), filepath.FromSlash(bundledrg.RelativeDir))
	legacy := filepath.Join(filepath.Dir(executable), "tools", "rg")
	if err := os.MkdirAll(filepath.Dir(legacy), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(canonical, legacy); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(legacy, "manifest.json")); err != nil {
		t.Fatal(err)
	}
	lookup := func(string) (string, error) {
		t.Fatal("compatibility bundle lost to PATH")
		return "", exec.ErrNotFound
	}
	selected, err := selectRGForExecutable(context.Background(), executable, "windows", "amd64", lookup)
	if err != nil || selected.source != "compatibility_bundled" {
		t.Fatalf("legacy: %+v %v", selected, err)
	}
	selected.close()
	if err := os.MkdirAll(canonical, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := selectRGForExecutable(context.Background(), executable, "windows", "amd64", lookup); !errors.Is(err, bundledrg.ErrIntegrity) {
		t.Fatalf("corrupt canonical fell back: %v", err)
	}
}
