package command

import "github.com/uvwt/agentdock/internal/activity"

// RuntimeOptions 描述命令实际执行环境。非 Windows 主机只接受零值。
type RuntimeOptions struct {
	Runtime         string `json:"runtime,omitempty"`
	WSLDistribution string `json:"wsl_distribution,omitempty"`
}

// ExecRequest is the stable exec_command input contract.
type ExecRequest struct {
	TargetKind       string `json:"target_kind,omitempty"`
	ExternalPath     string `json:"external_path,omitempty"`
	activity.Binding `json:"-"`
	RuntimeOptions
	Cmd            string            `json:"cmd"`
	Workdir        string            `json:"workdir,omitempty"`
	Skill          string            `json:"skill,omitempty"`
	SkillEnv       string            `json:"skill_env,omitempty"`
	SkillRef       string            `json:"skill_ref,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	TimeoutMS      *int              `json:"timeout_ms,omitempty"`
	ExecutionMode  string            `json:"execution_mode,omitempty"`
	YieldTimeMS    *int              `json:"yield_time_ms,omitempty"`
	MaxOutputBytes *int              `json:"max_output_bytes,omitempty"`
	Stdin          string            `json:"stdin,omitempty"`
	TTY            bool              `json:"tty,omitempty"`
}

type SessionObserveRequest struct {
	Action         string `json:"action,omitempty"`
	SessionID      string `json:"session_id,omitempty"`
	MaxOutputBytes *int   `json:"max_output_bytes,omitempty"`
}

type SessionActRequest struct {
	Action         string `json:"action,omitempty"`
	SessionID      string `json:"session_id,omitempty"`
	Chars          string `json:"chars,omitempty"`
	MaxOutputBytes *int   `json:"max_output_bytes,omitempty"`
}

func intValue(value *int, fallback int) int {
	if value == nil {
		return fallback
	}
	return *value
}
