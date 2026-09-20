package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

var executionReleaseChecks = []string{
	"backend_full_regression", "static_analysis", "desktop_window_regression",
	"large_scale_backend_and_desktop", "windows_x64_candidate", "windows_arm64_candidate",
	"install_upgrade_and_repair", "rollback_preserves_data_and_permission_intent",
	"real_chatgpt_metadata", "real_chatgpt_reconnect", "production_configuration_preserved",
}

type releaseAcceptance struct {
	Version      string            `json:"version"`
	ReleaseReady bool              `json:"release_ready"`
	Checks       map[string]bool   `json:"checks"`
	Evidence     map[string]string `json:"evidence"`
}

func validateAcceptance(data []byte, version string) error {
	var report releaseAcceptance
	if err := json.Unmarshal(data, &report); err != nil {
		return fmt.Errorf("invalid acceptance report: %w", err)
	}
	if report.Version != version {
		return errors.New("acceptance report version does not match the candidate")
	}
	var missing []string
	for _, check := range executionReleaseChecks {
		if !report.Checks[check] || strings.TrimSpace(report.Evidence[check]) == "" {
			missing = append(missing, check)
		}
	}
	if !report.ReleaseReady || len(missing) > 0 {
		return fmt.Errorf("formal release blocked; release_ready=%t; unresolved acceptance: %s", report.ReleaseReady, strings.Join(missing, ", "))
	}
	return nil
}

func verifyAcceptance(path, version string) error {
	// Earlier release lines retain their existing acceptance mechanism.
	if version != "1.1.2" {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("formal release acceptance unavailable: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > 128<<10 {
		return errors.New("invalid acceptance report file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return validateAcceptance(data, version)
}
