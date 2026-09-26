package generationretention

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestCleanKeepsActiveFallbackAndTransactionReferences(t *testing.T) {
	versions := filepath.Join(t.TempDir(), "versions")
	makeGenerations(t, versions, "v1.0.0", "v2.0.0", "v3.0.0", "v4.0.0", "v5.0.0")

	report := Clean(Policy{
		VersionsDir:       versions,
		ActiveVersion:     "v5.0.0",
		FallbackVersion:   "v4.0.0",
		ProtectedVersions: []string{"v2.0.0", "v3.0.0"},
	})
	if len(report.Warnings) != 0 {
		t.Fatalf("warnings=%v", report.Warnings)
	}
	if len(report.Removed) != 1 || report.Removed[0].Version != "v1.0.0" || report.Removed[0].Error != "" {
		t.Fatalf("removed=%+v", report.Removed)
	}
	for _, version := range []string{"v2.0.0", "v3.0.0", "v4.0.0", "v5.0.0"} {
		assertDirectory(t, filepath.Join(versions, version), true)
	}
	assertDirectory(t, filepath.Join(versions, "v1.0.0"), false)
}

func TestCleanFreshInstallKeepsOnlyActiveGeneration(t *testing.T) {
	versions := filepath.Join(t.TempDir(), "versions")
	makeGenerations(t, versions, "v0.8.0", "v0.9.0", "v1.0.0")

	report := Clean(Policy{VersionsDir: versions, ActiveVersion: "1.0.0"})
	if len(report.Warnings) != 0 {
		t.Fatalf("warnings=%v", report.Warnings)
	}
	if len(report.Removed) != 2 {
		t.Fatalf("removed=%+v", report.Removed)
	}
	assertDirectory(t, filepath.Join(versions, "v1.0.0"), true)
	assertDirectory(t, filepath.Join(versions, "v0.8.0"), false)
	assertDirectory(t, filepath.Join(versions, "v0.9.0"), false)
}

func TestCleanDeletionFailureIsWarningAndRetryable(t *testing.T) {
	versions := filepath.Join(t.TempDir(), "versions")
	makeGenerations(t, versions, "v1.0.0", "v2.0.0")
	denied := errors.New("sharing violation")

	report := clean(Policy{VersionsDir: versions, ActiveVersion: "v2.0.0"}, func(path string) error {
		if filepath.Base(path) == "v1.0.0" {
			return denied
		}
		return os.RemoveAll(path)
	})
	if len(report.Removed) != 1 || !strings.Contains(report.Removed[0].Error, denied.Error()) || len(report.Warnings) != 1 {
		t.Fatalf("report=%+v", report)
	}
	assertDirectory(t, filepath.Join(versions, "v1.0.0"), true)

	retry := Clean(Policy{VersionsDir: versions, ActiveVersion: "v2.0.0"})
	if len(retry.Removed) != 1 || retry.Removed[0].Error != "" {
		t.Fatalf("retry=%+v", retry)
	}
	assertDirectory(t, filepath.Join(versions, "v1.0.0"), false)
}

func TestCollectPolicyProtectsPointerTransactionsAndJournal(t *testing.T) {
	root := t.TempDir()
	versions := filepath.Join(root, "versions")
	makeGenerations(t, versions, "v0.9.0", "v1.0.0", "v2.0.0", "v3.0.0", "v4.0.0", "v5.0.0")
	writeJSONFixture(t, filepath.Join(root, "active-version.json"), `{
		"schema_version": 1,
		"active_version": "v5.0.0",
		"fallback_version": "v4.0.0",
		"state": "committed"
	}`)
	writeJSONFixture(t, filepath.Join(root, "install", "transaction.json"), `{
		"schema_version": 1,
		"transaction_id": "install-pending",
		"source_version": "v2.0.0",
		"target_version": "v3.0.0",
		"active_version": "v5.0.0",
		"fallback_version": "v4.0.0",
		"state": "trial"
	}`)
	writeJSONFixture(t, filepath.Join(root, "install", "rollback", "install-pending", "journal.json"), `{
		"transaction_id": "install-pending",
		"backups": [{"original": "`+filepath.ToSlash(filepath.Join(versions, "v1.0.0"))+`"}]
	}`)
	writeJSONFixture(t, filepath.Join(root, "update", "transaction.json"), `{
		"schema_version": 1,
		"transaction_id": "update-current",
		"source_version": "v4.0.0",
		"target_version": "v5.0.0",
		"active_version": "v5.0.0",
		"fallback_version": "v4.0.0",
		"state": "committed"
	}`)

	policy, err := CollectPolicy(root, versions)
	if err != nil {
		t.Fatal(err)
	}
	if policy.ActiveVersion != "v5.0.0" || policy.FallbackVersion != "v4.0.0" {
		t.Fatalf("policy=%+v", policy)
	}
	for _, version := range []string{"v1.0.0", "v2.0.0", "v3.0.0"} {
		if !slices.Contains(policy.ProtectedVersions, version) {
			t.Fatalf("protected=%v, missing %s", policy.ProtectedVersions, version)
		}
	}

	report := Clean(policy)
	if len(report.Removed) != 1 || report.Removed[0].Version != "v0.9.0" {
		t.Fatalf("report=%+v", report)
	}
}

func TestCollectPolicyRefusesCommittedUpgradeWithoutHealthyFallback(t *testing.T) {
	root := t.TempDir()
	versions := filepath.Join(root, "versions")
	makeGenerations(t, versions, "v1.0.0", "v2.0.0")
	writeJSONFixture(t, filepath.Join(root, "active-version.json"), `{
		"active_version": "v2.0.0",
		"state": "committed"
	}`)
	writeJSONFixture(t, filepath.Join(root, "update", "transaction.json"), `{
		"transaction_id": "upgrade",
		"source_version": "v1.0.0",
		"target_version": "v2.0.0",
		"active_version": "v2.0.0",
		"state": "committed"
	}`)

	_, err := CollectPolicy(root, versions)
	if !errors.Is(err, ErrMissingHealthyFallback) {
		t.Fatalf("error=%v", err)
	}
	assertDirectory(t, filepath.Join(versions, "v1.0.0"), true)
	assertDirectory(t, filepath.Join(versions, "v2.0.0"), true)
}

func TestCollectPolicyRejectsMalformedAuthoritativeState(t *testing.T) {
	root := t.TempDir()
	versions := filepath.Join(root, "versions")
	makeGenerations(t, versions, "v1.0.0")
	writeJSONFixture(t, filepath.Join(root, "active-version.json"), `{not-json`)
	if _, err := CollectPolicy(root, versions); err == nil {
		t.Fatal("malformed active pointer did not block cleanup")
	}
	assertDirectory(t, filepath.Join(versions, "v1.0.0"), true)
}

func makeGenerations(t *testing.T, root string, versions ...string) {
	t.Helper()
	for _, version := range versions {
		if err := os.MkdirAll(filepath.Join(root, version), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, version, "marker"), []byte(version), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func writeJSONFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertDirectory(t *testing.T, path string, exists bool) {
	t.Helper()
	info, err := os.Stat(path)
	if exists {
		if err != nil || !info.IsDir() {
			t.Fatalf("directory %s missing: %v", path, err)
		}
		return
	}
	if !os.IsNotExist(err) {
		t.Fatalf("directory %s still exists or stat failed: %v", path, err)
	}
}
