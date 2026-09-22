package scripts

import (
	"os"
	"strings"
	"testing"
)

func TestElevatedStartupUsesGUIBridge(t *testing.T) {
	for _, check := range []struct{ path, required, forbidden string }{
		{"../../desktop/windows/control-panel/Services/TaskAdminService.cs", "--run-core-task --runtime-root", "action.Arguments = $\"service launch-core"},
		{"../../desktop/windows/control-panel/Services/RuntimeService.cs", "\"--launcher-path\", stableTrayEntry", "\"--launcher-path\", stableCoreEntry"},
		{"../install/install.ps1", "-LauncherPath $destinationTrayBinary", "-LauncherPath $destinationBinary"},
	} {
		data, err := os.ReadFile(check.path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), check.required) || strings.Contains(string(data), check.forbidden) {
			t.Fatalf("GUI task bridge contract failed: %s", check.path)
		}
	}
}
