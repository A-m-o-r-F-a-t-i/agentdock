package permission

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/uvwt/agentdock/internal/activity"
)

func settingsStore(t *testing.T, settings Settings) *Store {
	t.Helper()
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Update(context.Background(), Change{Scope: "global", ExpectedRevision: 1, Settings: &settings}); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestProfileHardCeilingPrecedesFullAndAllow(t *testing.T) {
	for _, fs := range []string{Deny, FileRead, FileWrite} {
		for _, network := range []string{Deny, Allow} {
			for _, boundary := range []string{BoundaryNone, BoundaryWorkspace} {
				for _, access := range []string{"", FileRead, FileWrite} {
					name := fs + "/" + network + "/" + boundary + "/" + access
					t.Run(name, func(t *testing.T) {
						settings := DefaultSettings()
						settings.Profile = Profile{Filesystem: fs, Network: network, SandboxBoundary: boundary}
						s := settingsStore(t, settings)
						if _, err := s.Update(context.Background(), Change{Scope: "global", Mode: Full, ConfirmFull: true, ExpectedRevision: 2}); err != nil {
							t.Fatal(err)
						}
						facts := Facts{Tool: "file_edit", EffectsKnown: true, Filesystem: access, WorkspaceBound: true}
						d, err := s.Decide(context.Background(), facts)
						denied := fs == Deny && access != "" || fs == FileRead && access == FileWrite
						if err != nil || (d.Effect == Deny) != denied {
							t.Fatalf("decision=%+v err=%v denied=%v", d, err, denied)
						}
						facts.Network = true
						d, err = s.Decide(context.Background(), facts)
						if err != nil || (d.Effect == Deny) != (denied || network == Deny) {
							t.Fatalf("network ceiling: %+v %v", d, err)
						}
						facts.Network, facts.WorkspaceBound = false, false
						d, err = s.Decide(context.Background(), facts)
						if err != nil || (d.Effect == Deny) != (denied || boundary == BoundaryWorkspace) {
							t.Fatalf("boundary ceiling: %+v %v", d, err)
						}
					})
				}
			}
		}
	}
}

func TestProfileOpaqueEffectsAndDenyRule(t *testing.T) {
	ctx := context.Background()
	settings := DefaultSettings()
	settings.Profile.Network = Deny
	s := settingsStore(t, settings)
	rules := []Rule{{ID: "allow_shell", Tool: "exec_command", Effect: Allow}}
	if _, err := s.Update(ctx, Change{ExpectedRevision: 2, Rules: &rules}); err != nil {
		t.Fatal(err)
	}
	d, err := s.Decide(ctx, Facts{Tool: "exec_command", ReadOnly: true})
	if err != nil || d.Effect != Deny || d.RuleID != "profile-unknown-effects" {
		t.Fatalf("untrusted shell allowed: %+v %v", d, err)
	}
	rules = []Rule{{ID: "explicit_deny", Tool: "read_file", Effect: Deny}}
	if _, err = s.Update(ctx, Change{ExpectedRevision: 3, Rules: &rules}); err != nil {
		t.Fatal(err)
	}
	d, err = s.Decide(ctx, Facts{Tool: "read_file", ReadOnly: true, EffectsKnown: true, Filesystem: FileRead, WorkspaceBound: true})
	if err != nil || d.RuleID != "explicit_deny" {
		t.Fatalf("explicit deny lost precedence: %+v %v", d, err)
	}
}

func TestApprovalNeverRejectsAskNotAlreadyAllowed(t *testing.T) {
	settings := DefaultSettings()
	settings.Approval.Mode = Never
	s := settingsStore(t, settings)
	d, err := s.Decide(context.Background(), Facts{Tool: "file_edit"})
	if err != nil || d.Effect != Deny || d.RuleID != "approval-policy" {
		t.Fatalf("never approved: %+v %v", d, err)
	}
	d, err = s.Decide(context.Background(), Facts{Tool: "read_file", ReadOnly: true})
	if err != nil || d.Effect != Allow {
		t.Fatalf("never blocked already allowed read: %+v %v", d, err)
	}
	if _, err = s.Create(context.Background(), Approval{Binding: activity.Binding{CallID: "call_never"}, PolicyRevision: 2}); err == nil {
		t.Fatal("never accepted pending approval")
	}
}

func TestApprovalGranularAllApplicableCategories(t *testing.T) {
	for _, test := range []struct {
		name    string
		facts   Facts
		allowed ApprovalCategories
	}{
		{"file", Facts{Tool: "file_edit", Filesystem: FileWrite}, ApprovalCategories{FileWrites: true}},
		{"command", Facts{Tool: "exec_command"}, ApprovalCategories{Commands: true}},
		{"network", Facts{Tool: "download", Network: true}, ApprovalCategories{Network: true}},
		{"mcp", Facts{Tool: "mcp_tool_call"}, ApprovalCategories{MCP: true}},
		{"management", Facts{Tool: "workspace_manage"}, ApprovalCategories{Management: true}},
		{"other", Facts{Tool: "future_tool"}, ApprovalCategories{Other: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			settings := DefaultSettings()
			settings.Approval = ApprovalPolicy{Mode: Granular, Granular: &ApprovalCategories{}}
			s := settingsStore(t, settings)
			d, err := s.Decide(context.Background(), test.facts)
			if err != nil || d.Effect != Deny {
				t.Fatalf("unchecked category allowed: %+v %v", d, err)
			}
			settings.Approval.Granular = &test.allowed
			if _, err = s.Update(context.Background(), Change{Scope: "global", ExpectedRevision: 2, Settings: &settings}); err != nil {
				t.Fatal(err)
			}
			d, err = s.Decide(context.Background(), test.facts)
			if err != nil || d.Effect != Ask {
				t.Fatalf("checked category not reviewable: %+v %v", d, err)
			}
		})
	}
	settings := DefaultSettings()
	settings.Approval = ApprovalPolicy{Mode: Granular, Granular: &ApprovalCategories{Commands: true}}
	s := settingsStore(t, settings)
	d, err := s.Decide(context.Background(), Facts{Tool: "exec_command", Network: true})
	if err != nil || d.Effect != Deny {
		t.Fatalf("one category bypassed another: %+v %v", d, err)
	}
}

func TestProfileLegacyUpgradeInheritanceAndReset(t *testing.T) {
	root := t.TempDir()
	legacy := `{"schema_version":1,"revision":7,"global_mode":"rules","scopes":[{"kind":"workspace","id":"wsp_legacy","mode":"readonly"}],"rules":[]}`
	if err := os.WriteFile(filepath.Join(root, "policy.json"), []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	before, err := s.Effective(ctx, activity.Binding{WorkspaceID: "wsp_legacy"})
	if err != nil || before.Mode != ReadOnly || before.Settings.Reviewer != ReviewerUser {
		t.Fatal(before, err)
	}
	settings := DefaultSettings()
	settings.Profile.Network = Deny
	policy, err := s.Update(ctx, Change{Scope: "workspace", ScopeID: "wsp_other", ExpectedRevision: 7, Settings: &settings})
	if err != nil || policy.SchemaVersion != 2 {
		t.Fatal(policy, err)
	}
	a, _ := s.Effective(ctx, activity.Binding{WorkspaceID: "wsp_other", ConversationID: "conv_a"})
	b, _ := s.Effective(ctx, activity.Binding{WorkspaceID: "wsp_legacy", ConversationID: "conv_b"})
	if a.Settings.Profile.Network != Deny || b.Settings.Profile.Network != Allow || b.Mode != ReadOnly {
		t.Fatal("workspace profiles leaked")
	}
	if _, err = s.Update(ctx, Change{Scope: "conversation", ScopeID: "conv_a", ExpectedRevision: 8, Settings: &settings}); err == nil {
		t.Fatal("conversation profile accepted")
	}
	if _, err = s.Update(ctx, Change{Scope: "workspace", ScopeID: "wsp_other", ExpectedRevision: 8, InheritSettings: true}); err != nil {
		t.Fatal(err)
	}
	restored, _ := s.Effective(ctx, activity.Binding{WorkspaceID: "wsp_other"})
	if restored.SettingsScope != "global" || restored.Settings.Profile.Network != Allow {
		t.Fatal(restored)
	}
	reopened, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(restored.Settings)
	fresh, _ := reopened.Effective(ctx, activity.Binding{WorkspaceID: "wsp_other"})
	other, _ := json.Marshal(fresh.Settings)
	if string(data) != string(other) {
		t.Fatal("settings changed after restart")
	}
}

func TestProfileRejectsInvalidSettingsAndRevision(t *testing.T) {
	for _, mutate := range []func(*Settings){
		func(s *Settings) { s.Profile.Filesystem = "execute" }, func(s *Settings) { s.Profile.Network = "ask" }, func(s *Settings) { s.Profile.SandboxBoundary = "os-sandbox" },
		func(s *Settings) { s.Approval.Mode = "always" }, func(s *Settings) { s.Approval.Mode = Granular }, func(s *Settings) { s.Approval.Granular = &ApprovalCategories{} }, func(s *Settings) { s.Reviewer = "model" },
	} {
		settings := DefaultSettings()
		mutate(&settings)
		if err := settings.Validate(); err == nil {
			t.Fatalf("invalid settings accepted: %+v", settings)
		}
	}
	settings := DefaultSettings()
	s := settingsStore(t, settings)
	if _, err := s.Update(context.Background(), Change{Scope: "global", ExpectedRevision: 1, Settings: &settings}); err != ErrRevision {
		t.Fatal("stale update accepted", err)
	}
}
