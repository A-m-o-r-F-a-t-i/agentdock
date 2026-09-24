package installer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/uvwt/agentdock/internal/updateengine"
)

func TestWindowsServiceBinaryTracksActiveGeneration(t *testing.T) {
	root := t.TempDir()
	layout, err := updateengine.NewWindowsLayout(root)
	if err != nil {
		t.Fatal(err)
	}
	payload := filepath.Join(root, "payload")
	for _, path := range []string{
		layout.CoreShim(), filepath.Join(root, "agentdock.exe"), filepath.Join(payload, "agentdock.exe"),
		layout.GenerationCore("v1.1.5"), layout.GenerationCore("v1.1.6"),
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	store, err := updateengine.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	request := Request{InstallRoot: root, RuntimeRoot: root, PayloadDir: payload, Version: "v1.1.6"}
	for _, test := range []struct {
		name, version string
		state         updateengine.State
	}{
		{"snapshot source before activation", "v1.1.5", updateengine.StateCommitted},
		{"start and stop trial target", "v1.1.6", updateengine.StateTrial},
		{"restore source after rollback", "v1.1.5", updateengine.StateCommitted},
		{"committed target", "v1.1.6", updateengine.StateCommitted},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := store.WriteActive(updateengine.ActiveVersion{
				SchemaVersion: updateengine.SchemaVersion, ActiveVersion: test.version,
				State: test.state, TransactionID: "1234567890abcdef1234567890abcdef",
			}); err != nil {
				t.Fatal(err)
			}
			want := layout.GenerationCore(test.version)
			if got := windowsServiceBinary(request); got != want {
				t.Fatalf("installer control binary=%s, want selected generation=%s", got, want)
			}
		})
	}
}

func TestWindowsServiceBinaryDoesNotSelectAnInactiveGeneration(t *testing.T) {
	root := t.TempDir()
	layout, err := updateengine.NewWindowsLayout(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{layout.CoreShim(), layout.GenerationCore("v1.1.6")} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// A generation directory or the requested version alone is not activation.
	request := Request{InstallRoot: root, RuntimeRoot: root, Version: "v1.1.6"}
	if got := windowsServiceBinary(request); got != layout.CoreShim() {
		t.Fatalf("unselected generation was used: %s", got)
	}
}
