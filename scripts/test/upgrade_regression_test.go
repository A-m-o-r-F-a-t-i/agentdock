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
		"agentdock-tray-shim.exe", "--setup-launch",
		"StandardOutputEncoding = $utf8", "CreateNoWindow = $true",
	} {
		if !strings.Contains(broker, required) {
			t.Fatalf("broker missing %q", required)
		}
	}
	if strings.Contains(broker, "$info.LastRunTime -ge") {
		t.Fatal("launch acknowledgement must not depend on wall-clock equality")
	}
	nativeData, err := os.ReadFile(filepath.Join("..", "..", "internal", "desktopruntime", "setup_launcher_windows.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"result.PID = command.Process.Pid", "result.TaskName != request.TaskName",
		"writeSetupJSON(filepath.Join(root, \"result.json\"), result)",
		"deleteSetupTask(request.TaskName", "command.Wait()",
	} {
		if !strings.Contains(string(nativeData), required) {
			t.Fatalf("native launch receipt missing %q", required)
		}
	}
}
