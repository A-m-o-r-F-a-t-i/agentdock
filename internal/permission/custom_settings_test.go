package permission

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/uvwt/agentdock/internal/activity"
)

func TestFreshInstallPolicyIsExplicitIdempotentAndDenyStillWins(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	policy, created, err := InitializeFreshInstall(ctx, root)
	if err != nil || !created {
		t.Fatalf("fresh initialization failed: created=%v policy=%+v err=%v", created, policy, err)
	}
	if policy.SchemaVersion != CurrentSchemaVersion || policy.GlobalMode != Full || policy.CustomPermissionsEnabled == nil || *policy.CustomPermissionsEnabled || policy.Settings == nil {
		t.Fatalf("unexpected fresh policy: %+v", policy)
	}
	path := filepath.Join(root, "policy.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	repeated, created, err := InitializeFreshInstall(ctx, root)
	if err != nil || created || repeated.Revision != 1 {
		t.Fatalf("repeat changed fresh policy: created=%v policy=%+v err=%v", created, repeated, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatalf("repeat rewrote policy: %v", err)
	}
	store, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	decision, err := store.Decide(ctx, Facts{Tool: "file_edit"})
	if err != nil || decision.Effect != Allow || decision.RuleID != "full-scope" {
		t.Fatalf("fresh full mode not effective: %+v %v", decision, err)
	}
	rules := []Rule{{ID: "deny_edit", Tool: "file_edit", Effect: Deny, Reason: "explicit fixture"}}
	if _, err = store.Update(ctx, Change{ExpectedRevision: 1, Rules: &rules}); err != nil {
		t.Fatal(err)
	}
	decision, err = store.Decide(ctx, Facts{Tool: "file_edit"})
	if err != nil || decision.Effect != Deny || decision.RuleID != "deny_edit" {
		t.Fatalf("fresh full mode bypassed explicit deny: %+v %v", decision, err)
	}
}

func TestFreshInstallInitializerPreservesExistingLegacyPolicyBytes(t *testing.T) {
	root := t.TempDir()
	legacy := `{"schema_version":1,"revision":7,"global_mode":"readonly","scopes":[],"rules":[]}` + "\n"
	path := filepath.Join(root, "policy.json")
	if err := os.WriteFile(path, []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	policy, created, err := InitializeFreshInstall(context.Background(), root)
	if err != nil || created || policy.GlobalMode != ReadOnly || policy.CustomPermissionsEnabled == nil || *policy.CustomPermissionsEnabled {
		t.Fatalf("existing policy changed: created=%v policy=%+v err=%v", created, policy, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != legacy {
		t.Fatalf("legacy bytes were rewritten: %q %v", after, err)
	}
}

func TestLegacyCustomSettingsMigrationPreservesMeaningAndWritesOnlyOnUpdate(t *testing.T) {
	root := t.TempDir()
	legacy := `{"schema_version":2,"revision":7,"global_mode":"rules","settings":{"permission_profile":{"filesystem":"write","network":"deny","sandbox_boundary":"none"},"approval_policy":{"mode":"never"},"approval_reviewer":"user"},"scopes":[{"kind":"workspace","id":"wsp_legacy","mode":"","settings":{"permission_profile":{"filesystem":"read","network":"allow","sandbox_boundary":"workspace"},"approval_policy":{"mode":"on-request"},"approval_reviewer":"user"}}],"rules":[]}`
	path := filepath.Join(root, "policy.json")
	if err := os.WriteFile(path, []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := store.Get(context.Background())
	if err != nil || policy.SchemaVersion != CurrentSchemaVersion || policy.CustomPermissionsEnabled == nil || !*policy.CustomPermissionsEnabled || policy.Scopes[0].CustomPermissionsEnabled == nil || !*policy.Scopes[0].CustomPermissionsEnabled {
		t.Fatalf("legacy state not normalized: %+v %v", policy, err)
	}
	unchanged, _ := os.ReadFile(path)
	if string(unchanged) != legacy {
		t.Fatal("read-only normalization rewrote the legacy policy")
	}
	effective, err := store.Effective(context.Background(), activity.Binding{WorkspaceID: "wsp_legacy"})
	if err != nil || !effective.CustomPermissionsEnabled || effective.SettingsSource != "custom_permissions" || effective.SettingsScope != "workspace" || effective.Settings.Profile.Filesystem != FileRead {
		t.Fatalf("legacy workspace settings lost: %+v %v", effective, err)
	}
	policy, err = store.Update(context.Background(), Change{Scope: "global", Mode: Rules, ExpectedRevision: 7})
	if err != nil || policy.SchemaVersion != CurrentSchemaVersion || policy.Revision != 8 {
		t.Fatalf("legacy update failed: %+v %v", policy, err)
	}
	var durable Policy
	raw, _ := os.ReadFile(path)
	if err = json.Unmarshal(raw, &durable); err != nil || durable.CustomPermissionsEnabled == nil || durable.Scopes[0].CustomPermissionsEnabled == nil {
		t.Fatalf("schema-3 migration not durable: %s %v", raw, err)
	}
}

func TestMissingLegacyCustomFieldDefaultsDisabledWithoutExplicitSettings(t *testing.T) {
	root := t.TempDir()
	legacy := `{"schema_version":1,"revision":4,"global_mode":"full","scopes":[{"kind":"workspace","id":"wsp_mode","mode":"readonly"}],"rules":[]}`
	if err := os.WriteFile(filepath.Join(root, "policy.json"), []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	global, _ := store.Effective(context.Background(), activity.Binding{})
	workspace, _ := store.Effective(context.Background(), activity.Binding{WorkspaceID: "wsp_mode"})
	if global.CustomPermissionsEnabled || global.SettingsSource != "execution_mode" || global.Mode != Full || workspace.CustomPermissionsEnabled || workspace.Mode != ReadOnly {
		t.Fatalf("legacy no-settings policy was broadened or reinterpreted: global=%+v workspace=%+v", global, workspace)
	}
}

func TestCustomPermissionToggleRetainsHistoryAndNeverDoesNotAuthorize(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	settings := DefaultSettings()
	settings.Profile.Network = Deny
	settings.Approval.Mode = Never
	settings.Reviewer = ReviewerAuto
	enabled := true
	policy, err := store.Update(ctx, Change{Scope: "global", Mode: Full, ConfirmFull: true, ExpectedRevision: 1, Settings: &settings, CustomPermissionsEnabled: &enabled})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := store.Decide(ctx, Facts{Tool: "download", EffectsKnown: true, Network: true})
	if err != nil || decision.Effect != Deny || decision.RuleID != "profile-network" {
		t.Fatalf("enabled profile not enforced: %+v %v", decision, err)
	}
	disabled := false
	policy, err = store.Update(ctx, Change{Scope: "global", ExpectedRevision: policy.Revision, CustomPermissionsEnabled: &disabled})
	if err != nil {
		t.Fatal(err)
	}
	effective, err := store.Effective(ctx, activity.Binding{})
	if err != nil || effective.CustomPermissionsEnabled || effective.SettingsSource != "execution_mode" || effective.Settings.Profile.Network != Allow || effective.ConfiguredSettings.Profile.Network != Deny || effective.ConfiguredSettings.Approval.Mode != Never || effective.ConfiguredSettings.Reviewer != ReviewerAuto {
		t.Fatalf("disabled history/effective split incorrect: %+v %v", effective, err)
	}
	decision, err = store.Decide(ctx, Facts{Tool: "download", EffectsKnown: true, Network: true})
	if err != nil || decision.Effect != Allow || decision.RuleID != "full-scope" {
		t.Fatalf("disabled custom settings still constrained full mode: %+v %v", decision, err)
	}
	policy, err = store.Update(ctx, Change{Scope: "global", Mode: Rules, ExpectedRevision: policy.Revision})
	if err != nil {
		t.Fatal(err)
	}
	decision, err = store.Decide(ctx, Facts{Tool: "file_edit", EffectsKnown: true, Filesystem: FileWrite, WorkspaceBound: true})
	if err != nil || decision.Effect != Ask {
		t.Fatalf("disabled historical never changed mode behavior: %+v %v", decision, err)
	}
	policy, err = store.Update(ctx, Change{Scope: "global", ExpectedRevision: policy.Revision, CustomPermissionsEnabled: &enabled})
	if err != nil {
		t.Fatal(err)
	}
	decision, err = store.Decide(ctx, Facts{Tool: "file_edit", EffectsKnown: true, Filesystem: FileWrite, WorkspaceBound: true})
	if err != nil || decision.Effect != Deny || decision.RuleID != "approval-policy" {
		t.Fatalf("never authorized or queued approval-required work: %+v %v", decision, err)
	}
	reopened, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	effective, err = reopened.Effective(ctx, activity.Binding{})
	if err != nil || !effective.CustomPermissionsEnabled || effective.ConfiguredSettings.Profile.Network != Deny {
		t.Fatalf("toggle did not survive restart: %+v %v", effective, err)
	}
}

func TestWorkspaceCustomPermissionInheritance(t *testing.T) {
	ctx := context.Background()
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	global := DefaultSettings()
	global.Profile.Network = Deny
	disabled := false
	policy, err := store.Update(ctx, Change{Scope: "global", ExpectedRevision: 1, Settings: &global, CustomPermissionsEnabled: &disabled})
	if err != nil {
		t.Fatal(err)
	}
	workspace := DefaultSettings()
	workspace.Profile.Filesystem = Deny
	enabled := true
	policy, err = store.Update(ctx, Change{Scope: "workspace", ScopeID: "wsp_custom", ExpectedRevision: policy.Revision, Settings: &workspace, CustomPermissionsEnabled: &enabled})
	if err != nil {
		t.Fatal(err)
	}
	effective, err := store.Effective(ctx, activity.Binding{WorkspaceID: "wsp_custom"})
	if err != nil || !effective.CustomPermissionsEnabled || effective.CustomPermissionsScope != "workspace" || effective.SettingsScope != "workspace" || effective.Settings.Profile.Filesystem != Deny {
		t.Fatalf("workspace override not effective: %+v %v", effective, err)
	}
	other, _ := store.Effective(ctx, activity.Binding{WorkspaceID: "wsp_other"})
	if other.CustomPermissionsEnabled || other.Settings.Profile.Network != Allow || other.ConfiguredSettings.Profile.Network != Deny {
		t.Fatalf("workspace override leaked: %+v", other)
	}
	policy, err = store.Update(ctx, Change{Scope: "workspace", ScopeID: "wsp_flag_only", ExpectedRevision: policy.Revision, CustomPermissionsEnabled: &enabled})
	if err != nil {
		t.Fatal(err)
	}
	flagOnly, err := store.Effective(ctx, activity.Binding{WorkspaceID: "wsp_flag_only"})
	if err != nil || !flagOnly.CustomPermissionsEnabled || flagOnly.CustomPermissionsScope != "workspace" || flagOnly.SettingsScope != "global" || flagOnly.Settings.Profile.Network != Deny {
		t.Fatalf("workspace flag did not inherit global configured settings: %+v %v", flagOnly, err)
	}
	policy, err = store.Update(ctx, Change{Scope: "workspace", ScopeID: "wsp_flag_only", ExpectedRevision: policy.Revision, InheritSettings: true})
	if err != nil {
		t.Fatal(err)
	}
	policy, err = store.Update(ctx, Change{Scope: "workspace", ScopeID: "wsp_custom", ExpectedRevision: policy.Revision, InheritSettings: true})
	if err != nil {
		t.Fatal(err)
	}
	effective, err = store.Effective(ctx, activity.Binding{WorkspaceID: "wsp_custom"})
	if err != nil || effective.CustomPermissionsEnabled || effective.CustomPermissionsScope != "global" || effective.SettingsScope != "global" || effective.ConfiguredSettings.Profile.Network != Deny {
		t.Fatalf("workspace inheritance not restored: %+v %v", effective, err)
	}
	if _, err = store.Update(ctx, Change{Scope: "conversation", ScopeID: "conv_custom", ExpectedRevision: policy.Revision, CustomPermissionsEnabled: &enabled}); err == nil {
		t.Fatal("conversation enabled custom permissions")
	}
}

func TestCustomPermissionConcurrentConflictAndInvalidSaveAreAtomic(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	first, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	errorsSeen := make(chan error, 2)
	var wait sync.WaitGroup
	for index, store := range []*Store{first, second} {
		wait.Add(1)
		go func(index int, store *Store) {
			defer wait.Done()
			<-start
			enabled := index == 0
			_, updateErr := store.Update(ctx, Change{Scope: "global", ExpectedRevision: 1, CustomPermissionsEnabled: &enabled})
			errorsSeen <- updateErr
		}(index, store)
	}
	close(start)
	wait.Wait()
	close(errorsSeen)
	succeeded, conflicted := 0, 0
	for updateErr := range errorsSeen {
		switch {
		case updateErr == nil:
			succeeded++
		case errors.Is(updateErr, ErrRevision):
			conflicted++
		default:
			t.Fatalf("unexpected concurrent error: %v", updateErr)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("concurrent writes: succeeded=%d conflicted=%d", succeeded, conflicted)
	}
	path := filepath.Join(root, "policy.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	current, err := first.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	invalid := DefaultSettings()
	invalid.Profile.Filesystem = "execute"
	enabled := true
	if _, err = first.Update(ctx, Change{Scope: "global", ExpectedRevision: current.Revision, Settings: &invalid, CustomPermissionsEnabled: &enabled}); err == nil {
		t.Fatal("invalid settings were committed")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatalf("invalid save changed durable policy: %v", err)
	}
}

func TestLegacyClientExplicitSettingsEnablesCustomPermissions(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	settings := DefaultSettings()
	settings.Profile.Filesystem = FileRead
	policy, err := store.Update(context.Background(), Change{Scope: "global", ExpectedRevision: 1, Settings: &settings})
	if err != nil || policy.CustomPermissionsEnabled == nil || !*policy.CustomPermissionsEnabled {
		t.Fatalf("legacy settings write was ignored: %+v %v", policy, err)
	}
	effective, err := store.Effective(context.Background(), activity.Binding{})
	if err != nil || !effective.CustomPermissionsEnabled || effective.Settings.Profile.Filesystem != FileRead {
		t.Fatalf("legacy settings not effective: %+v %v", effective, err)
	}
}
