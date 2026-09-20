package app

import (
	"context"
	"os/exec"
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
