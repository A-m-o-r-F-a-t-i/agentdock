package permission

import "errors"

const (
	FileRead          = "read"
	FileWrite         = "write"
	BoundaryNone      = "none"
	BoundaryWorkspace = "workspace"
	OnRequest         = "on-request"
	Never             = "never"
	Granular          = "granular"
	ReviewerUser      = "user"
	ReviewerAuto      = "auto_review"
)

// Profile is a tool-admission ceiling, not an OS process sandbox. Unknown
// effects cannot satisfy a restricted ceiling, even after an approval.
type Profile struct {
	Filesystem      string `json:"filesystem"`
	Network         string `json:"network"`
	SandboxBoundary string `json:"sandbox_boundary"`
}

type ApprovalCategories struct {
	FileWrites bool `json:"file_writes"`
	Commands   bool `json:"commands"`
	Network    bool `json:"network"`
	MCP        bool `json:"mcp"`
	Management bool `json:"management"`
	Other      bool `json:"other"`
}

type ApprovalPolicy struct {
	Mode     string              `json:"mode"`
	Granular *ApprovalCategories `json:"granular,omitempty"`
}

type Settings struct {
	Profile  Profile        `json:"permission_profile"`
	Approval ApprovalPolicy `json:"approval_policy"`
	Reviewer string         `json:"approval_reviewer"`
}

func DefaultSettings() Settings {
	return Settings{Profile: Profile{Filesystem: FileWrite, Network: Allow, SandboxBoundary: BoundaryNone}, Approval: ApprovalPolicy{Mode: OnRequest}, Reviewer: ReviewerUser}
}

func (s Settings) Validate() error {
	if s.Profile.Filesystem != Deny && s.Profile.Filesystem != FileRead && s.Profile.Filesystem != FileWrite {
		return errors.New("filesystem must be deny, read or write")
	}
	if s.Profile.Network != Allow && s.Profile.Network != Deny {
		return errors.New("network must be allow or deny")
	}
	if s.Profile.SandboxBoundary != BoundaryNone && s.Profile.SandboxBoundary != BoundaryWorkspace {
		return errors.New("sandbox_boundary must be none or workspace (admission boundary, not an OS sandbox)")
	}
	if s.Approval.Mode != OnRequest && s.Approval.Mode != Never && s.Approval.Mode != Granular {
		return errors.New("approval policy must be on-request, never or granular")
	}
	if (s.Approval.Mode == Granular) != (s.Approval.Granular != nil) {
		return errors.New("granular settings are required exactly when approval mode is granular")
	}
	if s.Reviewer != ReviewerUser && s.Reviewer != ReviewerAuto {
		return errors.New("approval reviewer must be user or auto_review")
	}
	return nil
}

func (p Profile) Restricted() bool {
	return p.Filesystem != FileWrite || p.Network != Allow || p.SandboxBoundary != BoundaryNone
}

func profileDecision(d Decision, f Facts) Decision {
	profile := d.Settings.Profile
	deny := func(id, reason string) Decision {
		d.Effect, d.RuleID, d.Reason = Deny, id, reason
		return d
	}
	if !profile.Restricted() {
		return d
	}
	if !f.EffectsKnown {
		return deny("profile-unknown-effects", "当前权限配置无法约束此工具的内部副作用；未派发，审批不能绕过此限制。")
	}
	if profile.Filesystem == Deny && f.Filesystem != "" || profile.Filesystem == FileRead && f.Filesystem == FileWrite {
		return deny("profile-filesystem", "文件系统访问超出 Permission Profile；审批不能扩大此权限。")
	}
	if profile.Network == Deny && f.Network {
		return deny("profile-network", "Permission Profile 禁止此操作的网络访问。")
	}
	if profile.SandboxBoundary == BoundaryWorkspace && !f.WorkspaceBound {
		return deny("profile-boundary", "无法确认所有目标都位于固定工作区内；命令、远程运行时和不透明工具不会被视为已沙箱化。")
	}
	return d
}

func approvalDecision(d Decision, f Facts) Decision {
	if d.Effect != Ask {
		return d
	}
	block := false
	switch d.Settings.Approval.Mode {
	case Never:
		block = true
	case Granular:
		g := d.Settings.Approval.Granular
		matched := false
		check := func(applies, permitted bool) {
			if applies {
				matched = true
				block = block || !permitted
			}
		}
		check(f.Filesystem == FileWrite, g.FileWrites)
		check(f.Tool == "exec_command" || f.Tool == "session_act", g.Commands)
		check(f.Network, g.Network)
		check(f.Tool == "mcp_tool_call", g.MCP)
		check(f.Management || f.Tool == "plugin_manage" || f.Tool == "skill_package" || f.Tool == "mcp_manage" || f.Tool == "workspace_manage", g.Management)
		if !matched {
			block = !g.Other
		}
	}
	if block {
		d.Effect, d.RuleID, d.Reason = Deny, "approval-policy", "当前 Approval Policy 不受理此类审批；操作未执行。never 不代表自动批准。"
	}
	return d
}

// applySettingsChange updates one complete settings object. A conversation ID
// may restrict legacy mode but cannot select or broaden a permission profile.
func applySettingsChange(p *Policy, c Change) error {
	if c.Settings == nil && !c.InheritSettings {
		return nil
	}
	if c.Settings != nil && c.InheritSettings {
		return errors.New("cannot set and inherit settings together")
	}
	if c.Scope != "global" && c.Scope != "workspace" || c.Scope == "global" && c.ScopeID != "" || c.Scope == "workspace" && !validID.MatchString(c.ScopeID) {
		return errors.New("permission settings require an explicit global or workspace scope")
	}
	if c.Settings != nil {
		if err := c.Settings.Validate(); err != nil {
			return err
		}
	}
	if c.Scope == "global" {
		if c.InheritSettings {
			return errors.New("global settings have no parent to inherit")
		}
		p.Settings = c.Settings
	} else {
		found := false
		for i := range p.Scopes {
			if p.Scopes[i].Kind == c.Scope && p.Scopes[i].ID == c.ScopeID {
				p.Scopes[i].Settings = c.Settings
				if p.Scopes[i].Mode == "" && c.InheritSettings {
					p.Scopes = append(p.Scopes[:i], p.Scopes[i+1:]...)
				}
				found = true
				break
			}
		}
		if !found && c.Settings != nil {
			p.Scopes = append(p.Scopes, Scope{Kind: c.Scope, ID: c.ScopeID, Settings: c.Settings})
		}
	}
	p.SchemaVersion = 2
	return nil
}
