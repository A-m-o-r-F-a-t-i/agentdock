//go:build windows

package desktopruntime

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestTailscaleExecutableDiscovery(t *testing.T) {
	root := t.TempDir()
	saved := filepath.Join(root, "saved.exe")
	pathBinary := filepath.Join(root, "path.exe")
	defaultBinary := filepath.Join(root, "Tailscale", "tailscale.exe")
	if err := os.MkdirAll(filepath.Dir(defaultBinary), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{saved, pathBinary, defaultBinary} {
		if err := os.WriteFile(name, []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	pathLookup := func(string) (string, error) { return pathBinary, nil }
	missing := func(string) (string, error) { return "", errors.New("not found") }
	for _, tc := range []struct {
		saved  string
		lookup func(string) (string, error)
		want   string
	}{{saved, pathLookup, saved}, {"", pathLookup, pathBinary}, {"", missing, defaultBinary}} {
		got, err := discoverTailscaleBinary(tc.saved, tc.lookup, root)
		if err != nil || got != tc.want {
			t.Fatalf("got %q, want %q: %v", got, tc.want, err)
		}
	}
	if _, err := discoverTailscaleBinary(root, pathLookup, root); tailscaleDiagnosticCode(err) != "invalid_binary" {
		t.Fatal(err)
	}
	if _, err := discoverTailscaleBinary("relative.exe", pathLookup, root); err == nil {
		t.Fatal("relative binary accepted")
	}
	if _, err := discoverTailscaleBinary("", missing, t.TempDir()); tailscaleDiagnosticCode(err) != "not_installed" {
		t.Fatal(err)
	}
}

func TestTailscaleCommandBoundary(t *testing.T) {
	allowed := [][]string{{"status", "--json", "--peers=false"}, {"funnel", "status", "--json"}, {"funnel", "--bg", "--yes", "--https=443", "--set-path=/", "http://127.0.0.1:8765"}, {"funnel", "--bg", "--yes", "--https=443", "--set-path=/", "off"}, {"serve", "--bg", "--yes", "--https=443", "--set-path=/mcp", "http://127.0.0.1:8765/mcp"}}
	for _, args := range allowed {
		if !allowedTailscaleCommand(args) {
			t.Errorf("rejected %v", args)
		}
	}
	for _, args := range [][]string{{"down"}, {"logout"}, {"funnel", "reset"}, {"serve", "reset"}, {"funnel", "set-raw"}, {"funnel", "--https=443", "off"}, {"funnel", "--bg", "--yes", "--https=8443", "--set-path=/", "off"}, {"funnel", "--bg", "--yes", "--https=443", "--set-path=/other", "off"}, {"funnel", "--bg", "--yes", "--https=443", "--set-path=/", "http://example.com"}, {"funnel", "--bg", "--yes", "--https=443", "--set-path=/", "http://127.0.0.1:8765/mcp"}} {
		if allowedTailscaleCommand(args) {
			t.Errorf("accepted unsafe arguments: %v", args)
		}
	}
}

func TestTailscaleOutputBound(t *testing.T) {
	output := &tailscaleOutputBuffer{limit: 16}
	for range 10 {
		n, err := output.Write(bytes.Repeat([]byte("x"), 100))
		if err != nil || n != 100 {
			t.Fatal("child output was not drained")
		}
	}
	if output.buffer.Len() != 16 || !output.overflow {
		t.Fatal("output was not bounded")
	}
}
