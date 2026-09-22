package activity

import "time"

// CallMeasurements are measured intervals, not wall-clock reconstructions.
// Nil means that the producer did not measure the interval.
type CallMeasurements struct {
	RequestReceivedAt  *time.Time `json:"request_received_at,omitempty"`
	RPCCompletedAt     *time.Time `json:"rpc_completed_at,omitempty"`
	RPCElapsedMS       *int64     `json:"rpc_elapsed_ms,omitempty"`
	RPCStatus          string     `json:"rpc_status,omitempty"`
	ExecutionElapsedMS *int64     `json:"execution_elapsed_ms,omitempty"`
	WaitElapsedMS      *int64     `json:"wait_elapsed_ms,omitempty"`
	ApprovalWaitMS     *int64     `json:"approval_wait_ms,omitempty"`
	OperationElapsedMS *int64     `json:"operation_elapsed_ms,omitempty"`
	ProcessElapsedMS   *int64     `json:"process_elapsed_ms,omitempty"`
}

const MaxRecordedAffectedFiles = 16

type AffectedFile struct {
	Path      string `json:"path"`
	Operation string `json:"operation,omitempty"`
	MoveTo    string `json:"move_to,omitempty"`
}

// AffectedFiles can describe planned edits when DryRun is true. Changed must
// never claim that preview statistics were actually written to disk.
type FileEditDetails struct {
	Action         string         `json:"action"`
	Path           string         `json:"path,omitempty"`
	NewPath        string         `json:"new_path,omitempty"`
	DryRun         bool           `json:"dry_run"`
	Recursive      bool           `json:"recursive,omitempty"`
	Executed       bool           `json:"executed"`
	Changed        *bool          `json:"changed,omitempty"`
	AffectedFiles  []AffectedFile `json:"affected_files,omitempty"`
	AffectedCount  *int           `json:"affected_count,omitempty"`
	FilesTruncated bool           `json:"files_truncated,omitempty"`
	Insertions     *int           `json:"insertions,omitempty"`
	Deletions      *int           `json:"deletions,omitempty"`
	DiffPreview    string         `json:"diff_preview,omitempty"`
	DiffTruncated  bool           `json:"diff_truncated,omitempty"`
}

func copyValue[T any](value *T) *T {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func (m CallMeasurements) clone() CallMeasurements {
	m.RequestReceivedAt = copyValue(m.RequestReceivedAt)
	m.RPCCompletedAt = copyValue(m.RPCCompletedAt)
	m.RPCElapsedMS = copyValue(m.RPCElapsedMS)
	m.ExecutionElapsedMS = copyValue(m.ExecutionElapsedMS)
	m.WaitElapsedMS = copyValue(m.WaitElapsedMS)
	m.ApprovalWaitMS = copyValue(m.ApprovalWaitMS)
	m.OperationElapsedMS = copyValue(m.OperationElapsedMS)
	m.ProcessElapsedMS = copyValue(m.ProcessElapsedMS)
	return m
}

func (details *FileEditDetails) clone(preview bool) *FileEditDetails {
	if details == nil {
		return nil
	}
	copy := *details
	copy.Changed = copyValue(details.Changed)
	copy.AffectedCount = copyValue(details.AffectedCount)
	copy.Insertions, copy.Deletions = copyValue(details.Insertions), copyValue(details.Deletions)
	copy.AffectedFiles = append([]AffectedFile(nil), details.AffectedFiles...)
	if !preview {
		copy.DiffPreview = ""
	}
	return &copy
}

func applyMeasurements(call *ExecutionCall, event Event) {
	if event.Kind == "call.created" && call.RequestReceivedAt == nil {
		call.RequestReceivedAt = copyValue(event.RequestReceivedAt)
	}
	if event.Kind == "call.rpc_returned" && call.RPCCompletedAt == nil {
		call.RPCCompletedAt = copyValue(event.RPCCompletedAt)
		call.RPCElapsedMS = copyValue(event.RPCElapsedMS)
		call.RPCStatus = event.RPCStatus
	}
	if event.ExecutionElapsedMS != nil {
		call.ExecutionElapsedMS = copyValue(event.ExecutionElapsedMS)
	}
	if event.WaitElapsedMS != nil {
		call.WaitElapsedMS = copyValue(event.WaitElapsedMS)
	}
	if event.ApprovalWaitMS != nil {
		call.ApprovalWaitMS = copyValue(event.ApprovalWaitMS)
	}
	if event.OperationElapsedMS != nil {
		call.OperationElapsedMS = copyValue(event.OperationElapsedMS)
	}
	if event.Kind == "command.completed" {
		call.ProcessElapsedMS = copyValue(&event.ElapsedMS)
	}
	if event.FileEdit != nil {
		call.FileEdit = event.FileEdit.clone(true)
	}
}

// RecentlyActive uses a half-open server-time interval; tests need no sleeps.
func RecentlyActive(last *time.Time, now time.Time, terminated bool) bool {
	return !terminated && last != nil && !last.IsZero() && !now.Before(*last) && now.Sub(*last) < 120*time.Second
}

func genuineActivityEvent(kind string) bool {
	switch kind {
	case "call.created", "call.bound", "call.pending", "call.started", "call.completed", "call.rpc_returned",
		"command.started", "command.output", "command.completed", "tool.started", "tool.completed", "file.requested", "file.changed":
		return true
	}
	return false
}
