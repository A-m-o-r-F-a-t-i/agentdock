package scripts

import (
	"encoding/xml"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func source115(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(data), "\r\n", "\n")
}
func TestExecution115MarkupAndSharedThemeContracts(t *testing.T) {
	xaml := source115(t, "desktop/windows/control-panel/ExecutionWindow.xaml")
	decoder := xml.NewDecoder(strings.NewReader(xaml))
	names := map[string]bool{}
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if element, ok := token.(xml.StartElement); ok {
			for _, attr := range element.Attr {
				if attr.Name.Local == "Name" && attr.Name.Space == "http://schemas.microsoft.com/winfx/2006/xaml" {
					if names[attr.Value] {
						t.Fatalf("duplicate XAML name %s", attr.Value)
					}
					names[attr.Value] = true
				}
			}
		}
	}
	for _, name := range []string{"InsertionPanel", "InsertionTextBox", "SendInsertionButton", "StopConversationButton", "InsertButton", "DetailsPanel"} {
		if !names[name] {
			t.Fatalf("missing control %s", name)
		}
	}
	if strings.Contains(xaml, "ObjectMore_Click") || strings.Contains(xaml, "RowMore") {
		t.Fatal("per-conversation management button remains")
	}
	sidebar := source115(t, "desktop/windows/control-panel/ExecutionWindow.Sidebar.cs")
	if strings.Contains(sidebar, "Objects.Clear()") || !strings.Contains(sidebar, "_sidebarLimits.Remove(key.Id)") {
		t.Fatal("sidebar loses identity or expansion reset")
	}
	theme := source115(t, "desktop/windows/control-panel/MainWindow.Capabilities.cs")
	if strings.Contains(theme, "Brushes.White") || strings.Contains(theme, "Color.FromRgb") {
		t.Fatal("capability cards have fixed colors")
	}
	if !strings.Contains(theme, "BackgroundResource = \"PanelBackground\"") {
		t.Fatal("missing shared live card surface")
	}
}
func TestExecution115CompileOnlyWorkflowCannotRunInstallationProbes(t *testing.T) {
	workflow := source115(t, ".github/workflows/windows-package.yml")
	for _, value := range []string{"installation_tests:", "inputs.installation_tests == true", "-StaticOnly:$staticOnly", "not_run_user_requested", "Desktop pure-policy regression", "verification-scope.json"} {
		if !strings.Contains(workflow, value) {
			t.Fatalf("missing compile-only gate %s", value)
		}
	}
	script := source115(t, "scripts/test/test-install-windows.ps1")
	for _, value := range []string{"[switch] $StaticOnly", "if (-not $StaticOnly)", "if (-not $StaticOnly) { & $runValueProbe }"} {
		if !strings.Contains(script, value) {
			t.Fatalf("runtime probe is not gated: %s", value)
		}
	}
}
