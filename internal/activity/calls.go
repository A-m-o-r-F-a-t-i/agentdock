package activity

import (
	"context"
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const ExecutionSchemaVersion = 2
const MaxCallPage = 200

var ErrCallNotFound = errors.New("execution call not found in retained history")

type FileChange struct {
	Path       string `json:"path"`
	Insertions int    `json:"insertions,omitempty"`
	Deletions  int    `json:"deletions,omitempty"`
	StatsKnown bool   `json:"stats_known"`
}
type ExecutionCall struct {
	LastActivityAt *time.Time `json:"last_activity_at,omitempty"`
	CallMeasurements
	FileEdit *FileEditDetails `json:"file_edit,omitempty"`
	CallManagement
	OwnerPID      int    `json:"owner_pid,omitempty"`
	OwnerInstance string `json:"owner_instance,omitempty"`
	Binding
	DisplayTitle      string       `json:"display_title"`
	StartedAt         time.Time    `json:"started_at"`
	ParameterSummary  string       `json:"parameter_summary,omitempty"`
	OutputSummary     string       `json:"output_summary,omitempty"`
	ErrorSummary      string       `json:"error_summary,omitempty"`
	TaskThreadID      string       `json:"task_thread_id,omitempty"`
	ReadOnlyLegacy    bool         `json:"read_only_legacy,omitempty"`
	SchemaVersion     int          `json:"schema_version"`
	Status            string       `json:"status"`
	Title             string       `json:"title"`
	ToolName          string       `json:"tool_name"`
	SessionID         string       `json:"session_id,omitempty"`
	Runtime           string       `json:"runtime,omitempty"`
	Workdir           string       `json:"workdir,omitempty"`
	DisplayCommand    string       `json:"display_command,omitempty"`
	Summary           string       `json:"summary,omitempty"`
	ApprovalID        string       `json:"approval_id,omitempty"`
	RuleID            string       `json:"rule_id,omitempty"`
	PermissionMode    string       `json:"permission_mode,omitempty"`
	ErrorCode         string       `json:"error_code,omitempty"`
	CreatedAt         time.Time    `json:"created_at"`
	UpdatedAt         time.Time    `json:"updated_at"`
	CompletedAt       *time.Time   `json:"completed_at,omitempty"`
	CreatedSeq        uint64       `json:"created_seq"`
	UpdatedSeq        uint64       `json:"updated_seq"`
	ElapsedMS         int64        `json:"elapsed_ms,omitempty"`
	EventCount        int          `json:"event_count"`
	ExitCode          *int         `json:"exit_code,omitempty"`
	CommandOK         *bool        `json:"command_ok,omitempty"`
	TimedOut          bool         `json:"timed_out,omitempty"`
	HasOutput         bool         `json:"has_output,omitempty"`
	OutputPreview     string       `json:"output_preview,omitempty"`
	StderrPreview     string       `json:"stderr_preview,omitempty"`
	StdoutTruncated   bool         `json:"stdout_truncated,omitempty"`
	StderrTruncated   bool         `json:"stderr_truncated,omitempty"`
	FileChanges       []FileChange `json:"file_changes,omitempty"`
	ChangesTruncated  bool         `json:"changes_truncated,omitempty"`
	HistoryIncomplete bool         `json:"history_incomplete,omitempty"`
	Legacy            bool         `json:"legacy,omitempty"`
}
type CallQuery struct {
	View              string
	ConversationID    string
	TaskID            string
	ThreadID          string
	ParentCallID      string
	Status            string
	Search            string
	Before            uint64
	After             uint64
	Updates           bool
	Limit             int
	IncludeDiagnostic bool
	IncludeOutput     bool
	Unattributed      bool
	TopLevel          bool
}
type CallPage struct {
	Calls         []ExecutionCall `json:"calls"`
	NextBefore    uint64          `json:"next_before,omitempty"`
	NextSeq       uint64          `json:"next_seq"`
	LatestSeq     uint64          `json:"latest_seq"`
	PrunedThrough uint64          `json:"pruned_through"`
	HasMore       bool            `json:"has_more"`
	Gap           bool            `json:"gap"`
	Reset         bool            `json:"reset,omitempty"`
	Warnings      []string        `json:"warnings,omitempty"`
}
type CallStats struct {
	LastActivityAt  *time.Time `json:"last_activity_at,omitempty"`
	LastToolCallAt  *time.Time `json:"last_tool_call_at,omitempty"`
	Total           int        `json:"total"`
	Running         int        `json:"running"`
	Pending         int        `json:"pending"`
	Succeeded       int        `json:"succeeded"`
	Partial         int        `json:"partial"`
	Failed          int        `json:"failed"`
	Cancelled       int        `json:"cancelled"`
	Unknown         int        `json:"unknown"`
	DurationSamples int        `json:"duration_samples"`
	TotalElapsedMS  int64      `json:"total_elapsed_ms"`
	P50ElapsedMS    *int64     `json:"p50_elapsed_ms,omitempty"`
	P95ElapsedMS    *int64     `json:"p95_elapsed_ms,omitempty"`
	LatestAt        time.Time  `json:"latest_at"`
}
type callProjection struct {
	seq            uint64
	pruned         uint64
	calls          map[string]*ExecutionCall
	conversations  map[string]map[string]bool
	tasks          map[string]map[string]bool
	legacySessions map[string]*legacySession
	warnings       []string
	gap            bool
}

func newCallProjection() *callProjection {
	return &callProjection{calls: map[string]*ExecutionCall{}, conversations: map[string]map[string]bool{}, tasks: map[string]map[string]bool{}, legacySessions: map[string]*legacySession{}}
}
func CallTerminal(status string) bool {
	switch status {
	case "succeeded", "partial", "failed", "cancelled", "unknown":
		return true
	}
	return false
}
func normalizedCallStatus(status string, ok *bool) string {
	switch status {
	case "created", "pending_approval", "running", "succeeded", "partial", "failed", "cancelled", "unknown":
		return status
	case "success":
		return "succeeded"
	case "killed", "terminated":
		return "cancelled"
	case "timeout", "error":
		return "failed"
	case "exited":
		if ok != nil && *ok {
			return "succeeded"
		}
		return "failed"
	}
	return ""
}
func (p *callProjection) warning(text string) {
	p.gap = true
	for _, existing := range p.warnings {
		if existing == text {
			return
		}
	}
	if len(p.warnings) < 8 {
		p.warnings = append(p.warnings, text)
	}
}
func callIndexAdd(index map[string]map[string]bool, key, id string) {
	if index[key] == nil {
		index[key] = map[string]bool{}
	}
	index[key][id] = true
}
func (p *callProjection) apply(event Event) {
	if event.Seq <= p.seq {
		return
	}
	if p.seq != 0 && event.Seq > p.seq+1 {
		p.warning("Some activity sequence numbers are missing; the retained history is incomplete.")
	}
	p.seq = event.Seq
	id, legacy, ambiguous := event.CallID, false, false
	if id == "" {
		id, ambiguous = p.legacyIdentity(event)
		if id == "" {
			return
		}
		legacy = true
		event.ConversationID, event.ParentCallID = "", ""
		event.BindingQuality = "unattributed"
	}
	call, exists := p.calls[id]
	if !exists {
		call = &ExecutionCall{SchemaVersion: ExecutionSchemaVersion, Binding: event.Binding, ToolName: event.ToolName, Title: event.Title, Status: "created", CreatedAt: event.CreatedAt, CreatedSeq: event.Seq, Legacy: legacy}
		call.CallID = id
		call.StartedAt, call.DisplayTitle, call.ReadOnlyLegacy = event.CreatedAt, event.Title, legacy
		if call.BindingQuality == "" {
			call.BindingQuality = "unattributed"
		}
		if call.Visibility == "" {
			call.Visibility = "normal"
		}
		call.HistoryIncomplete = ambiguous || (event.Kind != "call.created" && event.Kind != "call.started" && event.Kind != "command.started" && event.Kind != "tool.started")
		p.calls[id] = call
	}
	oldConversation, oldTask := call.ConversationID, call.TaskID
	// A created task or resolved branch may become known after the first event.
	// Nonempty identities never migrate between conversations or tasks.
	if call.ConversationID != "" && event.ConversationID != "" && call.ConversationID != event.ConversationID || call.TaskID != "" && event.TaskID != "" && call.TaskID != event.TaskID {
		call.HistoryIncomplete = true
		p.warning("Conflicting call bindings were rejected while rebuilding execution history.")
		return
	}
	if call.ConversationID == "" {
		call.ConversationID = event.ConversationID
	}
	if call.TaskID == "" {
		call.TaskID = event.TaskID
	}
	if call.ThreadID != "" && event.ThreadID != "" && call.ThreadID != event.ThreadID || call.WorkspaceID != "" && event.WorkspaceID != "" && call.WorkspaceID != event.WorkspaceID {
		call.HistoryIncomplete = true
		p.warning("Conflicting task-thread or workspace bindings were rejected while rebuilding execution history.")
		return
	}
	if call.ThreadID == "" {
		call.ThreadID = event.ThreadID
	}
	if call.StepID == "" {
		call.StepID = event.StepID
	}
	if call.WorkspaceID == "" {
		call.WorkspaceID = event.WorkspaceID
	}
	if call.ParentCallID == "" {
		call.ParentCallID = event.ParentCallID
	}
	if call.RetryOfCallID == "" {
		call.RetryOfCallID = event.RetryOfCallID
	}
	if call.Label == "" {
		call.Label = event.Label
	}
	if call.Title == "" || event.Kind == "call.bound" {
		call.Title = event.Title
	}
	if call.ToolName == "" {
		call.ToolName = event.ToolName
	}
	if oldConversation != call.ConversationID {
		delete(p.conversations[oldConversation], id)
	}
	if oldTask != call.TaskID {
		delete(p.tasks[oldTask], id)
	}
	callIndexAdd(p.conversations, call.ConversationID, id)
	callIndexAdd(p.tasks, call.TaskID, id)
	call.DisplayTitle, call.TaskThreadID = call.Title, call.ThreadID
	if event.ParameterSummary != "" {
		call.ParameterSummary = event.ParameterSummary
	}
	if event.BindingQuality != "" {
		call.BindingQuality = event.BindingQuality
	}
	if event.Visibility != "" {
		call.Visibility = event.Visibility
	}
	call.UpdatedAt, call.UpdatedSeq = event.CreatedAt, event.Seq
	applyMeasurements(call, event)
	// Only genuine external-root execution facts advance activity. Projection reads,
	// metadata edits and recovery events must not restart an activity window.
	if call.RequestReceivedAt != nil && call.ParentCallID == "" && call.Visibility != "diagnostic" && genuineActivityEvent(event.Kind) {
		if call.LastActivityAt == nil || event.CreatedAt.After(*call.LastActivityAt) {
			call.LastActivityAt = copyValue(&event.CreatedAt)
		}
	}
	call.EventCount++
	if call.OwnerPID == 0 {
		call.OwnerPID = event.OwnerPID
		call.OwnerInstance = event.OwnerInstance
	}
	if event.SessionID != "" {
		call.SessionID = event.SessionID
	}
	if event.Runtime != "" {
		call.Runtime = event.Runtime
	}
	if event.Workdir != "" {
		call.Workdir = event.Workdir
	}
	if event.DisplayCommand != "" {
		call.DisplayCommand = event.DisplayCommand
	}
	if event.ApprovalID != "" {
		call.ApprovalID = event.ApprovalID
	}
	if event.RuleID != "" {
		call.RuleID = event.RuleID
	}
	if event.PermissionMode != "" {
		call.PermissionMode = event.PermissionMode
	}
	if event.ErrorCode != "" {
		call.ErrorCode = event.ErrorCode
	}
	if event.ElapsedMS > call.ElapsedMS {
		call.ElapsedMS = event.ElapsedMS
	}
	if event.Summary != "" && (call.Summary == "" || strings.HasPrefix(event.Kind, "call.") || event.Kind == "tool.completed" || event.Kind == "command.completed") {
		call.Summary = event.Summary
	}
	if event.ExitCode != nil {
		value := *event.ExitCode
		call.ExitCode = &value
	}
	if event.CommandOK != nil {
		value := *event.CommandOK
		call.CommandOK = &value
	}
	call.TimedOut = call.TimedOut || event.TimedOut
	if event.OutputPreview != "" {
		call.OutputPreview, call.StdoutTruncated = appendCallOutput(call.OutputPreview, event.OutputPreview, call.StdoutTruncated)
	}
	if event.StderrPreview != "" {
		call.StderrPreview, call.StderrTruncated = appendCallOutput(call.StderrPreview, event.StderrPreview, call.StderrTruncated)
	}
	call.StdoutTruncated = call.StdoutTruncated || event.StdoutTruncated
	call.StderrTruncated = call.StderrTruncated || event.StderrTruncated
	call.HasOutput = call.OutputPreview != "" || call.StderrPreview != "" || call.StdoutTruncated || call.StderrTruncated
	if event.Kind == "file.changed" {
		if len(call.FileChanges) < 128 {
			call.FileChanges = append(call.FileChanges, FileChange{Path: event.ResolvedPath, Insertions: event.Insertions, Deletions: event.Deletions, StatsKnown: event.ChangeStatsKnown})
		} else {
			call.ChangesTruncated = true
		}
	}
	var status string
	switch event.Kind {
	case "call.created":
		status = "created"
	case "call.pending":
		status = "pending_approval"
	case "call.started", "tool.started", "command.started":
		status = "running"
	case "call.completed", "call.recovered", "tool.completed", "command.completed":
		status = normalizedCallStatus(event.Status, event.CommandOK)
	}
	if legacy && (ambiguous || !CallTerminal(status)) {
		status = "unknown"
	}
	// An asynchronous return or late start event must not resurrect completion.
	if status != "" && (!CallTerminal(call.Status) || CallTerminal(status)) {
		call.Status = status
		if CallTerminal(status) {
			call.OutputSummary = call.Summary
			if status == "failed" || status == "unknown" {
				call.ErrorSummary = call.Summary
			}
			completed := event.CreatedAt
			call.CompletedAt = &completed
		}
	}
}
func appendCallOutput(previous, next string, truncated bool) (string, bool) {
	value := previous + next
	if len(value) <= MaxPreviewBytes {
		return value, truncated
	}
	start := len(value) - MaxPreviewBytes
	for start < len(value) && !utf8.RuneStart(value[start]) {
		start++
	}
	return value[start:], true
}
func (p *callProjection) prune(before uint64) {
	if before <= p.pruned {
		return
	}
	p.pruned = before
	for id, call := range p.calls {
		if call.CreatedSeq <= before {
			call.HistoryIncomplete = true
		}
		if call.UpdatedSeq <= before && CallTerminal(call.Status) {
			delete(p.conversations[call.ConversationID], id)
			delete(p.tasks[call.TaskID], id)
			delete(p.calls, id)
		}
	}
}
func (s *Store) projectionLocked(ctx context.Context) (*callProjection, error) {
	files, err := s.segments()
	if err != nil {
		return nil, err
	}
	state, err := s.state(files)
	if err != nil {
		return nil, err
	}
	if s.projection != nil && s.projection.seq == state.Seq {
		s.projection.prune(state.PrunedThrough)
		if err := s.applyCallManagementLocked(s.projection); err != nil {
			return nil, err
		}
		return s.projection, nil
	}
	projection := newCallProjection()
	projection.pruned, projection.seq = state.PrunedThrough, state.PrunedThrough
	for _, path := range files {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		damaged := false
		err = scanEvents(path, func(event Event) bool {
			if event.Seq > state.PrunedThrough {
				projection.apply(event)
			}
			return ctx.Err() == nil
		}, &damaged)
		if err != nil {
			return nil, err
		}
		if damaged {
			projection.warning("Skipped damaged records in " + filepath.Base(path) + "; the retained history is incomplete.")
		}
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if projection.seq < state.Seq {
		projection.warning("The journal reserved sequence numbers without a corresponding retained event.")
	}
	projection.seq = state.Seq
	if err := s.applyCallManagementLocked(projection); err != nil {
		return nil, err
	}
	s.projection = projection
	return projection, nil
}
func (s *Store) projectAppendedLocked(event Event) {
	if s.projection == nil {
		return
	}
	if s.projection.seq+1 != event.Seq {
		s.projection = nil
		return
	}
	s.projection.apply(event)
}
func cloneCall(call *ExecutionCall, output bool) ExecutionCall {
	copied := *call
	copied.CallMeasurements = call.CallMeasurements.clone()
	copied.LastActivityAt = copyValue(call.LastActivityAt)
	copied.FileEdit = call.FileEdit.clone(output)
	copied.FileChanges = append([]FileChange(nil), call.FileChanges...)
	if !output {
		copied.OutputPreview, copied.StderrPreview = "", ""
	}
	return copied
}
func (s *Store) Call(ctx context.Context, id string) (ExecutionCall, error) {
	if !identifier.MatchString(id) {
		return ExecutionCall{}, errors.New("invalid call_id")
	}
	release, err := s.lock(ctx)
	if err != nil {
		return ExecutionCall{}, err
	}
	defer release()
	projection, err := s.projectionLocked(ctx)
	if err != nil {
		return ExecutionCall{}, err
	}
	call, found := projection.calls[id]
	if !found || call.DeletedAt != nil {
		return ExecutionCall{}, ErrCallNotFound
	}
	return cloneCall(call, true), nil
}
func callMatches(call *ExecutionCall, query CallQuery) bool {
	if !query.Updates && !callManagementMatches(call.CallManagement, query.View) {
		return false
	}
	if call.Visibility == "diagnostic" && !query.IncludeDiagnostic {
		return false
	}
	if query.ConversationID != "" && call.ConversationID != query.ConversationID || query.TaskID != "" && call.TaskID != query.TaskID || query.ThreadID != "" && call.ThreadID != query.ThreadID {
		return false
	}
	if query.Unattributed && call.ConversationID != "" || query.TopLevel && call.ParentCallID != "" || query.ParentCallID != "" && call.ParentCallID != query.ParentCallID {
		return false
	}
	if query.Status == "attention" {
		if call.Status != "pending_approval" && call.Status != "failed" && call.Status != "unknown" {
			return false
		}
	} else if !query.Updates && query.Status != "" && call.Status != query.Status {
		return false
	}
	if query.Before > 0 && call.CreatedSeq >= query.Before || query.Updates && call.UpdatedSeq <= query.After {
		return false
	}
	if query.Search != "" && !strings.Contains(strings.ToLower(call.Title+" "+call.ToolName+" "+call.DisplayCommand+" "+call.Summary+" "+call.Workdir+" "+call.ParameterSummary), strings.ToLower(query.Search)) {
		return false
	}
	return true
}
func (s *Store) Calls(ctx context.Context, query CallQuery) (CallPage, error) {
	page := CallPage{Calls: []ExecutionCall{}}
	if err := (Binding{ConversationID: query.ConversationID, TaskID: query.TaskID, ThreadID: query.ThreadID, ParentCallID: query.ParentCallID}).Validate(); err != nil {
		return page, err
	}
	if query.Limit <= 0 || query.Limit > MaxCallPage {
		query.Limit = 100
	}
	if query.View != "" && query.View != "active" && query.View != "all" && query.View != "archived" && query.View != "isolated" && query.View != "trash" {
		return page, errors.New("invalid call view")
	}
	if len(query.Search) > 512 {
		return page, errors.New("search exceeds 512 bytes")
	}
	release, err := s.lock(ctx)
	if err != nil {
		return page, err
	}
	defer release()
	projection, err := s.projectionLocked(ctx)
	if err != nil {
		return page, err
	}
	page.LatestSeq, page.PrunedThrough = projection.seq, projection.pruned
	page.Gap = projection.gap || (query.Updates && query.After < projection.pruned) || (!query.Updates && projection.pruned > 0)
	page.Warnings = append([]string(nil), projection.warnings...)
	page.Reset = query.Updates && query.After > projection.seq
	page.NextSeq = projection.seq
	if query.Updates && query.After >= projection.seq {
		return page, nil
	}
	candidates := make([]*ExecutionCall, 0)
	add := func(call *ExecutionCall) {
		if call != nil && callMatches(call, query) {
			candidates = append(candidates, call)
		}
	}
	switch {
	case query.ConversationID != "":
		for id := range projection.conversations[query.ConversationID] {
			add(projection.calls[id])
		}
	case query.Unattributed:
		for id := range projection.conversations[""] {
			add(projection.calls[id])
		}
	case query.TaskID != "":
		for id := range projection.tasks[query.TaskID] {
			add(projection.calls[id])
		}
	default:
		for _, call := range projection.calls {
			add(call)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if query.Updates {
			return candidates[i].UpdatedSeq < candidates[j].UpdatedSeq
		}
		return candidates[i].CreatedSeq > candidates[j].CreatedSeq
	})
	page.HasMore = len(candidates) > query.Limit
	if page.HasMore {
		candidates = candidates[:query.Limit]
	}
	for _, call := range candidates {
		page.Calls = append(page.Calls, cloneCall(call, query.IncludeOutput))
	}
	if len(candidates) > 0 {
		page.NextBefore = candidates[len(candidates)-1].CreatedSeq
		if query.Updates && page.HasMore {
			page.NextSeq = candidates[len(candidates)-1].UpdatedSeq
		}
	}
	return page, nil
}

type callStatsAccumulator struct {
	stats     CallStats
	durations []int64
}

func (accumulator *callStatsAccumulator) add(call *ExecutionCall) {
	if call.ParentCallID != "" || call.Visibility == "diagnostic" {
		return
	}
	stats := &accumulator.stats
	stats.Total++
	if call.RequestReceivedAt != nil && (stats.LastToolCallAt == nil || call.RequestReceivedAt.After(*stats.LastToolCallAt)) {
		stats.LastToolCallAt = copyValue(call.RequestReceivedAt)
	}
	if call.LastActivityAt != nil && (stats.LastActivityAt == nil || call.LastActivityAt.After(*stats.LastActivityAt)) {
		stats.LastActivityAt = copyValue(call.LastActivityAt)
	}
	if call.UpdatedAt.After(stats.LatestAt) {
		stats.LatestAt = call.UpdatedAt
	}
	switch call.Status {
	case "created", "running":
		stats.Running++
	case "pending_approval":
		stats.Pending++
	case "succeeded":
		stats.Succeeded++
	case "partial":
		stats.Partial++
	case "failed":
		stats.Failed++
	case "cancelled":
		stats.Cancelled++
	case "unknown":
		stats.Unknown++
	}
	if !CallTerminal(call.Status) {
		return
	}
	if call.OperationElapsedMS != nil {
		accumulator.durations = append(accumulator.durations, *call.OperationElapsedMS)
		return
	}
	if call.ElapsedMS > 0 {
		accumulator.durations = append(accumulator.durations, call.ElapsedMS)
	}
}

func (accumulator *callStatsAccumulator) result() CallStats {
	stats := accumulator.stats
	if len(accumulator.durations) == 0 {
		return stats
	}
	durations := append([]int64(nil), accumulator.durations...)
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	for _, duration := range durations {
		stats.TotalElapsedMS += duration
	}
	stats.DurationSamples = len(durations)
	stats.P50ElapsedMS = copyValue(&durations[percentileIndex(len(durations), 50)])
	stats.P95ElapsedMS = copyValue(&durations[percentileIndex(len(durations), 95)])
	return stats
}

func percentileIndex(count, percentile int) int {
	index := (count*percentile + 99) / 100
	if index < 1 {
		index = 1
	}
	return min(index-1, count-1)
}

func (s *Store) CallStatistics(ctx context.Context) (CallStats, map[string]CallStats, error) {
	total := callStatsAccumulator{}
	byConversation := map[string]*callStatsAccumulator{}
	release, err := s.lock(ctx)
	if err != nil {
		return total.stats, map[string]CallStats{}, err
	}
	defer release()
	projection, err := s.projectionLocked(ctx)
	if err != nil {
		return total.stats, map[string]CallStats{}, err
	}
	for _, call := range projection.calls {
		if call.Visibility == "diagnostic" || !callManagementMatches(call.CallManagement, "active") {
			continue
		}
		total.add(call)
		stats := byConversation[call.ConversationID]
		if stats == nil {
			stats = &callStatsAccumulator{}
			byConversation[call.ConversationID] = stats
		}
		stats.add(call)
	}
	result := make(map[string]CallStats, len(byConversation))
	for conversationID, stats := range byConversation {
		result[conversationID] = stats.result()
	}
	return total.result(), result, nil
}
