package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/uvwt/agentdock/internal/activity"
)

func TestActivityGitFailureDiagnosticIsBoundedAndRedacted(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	r := newRuntimeValidationTestRuntime(t)
	record, _ := workspaceTask(t, r)
	secret := "fixture-private-diagnostic-7291"
	t.Setenv("DIAGNOSTIC_SECRET", secret)
	_, code, _, err := r.runActivityGit(context.Background(), record, activity.Event{}, []string{"--unsupported-" + secret})
	if err == nil || code == 0 || strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), "git read failed") {
		t.Fatalf("unsafe or missing diagnostic: %v", err)
	}
	buffer := &diffBuffer{}
	_, _ = buffer.Write([]byte("complete\n" + strings.Repeat("x", maxActivityDiffBytes)))
	if !buffer.truncated || completeDiffOutput(buffer) != "complete\n" {
		t.Fatal("truncated diagnostic retained a partial last line")
	}
}

func TestActivityGitIgnoresInheritedGlobalConfigAndCleansTemporaryFiles(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	r := newRuntimeValidationTestRuntime(t)
	record, _ := workspaceTask(t, r)
	root := t.TempDir()
	broken := filepath.Join(root, "broken-global-config")
	if err := os.WriteFile(broken, []byte("invalid untrusted global config ["), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", broken)
	t.Setenv("TMP", root)
	t.Setenv("TEMP", root)
	t.Setenv("TMPDIR", root)
	output, code, _, err := r.runActivityGit(context.Background(), record, activity.Event{}, []string{"config", "--global", "--list"})
	if err != nil || code != 0 || strings.TrimSpace(output) != "" {
		t.Fatalf("Git did not receive a real empty config: %q %d %v", output, code, err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 || entries[0].Name() != "broken-global-config" {
		t.Fatalf("temporary Git files leaked: %+v %v", entries, err)
	}
}
