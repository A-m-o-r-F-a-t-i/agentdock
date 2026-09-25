// Package permission decides whether AgentDock may dispatch a fixed request.
// It does not elevate OS privileges or implement an operating-system sandbox.
package permission

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/uvwt/agentdock/internal/activity"
	"github.com/uvwt/agentdock/internal/fs/atomicfile"
	"github.com/uvwt/agentdock/internal/fs/filelock"
)

const (
	ReadOnly             = "readonly"
	Rules                = "rules"
	Full                 = "full"
	Allow                = "allow"
	Ask                  = "ask"
	Deny                 = "deny"
	CurrentSchemaVersion = 3
)

var ErrRevision = errors.New("permission policy changed; reload the effective policy")
var validID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)

type Rule struct {
	ID          string `json:"id"`
	Tool        string `json:"tool"`
	Action      string `json:"action,omitempty"`
	WorkspaceID string `json:"workspace_id,omitempty"`
	Effect      string `json:"effect"`
	Reason      string `json:"reason"`
}
type Scope struct {
	Settings                 *Settings `json:"settings,omitempty"`
	CustomPermissionsEnabled *bool     `json:"custom_permissions_enabled,omitempty"`
	Kind                     string    `json:"kind"`
	ID                       string    `json:"id"`
	Mode                     string    `json:"mode"`
}
type Policy struct {
	Settings                 *Settings `json:"settings,omitempty"`
	CustomPermissionsEnabled *bool     `json:"custom_permissions_enabled,omitempty"`
	SchemaVersion            int       `json:"schema_version"`
	Revision                 uint64    `json:"revision"`
	GlobalMode               string    `json:"global_mode"`
	Scopes                   []Scope   `json:"scopes"`
	Rules                    []Rule    `json:"rules"`
	UpdatedAt                time.Time `json:"updated_at"`
}
type Effective struct {
	Settings                 Settings `json:"settings"`
	ConfiguredSettings       Settings `json:"configured_settings"`
	SettingsSource           string   `json:"settings_source"`
	SettingsScope            string   `json:"settings_scope"`
	SettingsScopeID          string   `json:"settings_scope_id,omitempty"`
	CustomPermissionsEnabled bool     `json:"custom_permissions_enabled"`
	CustomPermissionsScope   string   `json:"custom_permissions_scope"`
	CustomPermissionsScopeID string   `json:"custom_permissions_scope_id,omitempty"`
	Mode                     string   `json:"mode"`
	Scope                    string   `json:"scope"`
	ScopeID                  string   `json:"scope_id,omitempty"`
	Revision                 uint64   `json:"revision"`
}
type Change struct {
	Settings                 *Settings `json:"settings,omitempty"`
	CustomPermissionsEnabled *bool     `json:"custom_permissions_enabled,omitempty"`
	InheritSettings          bool      `json:"inherit_settings,omitempty"`
	Scope                    string    `json:"scope"`
	ScopeID                  string    `json:"scope_id,omitempty"`
	Mode                     string    `json:"mode,omitempty"`
	ExpectedRevision         uint64    `json:"expected_revision"`
	ConfirmFull              bool      `json:"confirm_full,omitempty"`
	Rules                    *[]Rule   `json:"rules,omitempty"`
}

// Facts must be computed by the runtime, never accepted as caller-supplied
// read-only hints. Third-party MCP annotations are not a trusted classification.
type Facts struct {
	EffectsKnown   bool
	Filesystem     string
	Network        bool
	WorkspaceBound bool
	Binding        activity.Binding
	Tool           string
	Action         string
	ReadOnly       bool
	Management     bool
	Reason         string
}
type Decision struct {
	Effective
	Effect string `json:"effect"`
	RuleID string `json:"rule_id"`
	Reason string `json:"reason"`
}
type Store struct {
	reviewSlots chan struct{}
	instance    string
	root        string
}

