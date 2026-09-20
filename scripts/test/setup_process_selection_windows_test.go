//go:build windows

package scripts

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/uvwt/agentdock/internal/process"
)

func TestSetupProcessSelectionPreservesDaemonChecks(t *testing.T) {
	powershell, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Fatal("Windows PowerShell is required to verify the Setup process classifier")
	}
	script, err := filepath.Abs("test-windows-setup-e2e.ps1")
	if err != nil {
		t.Fatal(err)
	}
	// Extract only the pure classifier. Loading the full E2E script would mutate
	// the production scheduled task and is reserved for disposable CI machines.
	program := `$ErrorActionPreference = 'Stop'
$tokens = $null; $parseErrors = $null
$ast = [System.Management.Automation.Language.Parser]::ParseFile('` + strings.ReplaceAll(script, "'", "''") + `', [ref]$tokens, [ref]$parseErrors)
if ($parseErrors.Count -ne 0) { throw 'Setup script has parse errors.' }
$function = $ast.Find({ param($node) $node -is [System.Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -eq 'Test-CoreReadOnlyProbe' }, $true)
if ($null -eq $function) { throw 'Missing process classifier.' }
Invoke-Expression $function.Extent.Text
$cases = @(
    @{Line='"C:\Agent Dock\agentdock-core.exe" version --json'; Probe=$true},
    @{Line='C:\AgentDock\agentdock-core.exe version'; Probe=$true},
    @{Line='"C:\Agent Dock\agentdock-core.exe" "VERSION" "--json"'; Probe=$true},
    @{Line='"C:\Agent Dock\agentdock-core.exe" service status --runtime-root "C:\Agent Dock"'; Probe=$true},
    @{Line='"C:\Agent Dock\agentdock-core.exe" "service" "status"'; Probe=$true},
    @{Line='"C:\Agent Dock\agentdock-core.exe" service launch-core --runtime-root "C:\Agent Dock"'; Probe=$false},
    @{Line='"C:\Agent Dock\agentdock-core.exe"'; Probe=$false},
    @{Line='agentdock-core.exe serve'; Probe=$false},
    @{Line='agentdock-core.exe service statusjunk'; Probe=$false},
    @{Line='agentdock-core.exe version unexpected'; Probe=$false},
    @{Line='"C:\service status\agentdock-core.exe" service launch-core'; Probe=$false},
    @{Line=''; Probe=$false},
    @{Line=$null; Probe=$false}
)
foreach ($case in $cases) {
    if ((Test-CoreReadOnlyProbe -CommandLine $case.Line) -ne $case.Probe) { throw ('Wrong probe classification: ' + $case.Line) }
}
$processes = @(
    'agentdock-core.exe service launch-core --runtime-root C:\AgentDock',
    'agentdock-core.exe service launch-core --runtime-root C:\AgentDock',
    'agentdock-core.exe version --json',
    'agentdock-core.exe service status --runtime-root C:\AgentDock'
)
$daemons = @($processes | Where-Object { -not (Test-CoreReadOnlyProbe -CommandLine $_) })
if ($daemons.Count -ne 2) { throw 'Duplicate daemons were concealed by the probe classifier.' }
'Process classification passed; duplicate daemons remain observable.'
`
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, powershell, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", program)
	process.Configure(cmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Setup process classifier: %v\n%s", err, output)
	}
}
