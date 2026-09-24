//go:build windows

package activity

import (
	"golang.org/x/sys/windows"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPayloadWindowsShortPathIsAnAliasNotALink(t *testing.T) {
	root := filepath.Join(t.TempDir(), "long-payload-path-for-alias-test")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	pointer, err := windows.UTF16PtrFromString(root)
	if err != nil {
		t.Fatal(err)
	}
	buffer := make([]uint16, 32768)
	count, err := windows.GetShortPathName(pointer, &buffer[0], uint32(len(buffer)))
	if err != nil || count == 0 || count >= uint32(len(buffer)) {
		t.Skipf("no Windows short path: %v", err)
	}
	short := windows.UTF16ToString(buffer[:count])
	resolved, err := filepath.EvalSymlinks(short)
	if err != nil {
		t.Fatal(err)
	}
	if strings.EqualFold(short, resolved) {
		t.Skip("volume does not expose a short-path alias")
	}
	store, err := New(short, Options{})
	if err != nil {
		t.Fatal(err)
	}
	payload := store.CapturePayload(t.Context(), map[string]any{"text": "short path works"}, "complete", NewRedactor())
	if payload.Ref == "" {
		t.Fatalf("short path rejected: %+v", payload)
	}
	savePayloadCall(t, store, "call_short_path", payload)
	page, err := store.ReadCallPayload(t.Context(), "call_short_path", "response", 0, 32768)
	if err != nil || !strings.Contains(page.Text, "short path works") {
		t.Fatal("short path replay failed", page, err)
	}
}
