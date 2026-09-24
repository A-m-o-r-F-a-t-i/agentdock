// Package activity persists execution facts independently of task checkpoints.
package activity

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"
)

const (
	SchemaVersion   = 1
	MaxPreviewBytes = 8 << 10
	MaxEventBytes   = 32 << 10
	MaxQueryEvents  = 500
)

// ExecutionScope is copied into context by the ingress. Consumers receive a
// value snapshot, never a pointer to mutable UI or conversation state.
type ExecutionScope struct {
	Source          string `json:"source,omitempty"`
	BindingQuality  string `json:"binding_quality,omitempty"`
	BindingRevision uint64 `json:"binding_revision,omitempty"`
	Visibility      string `json:"visibility,omitempty"`
	SourceOwnerKey  string `json:"source_owner_key,omitempty"`
	ConversationID  string `json:"conversation_id,omitempty"`
	CallID          string `json:"call_id,omitempty"`
	ParentCallID    string `json:"parent_call_id,omitempty"`
	RetryOfCallID   string `json:"retry_of_call_id,omitempty"`
	TaskID          string `json:"task_id,omitempty"`
	ThreadID        string `json:"thread_id,omitempty"`
	StepID          string `json:"step_id,omitempty"`
	WorkspaceID     string `json:"workspace_id,omitempty"`
	Label           string `json:"activity_label,omitempty"`
}

// Binding retains the existing activity and command API name. ThreadID is a
// task thread and never aliases a host conversation.
type Binding = ExecutionScope

var identifier = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)

func (b ExecutionScope) Validate() error {
	for _, id := range []string{b.TaskID, b.ThreadID, b.StepID, b.WorkspaceID, b.CallID, b.ParentCallID, b.RetryOfCallID} {
		if id != "" && !identifier.MatchString(id) {
			return errors.New("invalid activity binding identifier")
		}
	}
	if b.ConversationID != "" && !conversationIdentifier.MatchString(b.ConversationID) {
		return errors.New("invalid conversation_id")
	}
	if b.CallID != "" && (b.ParentCallID == b.CallID || b.RetryOfCallID == b.CallID) {
		return errors.New("a call cannot parent or retry itself")
	}
	if b.TaskID == "" && (b.ThreadID != "" || b.StepID != "") {
		return errors.New("task_id is required with thread_id or step_id")
	}
	if len(b.Label) > 512 {
		return errors.New("activity_label exceeds 512 bytes")
	}
	return nil
}

type Event struct {
	CallMeasurements
	Request          *Payload         `json:"request,omitempty"`
	Response         *Payload         `json:"response,omitempty"`
	FileEdit         *FileEditDetails `json:"file_edit,omitempty"`
	OwnerPID         int              `json:"owner_pid,omitempty"`
	OwnerInstance    string           `json:"owner_instance,omitempty"`
	ApprovalID       string           `json:"approval_id,omitempty"`
	RuleID           string           `json:"rule_id,omitempty"`
	PermissionMode   string           `json:"permission_mode,omitempty"`
	ErrorCode        string           `json:"error_code,omitempty"`
	ChangeStatsKnown bool             `json:"change_stats_known,omitempty"`
	SchemaVersion    int              `json:"schema_version"`
	Seq              uint64           `json:"seq"`
	EventID          string           `json:"event_id"`
	CreatedAt        time.Time        `json:"created_at"`
	Binding
	Kind             string `json:"kind"`
	Status           string `json:"status,omitempty"`
	Title            string `json:"title,omitempty"`
	ToolName         string `json:"tool_name,omitempty"`
	SessionID        string `json:"session_id,omitempty"`
	Runtime          string `json:"runtime,omitempty"`
	Workdir          string `json:"workdir,omitempty"`
	ParameterSummary string `json:"parameter_summary,omitempty"`
	DisplayCommand   string `json:"display_command,omitempty"`
	ExitCode         *int   `json:"exit_code,omitempty"`
	CommandOK        *bool  `json:"command_ok,omitempty"`
	TimedOut         bool   `json:"timed_out,omitempty"`
	ElapsedMS        int64  `json:"elapsed_ms,omitempty"`
	OutputPreview    string `json:"output_preview,omitempty"`
	StderrPreview    string `json:"stderr_preview,omitempty"`
	StdoutTruncated  bool   `json:"stdout_truncated,omitempty"`
	StderrTruncated  bool   `json:"stderr_truncated,omitempty"`
	LogicalPath      string `json:"logical_path,omitempty"`
	ResolvedPath     string `json:"resolved_path,omitempty"`
	Insertions       int    `json:"insertions,omitempty"`
	Deletions        int    `json:"deletions,omitempty"`
	Summary          string `json:"summary,omitempty"`
}

type Query struct {
	MilestonesOnly bool
	ConversationID string
	CallID         string
	TaskID         string
	ThreadID       string
	After          uint64
	Limit          int
}

type Page struct {
	Events        []Event  `json:"events"`
	NextSeq       uint64   `json:"next_seq"`
	LatestSeq     uint64   `json:"latest_seq"`
	PrunedThrough uint64   `json:"pruned_through"`
	HasMore       bool     `json:"has_more"`
	Gap           bool     `json:"gap"`
	Warnings      []string `json:"warnings,omitempty"`
}

type bindingKey struct{}
type diagnosticKey struct{}

func WithDiagnostic(ctx context.Context) context.Context {
	return context.WithValue(ctx, diagnosticKey{}, true)
}
func IsDiagnostic(ctx context.Context) bool {
	value, _ := ctx.Value(diagnosticKey{}).(bool)
	return value
}
func WithExecutionScope(ctx context.Context, scope ExecutionScope) context.Context {
	return context.WithValue(ctx, bindingKey{}, scope)
}
func ExecutionScopeFromContext(ctx context.Context) ExecutionScope { return FromContext(ctx) }

func WithBinding(ctx context.Context, binding Binding) context.Context {
	return WithExecutionScope(ctx, binding)
}
func FromContext(ctx context.Context) Binding {
	binding, _ := ctx.Value(bindingKey{}).(Binding)
	return binding
}

func validKind(kind string) bool {
	parts := strings.Split(kind, ".")
	return len(parts) == 2 && identifier.MatchString(parts[0]) && identifier.MatchString(parts[1])
}
