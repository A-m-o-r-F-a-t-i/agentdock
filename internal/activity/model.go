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

type Binding struct {
	TaskID      string `json:"task_id,omitempty"`
	ThreadID    string `json:"thread_id,omitempty"`
	StepID      string `json:"step_id,omitempty"`
	WorkspaceID string `json:"workspace_id,omitempty"`
	Label       string `json:"activity_label,omitempty"`
}

var identifier = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)

func (b Binding) Validate() error {
	for _, id := range []string{b.TaskID, b.ThreadID, b.StepID, b.WorkspaceID} {
		if id != "" && !identifier.MatchString(id) {
			return errors.New("invalid activity binding identifier")
		}
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
	ChangeStatsKnown bool      `json:"change_stats_known,omitempty"`
	SchemaVersion    int       `json:"schema_version"`
	Seq              uint64    `json:"seq"`
	EventID          string    `json:"event_id"`
	CreatedAt        time.Time `json:"created_at"`
	Binding
	Kind            string `json:"kind"`
	Status          string `json:"status,omitempty"`
	Title           string `json:"title,omitempty"`
	ToolName        string `json:"tool_name,omitempty"`
	SessionID       string `json:"session_id,omitempty"`
	Runtime         string `json:"runtime,omitempty"`
	Workdir         string `json:"workdir,omitempty"`
	DisplayCommand  string `json:"display_command,omitempty"`
	ExitCode        *int   `json:"exit_code,omitempty"`
	CommandOK       *bool  `json:"command_ok,omitempty"`
	TimedOut        bool   `json:"timed_out,omitempty"`
	ElapsedMS       int64  `json:"elapsed_ms,omitempty"`
	OutputPreview   string `json:"output_preview,omitempty"`
	StderrPreview   string `json:"stderr_preview,omitempty"`
	StdoutTruncated bool   `json:"stdout_truncated,omitempty"`
	StderrTruncated bool   `json:"stderr_truncated,omitempty"`
	LogicalPath     string `json:"logical_path,omitempty"`
	ResolvedPath    string `json:"resolved_path,omitempty"`
	Insertions      int    `json:"insertions,omitempty"`
	Deletions       int    `json:"deletions,omitempty"`
	Summary         string `json:"summary,omitempty"`
}

type Query struct {
	TaskID   string
	ThreadID string
	After    uint64
	Limit    int
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

func WithBinding(ctx context.Context, binding Binding) context.Context {
	return context.WithValue(ctx, bindingKey{}, binding)
}
func FromContext(ctx context.Context) Binding {
	binding, _ := ctx.Value(bindingKey{}).(Binding)
	return binding
}

func validKind(kind string) bool {
	parts := strings.Split(kind, ".")
	return len(parts) == 2 && identifier.MatchString(parts[0]) && identifier.MatchString(parts[1])
}
