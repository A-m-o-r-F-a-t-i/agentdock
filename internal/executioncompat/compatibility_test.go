package executioncompat

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSavedIntentRequiresExplicitCapability(t *testing.T) {
	for _, file := range []string{"policy.json", "approvals/approval_fixture.json"} {
		t.Run(file, func(t *testing.T) {
			home := t.TempDir()
			if required, err := RequiredVersion(home); err != nil || required != 0 {
				t.Fatalf("unused home: %d %v", required, err)
			}
			path := filepath.Join(home, "execution", "permissions", filepath.FromSlash(file))
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			original := []byte("{\"fixture\":\"preserve even damaged metadata\"")
			if err := os.WriteFile(path, original, 0600); err != nil {
				t.Fatal(err)
			}
			required, err := RequiredVersion(home)
			if err != nil || required != PolicyVersion {
				t.Fatalf("intent detection: %d %v", required, err)
			}
			if !errors.Is(Validate(required, 0), ErrUnsupported) {
				t.Fatal("legacy runtime accepted saved intent")
			}
			if err := Validate(required, PolicyVersion); err != nil {
				t.Fatal(err)
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != string(original) {
				t.Fatal("compatibility check changed policy bytes")
			}
		})
	}
}

func TestPolicyCheckRejectsAmbiguousDataPaths(t *testing.T) {
	if _, err := RequiredVersion("relative"); err == nil {
		t.Fatal("relative home accepted")
	}
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "execution"), []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := RequiredVersion(home); err == nil {
		t.Fatal("invalid policy directory accepted")
	}
}
