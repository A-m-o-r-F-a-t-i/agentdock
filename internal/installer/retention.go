package installer

import (
	"fmt"
	"strings"

	"github.com/uvwt/agentdock/internal/generationretention"
	"github.com/uvwt/agentdock/internal/updateengine"
)

// cleanupCommittedGenerations is deliberately best-effort and runs only after
// a Windows runtime has passed health and reached committed state. Every
// uncertainty becomes a warning; cleanup must never roll back a healthy install.
func cleanupCommittedGenerations(transaction Transaction, result Result) []string {
	if !strings.EqualFold(transaction.Platform, "windows") || !result.Healthy {
		return nil
	}
	root := strings.TrimSpace(transaction.InstallRoot)
	if root == "" {
		return []string{"generation cleanup skipped: committed install has no install root"}
	}
	layout, err := updateengine.NewWindowsLayout(root)
	if err != nil {
		return []string{fmt.Sprintf("generation cleanup skipped: %v", err)}
	}
	policy, err := generationretention.CollectPolicy(root, layout.VersionsDir())
	if err != nil {
		return []string{fmt.Sprintf("generation cleanup skipped: %v", err)}
	}
	report := generationretention.Clean(policy)
	return report.Warnings
}
