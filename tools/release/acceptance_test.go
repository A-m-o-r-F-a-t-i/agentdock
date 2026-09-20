package main

import (
	"encoding/json"
	"testing"
)

func TestExecutionAcceptanceRequiresEveryVerifiedGate(t *testing.T) {
	report := releaseAcceptance{Version: "1.1.2", ReleaseReady: true, Checks: map[string]bool{}, Evidence: map[string]string{}}
	for _, key := range executionReleaseChecks {
		report.Checks[key] = true
		report.Evidence[key] = "isolated test evidence"
	}
	data, _ := json.Marshal(report)
	if err := validateAcceptance(data, "1.1.2"); err != nil {
		t.Fatal(err)
	}
	for _, key := range executionReleaseChecks {
		report.Checks[key] = false
		data, _ = json.Marshal(report)
		if err := validateAcceptance(data, "1.1.2"); err == nil {
			t.Fatalf("missing %s gate was accepted", key)
		}
		report.Checks[key] = true
		report.Evidence[key] = ""
		data, _ = json.Marshal(report)
		if err := validateAcceptance(data, "1.1.2"); err == nil {
			t.Fatalf("missing %s evidence was accepted", key)
		}
		report.Evidence[key] = "isolated test evidence"
	}
	report.ReleaseReady = false
	data, _ = json.Marshal(report)
	if err := validateAcceptance(data, "1.1.2"); err == nil {
		t.Fatal("closed gate accepted")
	}
	report.ReleaseReady = true
	data, _ = json.Marshal(report)
	if err := validateAcceptance(data, "1.1.1"); err == nil {
		t.Fatal("wrong version accepted")
	}
	if err := verifyAcceptance(t.TempDir()+"/missing.json", "1.1.2"); err == nil {
		t.Fatal("missing report accepted")
	}
	if err := verifyAcceptance(t.TempDir()+"/missing.json", "1.1.1"); err != nil {
		t.Fatal("earlier version's existing release rules changed")
	}
}