func New(root string) (*Store, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("permission root must be a real directory")
	}
	instance, err := activity.NewExecutionID("call_")
	if err != nil {
		return nil, err
	}
	store := &Store{root: root, instance: instance, reviewSlots: make(chan struct{}, 2)}
	if _, err = store.Get(context.Background()); err != nil {
		return nil, err
	}
	return store, nil
}
func (s *Store) locked(ctx context.Context, fn func() error) error {
	release, err := filelock.Acquire(ctx, filepath.Join(s.root, ".permission.lock"))
	if err != nil {
		return err
	}
	defer release()
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn()
}
func readJSON(path string, destination any) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > 2<<20 {
		return errors.New("invalid or oversized permission record")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(data) > 2<<20 {
		return errors.New("permission record exceeds size limit")
	}
	return json.Unmarshal(data, destination)
}
func writeJSON(ctx context.Context, path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(data) > 2<<20 {
		return errors.New("permission record exceeds size limit")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return atomicfile.Write(path, append(data, '\n'), 0600)
}
func validMode(mode string) bool   { return mode == ReadOnly || mode == Rules || mode == Full }
func boolPointer(value bool) *bool { return &value }

// validatePolicy validates the durable representation before compatibility
// normalization. Legacy schema 1/2 records therefore remain readable without
// accepting schema-3 fields under an older version number.
func validatePolicy(p Policy) error {
	if (p.SchemaVersion != 1 && p.SchemaVersion != 2 && p.SchemaVersion != CurrentSchemaVersion) || p.Revision == 0 || !validMode(p.GlobalMode) {
		return errors.New("invalid permission policy schema or mode")
	}
	if p.CustomPermissionsEnabled != nil && p.SchemaVersion != CurrentSchemaVersion {
		return errors.New("custom permission state requires schema version 3")
	}
	if p.SchemaVersion == CurrentSchemaVersion && p.CustomPermissionsEnabled == nil {
		return errors.New("schema version 3 requires custom_permissions_enabled")
	}
	if p.Settings != nil {
		if p.SchemaVersion < 2 {
			return errors.New("profile settings require schema version 2 or newer")
		}
		if err := p.Settings.Validate(); err != nil {
			return err
		}
	}
	if len(p.Scopes) > 4000 || len(p.Rules) > 512 {
		return errors.New("permission policy exceeds capacity")
	}
	seen := map[string]bool{}
	for _, scope := range p.Scopes {
		if (scope.Kind != "workspace" && scope.Kind != "conversation") || !validID.MatchString(scope.ID) || (scope.Mode != "" && !validMode(scope.Mode)) || (scope.Mode == "" && scope.Settings == nil && scope.CustomPermissionsEnabled == nil) {
			return errors.New("invalid permission scope")
		}
		if scope.Settings != nil {
			if p.SchemaVersion < 2 || scope.Kind != "workspace" {
				return errors.New("profile overrides require a workspace and schema version 2 or newer")
			}
			if err := scope.Settings.Validate(); err != nil {
				return err
			}
		}
		if scope.CustomPermissionsEnabled != nil && (p.SchemaVersion != CurrentSchemaVersion || scope.Kind != "workspace") {
			return errors.New("custom permission overrides require a workspace and schema version 3")
		}
		key := scope.Kind + ":" + scope.ID
		if scope.Kind == "conversation" && scope.Mode == Full {
			return errors.New("conversation IDs do not grant permissions; use an explicitly confirmed workspace or global full-permission scope")
		}
		if seen[key] {
			return errors.New("duplicate permission scope")
		}
		seen[key] = true
	}
	seen = map[string]bool{}
	for _, rule := range p.Rules {
		if !validID.MatchString(rule.ID) || seen[rule.ID] || !validID.MatchString(rule.Tool) || (rule.Action != "" && !validID.MatchString(rule.Action)) || (rule.WorkspaceID != "" && !validID.MatchString(rule.WorkspaceID)) || (rule.Effect != Allow && rule.Effect != Ask && rule.Effect != Deny) || len(rule.Reason) > 512 {
			return errors.New("invalid permission rule")
		}
		seen[rule.ID] = true
	}
	return nil
}

// normalizePolicy is an in-memory compatibility migration. It never writes a
// legacy policy during a read. A later successful revision-checked update
// persists schema 3 atomically.
func normalizePolicy(p *Policy) {
	if p.CustomPermissionsEnabled == nil {
		p.CustomPermissionsEnabled = boolPointer(p.Settings != nil)
	}
	for index := range p.Scopes {
		if p.Scopes[index].CustomPermissionsEnabled == nil && p.Scopes[index].Settings != nil {
			p.Scopes[index].CustomPermissionsEnabled = boolPointer(true)
		}
	}
	p.SchemaVersion = CurrentSchemaVersion
}

func (s *Store) loadPolicy() (Policy, error) {
	p := Policy{SchemaVersion: 1, Revision: 1, GlobalMode: Rules, Scopes: []Scope{}, Rules: []Rule{}}
	if err := readJSON(filepath.Join(s.root, "policy.json"), &p); err != nil && !os.IsNotExist(err) {
		return p, err
	}
	if err := validatePolicy(p); err != nil {
		return p, err
	}
	normalizePolicy(&p)
	return p, nil
}
func (s *Store) Get(ctx context.Context) (Policy, error) {
	var p Policy
	err := s.locked(ctx, func() error { var err error; p, err = s.loadPolicy(); return err })
	return p, err
}
func effectivePolicy(p Policy, binding activity.Binding) Effective {
	configured := DefaultSettings()
	if p.Settings != nil {
		configured = *p.Settings
	}
	enabled := p.CustomPermissionsEnabled != nil && *p.CustomPermissionsEnabled
	result := Effective{
		Settings:                 DefaultSettings(),
		ConfiguredSettings:       configured,
		SettingsSource:           "execution_mode",
		SettingsScope:            "global",
		CustomPermissionsEnabled: enabled,
		CustomPermissionsScope:   "global",
		Mode:                     p.GlobalMode,
		Scope:                    "global",
		Revision:                 p.Revision,
	}
	for _, kind := range []string{"workspace", "conversation"} {
		id := binding.WorkspaceID
		if kind == "conversation" {
			id = binding.ConversationID
		}
		for _, scope := range p.Scopes {
			if scope.Kind == kind && scope.ID == id {
				if scope.Mode != "" {
					result.Mode, result.Scope, result.ScopeID = scope.Mode, kind, id
				}
				if scope.Settings != nil {
					configured = *scope.Settings
					result.SettingsScope, result.SettingsScopeID = kind, id
				}
				if scope.CustomPermissionsEnabled != nil {
					result.CustomPermissionsEnabled = *scope.CustomPermissionsEnabled
					result.CustomPermissionsScope, result.CustomPermissionsScopeID = kind, id
				}
			}
		}
	}
	result.ConfiguredSettings = configured
	if result.CustomPermissionsEnabled {
		result.Settings = configured
		result.SettingsSource = "custom_permissions"
	}
	return result
}
func (s *Store) Effective(ctx context.Context, binding activity.Binding) (Effective, error) {
	p, err := s.Get(ctx)
	if err != nil {
		return Effective{}, err
	}
	return effectivePolicy(p, binding), nil
}
func (s *Store) Decide(ctx context.Context, facts Facts) (decision Decision, returnErr error) {
	p, err := s.Get(ctx)
	if err != nil {
		return Decision{}, err
	}
	decision = Decision{Effective: effectivePolicy(p, facts.Binding)}
	defer func() {
		if returnErr == nil && decision.CustomPermissionsEnabled {
			decision = approvalDecision(decision, facts)
		}
	}()
	matches := func(rule Rule) bool {
		return rule.Tool == facts.Tool && (rule.Action == "" || rule.Action == facts.Action) && (rule.WorkspaceID == "" || rule.WorkspaceID == facts.Binding.WorkspaceID)
	}
	for _, rule := range p.Rules {
		if rule.Effect == Deny && matches(rule) {
			decision.Effect, decision.RuleID, decision.Reason = Deny, rule.ID, rule.Reason
			return decision, nil
		}
	}
	if decision.CustomPermissionsEnabled {
		decision = profileDecision(decision, facts)
		if decision.Effect == Deny {
			return decision, nil
		}
	}
	if decision.Mode == ReadOnly {
		decision.Effect, decision.RuleID, decision.Reason = Deny, "readonly-write", "只读检查模式禁止尚未确认只读的操作。"
		if facts.ReadOnly {
			decision.Effect, decision.RuleID, decision.Reason = Allow, "readonly-safe", "已确认的内置只读操作。"
		}
		return decision, nil
	}
	if decision.Mode == Full {
		decision.Effect, decision.RuleID, decision.Reason = Allow, "full-scope", "用户在该作用范围启用了完全权限；显式禁止规则仍有效。"
		return decision, nil
	}
	for _, effect := range []string{Ask, Allow} {
		for _, rule := range p.Rules {
			if rule.Effect == effect && matches(rule) {
				decision.Effect, decision.RuleID, decision.Reason = effect, rule.ID, rule.Reason
				return decision, nil
			}
		}
	}
	if facts.ReadOnly || facts.Management {
		decision.Effect, decision.RuleID, decision.Reason = Allow, "builtin-safe", "内置只读检查或可恢复的任务元数据操作。"
		return decision, nil
	}
	decision.Effect, decision.RuleID = Ask, "review-side-effects"
	decision.Reason = facts.Reason
	if decision.Reason == "" {
		decision.Reason = "该操作可能修改文件、服务、设备或外部资源，需要执行前确认。"
	}
	return decision, nil
}
func (s *Store) Update(ctx context.Context, change Change) (Policy, error) {
	var result Policy
	err := s.locked(ctx, func() error {
		p, err := s.loadPolicy()
		if err != nil {
			return err
		}
		if change.ExpectedRevision != p.Revision {
			return ErrRevision
		}
		if change.Mode != "" {
			if !validMode(change.Mode) {
				return errors.New("invalid permission mode")
			}
			if change.Mode == Full && !change.ConfirmFull {
				return errors.New("full permissions require explicit local confirmation")
			}
			if change.Scope == "global" && change.ScopeID == "" {
				p.GlobalMode = change.Mode
			} else {
				if (change.Scope != "workspace" && change.Scope != "conversation") || !validID.MatchString(change.ScopeID) {
					return errors.New("invalid scope")
				}
				replaced := false
				for i := range p.Scopes {
					if p.Scopes[i].Kind == change.Scope && p.Scopes[i].ID == change.ScopeID {
						p.Scopes[i].Mode = change.Mode
						replaced = true
					}
				}
				if !replaced {
					p.Scopes = append(p.Scopes, Scope{Kind: change.Scope, ID: change.ScopeID, Mode: change.Mode})
				}
			}
		}
		if change.Rules != nil {
			p.Rules = append([]Rule(nil), (*change.Rules)...)
		}
		if err = applySettingsChange(&p, change); err != nil {
			return err
		}
		if change.Mode == "" && change.Rules == nil && change.Settings == nil && change.CustomPermissionsEnabled == nil && !change.InheritSettings {
			return errors.New("permission change is empty")
		}
		p.SchemaVersion = CurrentSchemaVersion
		p.Revision++
		p.UpdatedAt = time.Now().UTC()
		if err = validatePolicy(p); err != nil {
			return err
		}
		if err = writeJSON(ctx, filepath.Join(s.root, "policy.json"), p); err != nil {
			return err
		}
		result = p
		return nil
	})
	return result, err
}
func (s *Store) AllowWorkspaceRule(ctx context.Context, tool, action, workspaceID string, revision uint64) (Policy, error) {
	p, err := s.Get(ctx)
	if err != nil {
		return Policy{}, err
	}
	if workspaceID == "" {
		return Policy{}, errors.New("a workspace-specific allow rule requires an explicit workspace")
	}
	p.Rules = append(p.Rules, Rule{ID: fmt.Sprintf("allow_%d", time.Now().UnixNano()), Tool: tool, Action: action, WorkspaceID: workspaceID, Effect: Allow, Reason: "用户在审批时明确允许此工作区的同类工具操作。"})
	return s.Update(ctx, Change{ExpectedRevision: revision, Rules: &p.Rules})
}
func cleanText(value string) string { return strings.TrimSpace(value) }
