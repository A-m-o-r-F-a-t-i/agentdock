package scripts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readWorkflow(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", name))
	if err != nil {
		t.Fatalf("read workflow %s: %v", name, err)
	}
	return strings.ReplaceAll(string(data), "\r\n", "\n")
}

func TestWorkflowsUseCurrentActionMajors(t *testing.T) {
	expected := map[string]string{
		"uses: actions/checkout@":               "uses: actions/checkout@v5",
		"uses: actions/setup-go@":               "uses: actions/setup-go@v6",
		"uses: actions/setup-dotnet@":           "uses: actions/setup-dotnet@v5",
		"uses: actions/upload-artifact@":        "uses: actions/upload-artifact@v7",
		"uses: actions/download-artifact@":      "uses: actions/download-artifact@v8",
		"uses: github/codeql-action/init@":      "uses: github/codeql-action/init@v4",
		"uses: github/codeql-action/autobuild@": "uses: github/codeql-action/autobuild@v4",
		"uses: github/codeql-action/analyze@":   "uses: github/codeql-action/analyze@v4",
	}
	foundManagedAction := false
	for _, name := range []string{"ci.yml", "codeql.yml", "release.yml", "windows-installer.yml", "windows-package.yml", "windows-release.yml"} {
		workflow := readWorkflow(t, name)
		for _, line := range strings.Split(workflow, "\n") {
			trimmed := strings.TrimSpace(line)
			for prefix, want := range expected {
				if !strings.HasPrefix(trimmed, prefix) {
					continue
				}
				foundManagedAction = true
				if trimmed != want {
					t.Fatalf("workflow %s must use %q, got %q", name, want, trimmed)
				}
			}
		}
	}
	if !foundManagedAction {
		t.Fatal("expected workflows to use managed GitHub Actions")
	}
}

func TestCIWorkflowUsesFreshBoundedGoTests(t *testing.T) {
	workflow := readWorkflow(t, "ci.yml")
	for _, want := range []string{
		"timeout-minutes: 20",
		"go test -p 2 ./... -count=1 -timeout=8m",
		"name: ACP prompt and steering race regression",
		"-count=20",
		"-timeout=90s",
		"go test -race -p 2 ./internal/activity ./internal/permission ./internal/insertion ./internal/fs/... ./internal/snapshot ./internal/acp ./internal/selfupdate -count=1 -timeout=8m",
		"go test -race -tags browser_integration ./internal/tool/browser ./internal/app -count=1 -timeout=3m",
		"timeout-minutes: 15",
	} {
		if !strings.Contains(workflow, want) {
			t.Fatalf("CI workflow must keep bounded non-cached validation; missing %q", want)
		}
	}
}

func TestWindowsInstallerWorkflowHasAlwaysPresentPullRequestGate(t *testing.T) {
	workflow := readWorkflow(t, "windows-installer.yml")
	for _, want := range []string{
		"pull_request:\n    branches:\n      - main",
		"name: Detect Windows installer changes",
		"fetch-depth: 0",
		"git diff --name-only \"$BASE_SHA\" \"$HEAD_SHA\"",
		"name: Validate installer on Windows PowerShell 5.1",
		"needs: changes",
		"if: needs.changes.outputs.relevant == 'true'",
		"timeout-minutes: 30",
		"name: Windows Installer gate",
		"needs: [changes, validate]",
		"if: always()",
		"CHANGES_RESULT: ${{ needs.changes.result }}",
		"VALIDATE_RESULT: ${{ needs.validate.result }}",
		"github.event_name == 'workflow_dispatch' && inputs.test_tag != ''",
		"-InstallerPath .\\scripts\\install\\install.ps1",
		"name: Download and verify cloudflared compatibility payload",
		"for ($attempt = 1; $attempt -le 5; $attempt++)",
		"Get-AuthenticodeSignature -LiteralPath $cloudflaredPath",
	} {
		if !strings.Contains(workflow, want) {
			t.Fatalf("Windows Installer workflow must keep a safe pull-request gate; missing %q", want)
		}
	}
	if strings.Contains(workflow, "raw.githubusercontent.com/${{ github.repository }}/${{ github.sha }}/scripts/install/install.ps1") {
		t.Fatal("routine Windows installer validation must use the checked-out installer instead of refetching it over the network")
	}
}

func TestWindowsReleaseKeepsBoundedCompleteValidation(t *testing.T) {
	workflow := readWorkflow(t, "windows-release.yml")
	for _, want := range []string{
		"go test -json -p 2 ./... -count=1 -timeout=8m",
		"name: Backend full regression",
		"name: Static analysis",
		"name: Windows installer contracts",
		"-ExpectedVersion $expectedVersion",
		"execution-go.jsonl",
		"name: Execution policy compatibility with actual 1.1.1 Core",
		"7505e8044c6daa78ed73c603a2dd9ff214883d82",
		"test-windows-execution-compatibility.ps1",
		"execution-compatibility/result.json",
		"throw 'Go tests failed.'",
		"go vet ./...",
		"needs: [windows, windows-arm64]",
		"needs: [windows, windows-arm64, windows-arm64-install]",
		"name: Native Windows ARM64 installation",
		"arm64-install-result.json",
		"runs-on: windows-11-vs2026-arm",
		"-RuntimeIdentifier win-arm64",
		"github.ref == 'refs/heads/main' && inputs.publish",
	} {
		if !strings.Contains(workflow, want) {
			t.Fatalf("Windows release must retain complete bounded validation: missing %q", want)
		}
	}
}

func TestWindowsPackageOwnsAutomaticVersionTagRelease(t *testing.T) {
	workflow := readWorkflow(t, "windows-package.yml")
	for _, want := range []string{
		"push:\n    tags:\n      - 'v*'",
		"workflow_dispatch:",
		"name: Build verified unsigned Windows x64 package",
		"Architectures = @('amd64')",
		"build-windows-release.ps1",
		"verify-windows-release-assets.ps1",
		"name: Verify offline Setup installation and uninstall",
		"test-windows-setup-e2e.ps1",
		"-AllowLegacyTaskMutation",
		"actions/upload-artifact@v7",
		"actions/download-artifact@v8",
		"gh release create",
		"gh release upload",
		"--clobber",
		"docs/releases/$tag.md",
		"ExpectedChannel release",
	} {
		if !strings.Contains(workflow, want) {
			t.Fatalf("Windows package workflow is missing %q", want)
		}
	}

	crossPlatform := readWorkflow(t, "release.yml")
	if strings.Contains(crossPlatform, "push:\n    tags:") {
		t.Fatal("cross-platform signed release must remain manual; windows-package.yml owns automatic version tags")
	}
}

func TestFunnelFixtureRequiresExplicitTargetVersion(t *testing.T) {
	source, err := os.ReadFile("test-windows-tailscale-funnel-lifecycle.ps1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(source), "[Parameter(Mandatory=$true)][string] $ExpectedVersion") {
		t.Fatal("Funnel lifecycle must not silently inherit an older hardcoded target version")
	}
}
