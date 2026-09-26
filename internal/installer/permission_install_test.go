package installer

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/uvwt/agentdock/internal/permission"
	"github.com/uvwt/agentdock/internal/updateengine"
)

func freshPermissionRequest(t *testing.T) Request {
	t.Helper()
	root := t.TempDir()
	return Request{Action: ActionInstall, InstallRoot: filepath.Join(root, "opt"),
		RuntimeRoot: filepath.Join(root, "runtime"), AgentDockHome: filepath.Join(root, "home")}
}

func assertFreshPermissionPolicy(t *testing.T, home string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, "execution", "permissions", "policy.json"))
	if err != nil {
		t.Fatal(err)
	}
	var policy permission.Policy
	if err := json.Unmarshal(data, &policy); err != nil {
		t.Fatal(err)
	}
	if policy.SchemaVersion != permission.CurrentSchemaVersion || policy.GlobalMode != permission.Full ||
		policy.CustomPermissionsEnabled == nil || *policy.CustomPermissionsEnabled {
		t.Fatalf("unexpected fresh-install policy: %+v", policy)
	}
	return data
}

func TestFreshInstallPermissionsAreExclusiveIdempotentAndJournaled(t *testing.T) {
	request := freshPermissionRequest(t)
	home := provenFreshPermissionHome(request)
	if home != request.AgentDockHome {
		t.Fatal("clean installation was not recognized")
	}
	journal := newJournal(request.RuntimeRoot, "permissions")
	ctx := context.Background()
	if err := initializeInstallPermissions(ctx, request, home, journal); err != nil {
		t.Fatal(err)
	}
	before := assertFreshPermissionPolicy(t, home)
	if err := initializeInstallPermissions(ctx, request, home, journal); err != nil {
		t.Fatal(err)
	}
	if string(assertFreshPermissionPolicy(t, home)) != string(before) {
		t.Fatal("repeated initializer changed existing policy bytes")
	}
	if len(journal.Created) != 1 || journal.Created[0] != home {
		t.Fatalf("new Home was not journaled exactly once: %v", journal.Created)
	}
	if err := journal.Restore(ctx, request); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(home); !os.IsNotExist(err) {
		t.Fatalf("rollback left fresh permission Home: %v", err)
	}
}

func TestFreshInstallPermissionsPreserveExistingHomesAndSchemas(t *testing.T) {
	for _, content := range []string{"", `{"schema_version":1,"global_mode":"restricted"}`,
		`{"schema_version":2,"global_mode":"standard"}`,
		`{"schema_version":3,"global_mode":"standard","custom_permissions_enabled":true}`} {
		t.Run(content, func(t *testing.T) {
			request := freshPermissionRequest(t)
			root := filepath.Join(request.AgentDockHome, "execution", "permissions")
			if err := os.MkdirAll(root, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "policy.json")
			if content != "" {
				if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			proof := provenFreshPermissionHome(request)
			if proof != "" {
				t.Fatal("existing Home must not be considered fresh, even without policy")
			}
			if err := initializeInstallPermissions(context.Background(), request, proof, newJournal(request.RuntimeRoot, "existing")); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(path)
			if content == "" {
				if !os.IsNotExist(err) {
					t.Fatal("initializer created a policy in an existing Home")
				}
			} else if err != nil || string(got) != content {
				t.Fatalf("existing schema bytes changed: %s, %v", got, err)
			}
		})
	}
}

func TestFreshInstallProofRejectsRepairAndPriorInstallation(t *testing.T) {
	request := freshPermissionRequest(t)
	request.Action = ActionRepair
	if provenFreshPermissionHome(request) != "" {
		t.Fatal("repair cannot initialize defaults")
	}
	for _, relative := range []string{"runtime.json", "agentdock.env", "install/transaction.json",
		"install/result.json", "active-version.json", "bin/agentdock.exe", "bin/agentdock", "versions/v1/core"} {
		t.Run(relative, func(t *testing.T) {
			request := freshPermissionRequest(t)
			path := filepath.Join(request.InstallRoot, filepath.FromSlash(relative))
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("prior installation"), 0o600); err != nil {
				t.Fatal(err)
			}
			if provenFreshPermissionHome(request) != "" {
				t.Fatal("prior installation was mistaken for fresh because Home was missing")
			}
		})
	}
}

func TestFreshInstallDoesNotClaimHomeThatAppearedAfterProbe(t *testing.T) {
	request := freshPermissionRequest(t)
	proof := provenFreshPermissionHome(request)
	if err := os.Mkdir(proof, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(proof, "user-data")
	if err := os.WriteFile(marker, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	journal := newJournal(request.RuntimeRoot, "concurrent")
	if err := initializeInstallPermissions(context.Background(), request, proof, journal); err != nil {
		t.Fatal(err)
	}
	if len(journal.Created) != 0 {
		t.Fatal("another actor's Home was added to rollback ownership")
	}
	if _, err := os.Stat(filepath.Join(proof, "execution")); !os.IsNotExist(err) {
		t.Fatal("initializer touched the Home created by another actor")
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "preserve" {
		t.Fatalf("concurrent data was changed: %q %v", data, err)
	}
}

func TestEngineWiresFreshInstallPermissionsBeforeActivation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix payload integration; platform-neutral permission boundary tests run on Windows")
	}
	for _, failActivation := range []bool{false, true} {
		name := "commit"
		if failActivation {
			name = "rollback"
		}
		t.Run(name, func(t *testing.T) {
			request := freshPermissionRequest(t)
			root := filepath.Dir(request.InstallRoot)
			request.PayloadDir = writeUnixPayload(t, root, "payload", "#!/bin/sh\nexit 0\n")
			request.Version, request.ServiceManager = "v1.2.3", "none"
			request.SkipSkills, request.SkipHealth = true, true
			if failActivation {
				request.TunnelMode = "named"
				request.ServerURL = "https://example.invalid"
				request.TunnelTokenFile = filepath.Join(root, "absent-token")
			}
			result, err := (Engine{}).Run(context.Background(), request)
			if failActivation {
				if err == nil || result.State != updateengine.StateRolledBack {
					t.Fatalf("expected isolated activation rollback: %+v %v", result, err)
				}
				if _, err := os.Stat(request.AgentDockHome); !os.IsNotExist(err) {
					t.Fatal("failed installation left full permission behind")
				}
				return
			}
			if err != nil || result.State != updateengine.StateCommitted {
				t.Fatalf("isolated install: %+v %v", result, err)
			}
			assertFreshPermissionPolicy(t, request.AgentDockHome)
		})
	}
}
