package scripts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWindowsUpgradeUsesValidatedPayloadAndLaunchReceipt(t *testing.T) {
	read := func(name string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join("..", "..", "scripts", "install", name))
		if err != nil {
			t.Fatal(err)
		}
		return strings.ReplaceAll(string(data), "\r\n", "\n")
	}
	installer, broker := read("install.ps1"), read("launch-windows-process.ps1")
	for _, required := range []string{
		"-AgentDockBinary $sourceBinary", "-WaitForExit -PassThruOutput -TimeoutSeconds 300",
		"ConvertTo-NativeArguments -Values $engineArgs",
		"$activationTrayBinary = Join-Path $generationBootstrapDirectory 'agentdock-tray.exe'",
	} {
		if !strings.Contains(installer, required) {
			t.Fatalf("installer missing %q", required)
		}
	}
	if strings.Contains(installer[:strings.Index(installer, "function Get-AgentDockArchitecture")], "-AgentDockBinary $destinationBinary") {
		t.Fatal("trial and rollback must not invoke the stable/old core as the task broker")
	}
	for _, required := range []string{
		"[IO.File]::WriteAllText($startedPath, [string]$process.Id)",
		"$hasRun = Test-Path -LiteralPath $startedPath -PathType Leaf",
		"StandardOutputEncoding = [Text.Encoding]::UTF8", "Stop-ScheduledTask",
	} {
		if !strings.Contains(broker, required) {
			t.Fatalf("broker missing %q", required)
		}
	}
	if strings.Contains(broker, "$info.LastRunTime -ge") {
		t.Fatal("launch acknowledgement must not depend on wall-clock equality")
	}
}
