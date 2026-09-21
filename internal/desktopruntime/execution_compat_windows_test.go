//go:build windows

package desktopruntime

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/uvwt/agentdock/internal/executioncompat"
)

func TestManagedRuntimePolicyCompatibility(t *testing.T) {
	home := t.TempDir()
	calls := 0
	probe := func() (int, error) { calls++; return 0, nil }
	if err := checkExecutionCompatibility(home, probe); err != nil || calls != 0 {
		t.Fatal("unused home should not probe a legacy binary")
	}
	path := filepath.Join(home, "execution", "permissions", "policy.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"schema_version":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := checkExecutionCompatibility(home, probe); !errors.Is(err, executioncompat.ErrUnsupported) || calls != 1 {
		t.Fatalf("legacy accepted: %v", err)
	}
	if err := checkExecutionCompatibility(home, func() (int, error) { return executioncompat.PolicyVersion, nil }); err != nil {
		t.Fatal(err)
	}
	if err := checkExecutionCompatibility(home, func() (int, error) { return 0, errors.New("failed probe") }); err == nil {
		t.Fatal("failed probe opened execution")
	}
	buffer := &capabilityOutput{}
	_, _ = buffer.Write(make([]byte, 128<<10))
	if !buffer.overflow || buffer.Len() != 64<<10 {
		t.Fatal("capability output was not bounded")
	}
}

func TestLegacyExecutionPolicyCapabilityIsBoundToReleaseCommit(t *testing.T) {
	if got := resolvedExecutionPolicyVersion(executionCapability{
		Version: "1.1.2",
		Commit:  legacyExecutionPolicyCommit,
	}); got != executioncompat.PolicyVersion {
		t.Fatalf("known 1.1.2 release capability = %d", got)
	}
	if got := resolvedExecutionPolicyVersion(executionCapability{
		Version: "1.1.2",
		Commit:  "different",
	}); got != 0 {
		t.Fatalf("unknown 1.1.2 build capability = %d", got)
	}
	if got := resolvedExecutionPolicyVersion(executionCapability{
		ExecutionPolicyVersion: executioncompat.PolicyVersion,
	}); got != executioncompat.PolicyVersion {
		t.Fatalf("explicit capability = %d", got)
	}
}
