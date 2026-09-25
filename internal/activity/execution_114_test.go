package activity

import (
	"context"
	"testing"
	"time"
)

func Test114RecentlyActiveBoundary(t *testing.T) {
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		last       *time.Time
		terminated bool
		want       bool
	}{
		{name: "missing", want: false},
		{name: "zero", last: timePointerForTest(time.Time{}), want: false},
		{name: "now", last: timePointerForTest(now), want: true},
		{name: "29.9 seconds", last: timePointerForTest(now.Add(-29900 * time.Millisecond)), want: true},
		{name: "30 seconds remains active", last: timePointerForTest(now.Add(-30 * time.Second)), want: true},
		{name: "119.999 seconds", last: timePointerForTest(now.Add(-119999 * time.Millisecond)), want: true},
		{name: "120 seconds", last: timePointerForTest(now.Add(-120 * time.Second)), want: false},
		{name: "120.001 seconds", last: timePointerForTest(now.Add(-120001 * time.Millisecond)), want: false},
		{name: "future", last: timePointerForTest(now.Add(time.Millisecond)), want: false},
		{name: "terminated", last: timePointerForTest(now), terminated: true, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := RecentlyActive(test.last, now, test.terminated); got != test.want {
				t.Fatalf("RecentlyActive() = %v, want %v", got, test.want)
			}
		})
	}
}

func Test114CallStatisticsUseTopLevelTerminalDurations(t *testing.T) {
	store, err := New(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	conversationID, _ := NewExecutionID("conv_")
	base := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	statuses := []string{"succeeded", "failed", "cancelled", "partial", "unknown"}
	durations := []int64{10, 20, 30, 40, 50}
	for index, status := range statuses {
		callID, _ := NewExecutionID("call_")
		received := base.Add(time.Duration(index) * time.Second)
		binding := Binding{ConversationID: conversationID, CallID: callID}
		if _, err = store.Append(ctx, Event{Binding: binding, Kind: "call.created", Status: "created", ToolName: "read_file", CallMeasurements: CallMeasurements{RequestReceivedAt: &received}}); err != nil {
			t.Fatal(err)
		}
		duration := durations[index]
		if _, err = store.Append(ctx, Event{Binding: binding, Kind: "call.completed", Status: status, ToolName: "read_file", ElapsedMS: duration, CallMeasurements: CallMeasurements{OperationElapsedMS: &duration}}); err != nil {
			t.Fatal(err)
		}
	}
	runningID, _ := NewExecutionID("call_")
	runningAt := base.Add(6 * time.Second)
	if _, err = store.Append(ctx, Event{Binding: Binding{ConversationID: conversationID, CallID: runningID}, Kind: "call.created", Status: "created", ToolName: "exec_command", CallMeasurements: CallMeasurements{RequestReceivedAt: &runningAt}}); err != nil {
		t.Fatal(err)
	}
	childID, _ := NewExecutionID("call_")
	childDuration := int64(999)
	if _, err = store.Append(ctx, Event{Binding: Binding{ConversationID: conversationID, CallID: childID, ParentCallID: runningID}, Kind: "call.created", Status: "created", ToolName: "child"}); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Append(ctx, Event{Binding: Binding{ConversationID: conversationID, CallID: childID, ParentCallID: runningID}, Kind: "call.completed", Status: "succeeded", ToolName: "child", ElapsedMS: childDuration, CallMeasurements: CallMeasurements{OperationElapsedMS: &childDuration}}); err != nil {
		t.Fatal(err)
	}
	diagnosticID, _ := NewExecutionID("call_")
	if _, err = store.Append(ctx, Event{Binding: Binding{ConversationID: conversationID, CallID: diagnosticID, Visibility: "diagnostic"}, Kind: "call.created", Status: "created", ToolName: "probe"}); err != nil {
		t.Fatal(err)
	}

	total, byConversation, err := store.CallStatistics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	stats := byConversation[conversationID]
	for _, candidate := range []CallStats{total, stats} {
		if candidate.Total != 6 || candidate.Running != 1 || candidate.Succeeded != 1 || candidate.Failed != 1 || candidate.Cancelled != 1 || candidate.Partial != 1 || candidate.Unknown != 1 {
			t.Fatalf("unexpected state counts: %+v", candidate)
		}
		if candidate.DurationSamples != 5 || candidate.TotalElapsedMS != 150 || candidate.P50ElapsedMS == nil || *candidate.P50ElapsedMS != 30 || candidate.P95ElapsedMS == nil || *candidate.P95ElapsedMS != 50 {
			t.Fatalf("unexpected duration statistics: %+v", candidate)
		}
		if candidate.LastToolCallAt == nil || !candidate.LastToolCallAt.Equal(runningAt) {
			t.Fatalf("last tool activity = %v, want %v", candidate.LastToolCallAt, runningAt)
		}
	}
}

func TestWB04InteractionStopsAtRPCReturnWhileExecutionContinues(t *testing.T) {
	projection := newCallProjection()
	base := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	binding := Binding{ConversationID: "conv_daemon", CallID: "call_daemon", WorkspaceID: "wsp_daemon"}
	sequence := uint64(0)
	apply := func(at time.Time, event Event) {
		sequence++
		event.Seq, event.CreatedAt = sequence, at
		projection.apply(event)
	}
	received := base
	apply(base, Event{Binding: binding, Kind: "call.created", Status: "created", ToolName: "exec_command", CallMeasurements: CallMeasurements{RequestReceivedAt: &received}})
	apply(base.Add(20*time.Millisecond), Event{Binding: binding, Kind: "call.pending", Status: "pending_approval", ToolName: "exec_command"})
	rpcReturned := base.Add(232 * time.Millisecond)
	rpcElapsed := int64(232)
	apply(rpcReturned, Event{Binding: binding, Kind: "call.rpc_returned", ToolName: "exec_command", CallMeasurements: CallMeasurements{RPCCompletedAt: &rpcReturned, RPCElapsedMS: &rpcElapsed, RPCStatus: "pending_approval"}})

	// Approval continuation, metadata repair and reconnect/recovery occur after
	// the original RPC returned. They preserve liveness/history but never renew
	// the two-minute external-interaction window.
	apply(base.Add(10*time.Minute), Event{Binding: binding, Kind: "command.started", Status: "running", ToolName: "exec_command"})
	apply(base.Add(11*time.Minute), Event{Binding: binding, Kind: "call.bound", ToolName: "exec_command"})
	apply(base.Add(12*time.Minute), Event{Binding: binding, Kind: "call.recovered", ToolName: "exec_command"})
	outputAt := base.Add(20 * time.Minute)
	apply(outputAt, Event{Binding: binding, Kind: "command.output", Status: "running", ToolName: "exec_command", Output: "still alive"})
	completedAt := outputAt.Add(5 * time.Second)
	processElapsed := int64((20*time.Minute + 5*time.Second) / time.Millisecond)
	apply(completedAt, Event{Binding: binding, Kind: "command.completed", Status: "succeeded", ToolName: "exec_command", ElapsedMS: processElapsed})

	call := projection.calls[binding.CallID]
	if call == nil {
		t.Fatal("daemon call was not projected")
	}
	if call.LastInteractionAt == nil || !call.LastInteractionAt.Equal(rpcReturned) {
		t.Fatalf("late execution renewed interaction: got=%v want=%v", call.LastInteractionAt, rpcReturned)
	}
	if call.LastActivityAt == nil || !call.LastActivityAt.Equal(completedAt) {
		t.Fatalf("execution activity stopped at RPC return: got=%v want=%v", call.LastActivityAt, completedAt)
	}
	if call.RPCElapsedMS == nil || *call.RPCElapsedMS != 232 || call.ProcessElapsedMS == nil || *call.ProcessElapsedMS != processElapsed {
		t.Fatalf("RPC/process timing collapsed: rpc=%v process=%v", call.RPCElapsedMS, call.ProcessElapsedMS)
	}
	if !RecentlyActive(call.LastInteractionAt, rpcReturned.Add(119999*time.Millisecond), false) ||
		RecentlyActive(call.LastInteractionAt, rpcReturned.Add(120*time.Second), false) {
		t.Fatal("interaction boundary is not half-open at 120 seconds")
	}

	// A later independent root request in the same conversation becomes the
	// conversation's newest interaction. A five-minute synchronous RPC uses its
	// actual return, not its request or an unrelated background completion.
	syncBinding := Binding{ConversationID: binding.ConversationID, CallID: "call_sync", WorkspaceID: binding.WorkspaceID}
	syncReceived := base.Add(time.Hour)
	apply(syncReceived, Event{Binding: syncBinding, Kind: "call.created", Status: "created", ToolName: "read_file", CallMeasurements: CallMeasurements{RequestReceivedAt: &syncReceived}})
	syncReturned := syncReceived.Add(5 * time.Minute)
	syncElapsed := int64((5 * time.Minute) / time.Millisecond)
	apply(syncReturned, Event{Binding: syncBinding, Kind: "call.rpc_returned", ToolName: "read_file", CallMeasurements: CallMeasurements{RPCCompletedAt: &syncReturned, RPCElapsedMS: &syncElapsed, RPCStatus: "succeeded"}})
	apply(syncReturned, Event{Binding: syncBinding, Kind: "call.completed", Status: "succeeded", ToolName: "read_file"})
	syncCall := projection.calls[syncBinding.CallID]
	if syncCall.LastInteractionAt == nil || !syncCall.LastInteractionAt.Equal(syncReturned) {
		t.Fatalf("synchronous RPC return was not the interaction boundary: %v", syncCall.LastInteractionAt)
	}

	stats := callStatsAccumulator{}
	for _, projected := range projection.calls {
		stats.add(projected)
	}
	conversation := stats.result()
	if conversation.LastInteractionAt == nil || !conversation.LastInteractionAt.Equal(syncReturned) {
		t.Fatalf("new root did not advance conversation interaction: %v", conversation.LastInteractionAt)
	}
	if conversation.LastActivityAt == nil || !conversation.LastActivityAt.Equal(syncReturned) {
		t.Fatalf("conversation execution activity is inconsistent: %v", conversation.LastActivityAt)
	}
}

func Test114FileEditProjectionKeepsBoundedDetails(t *testing.T) {
	store, err := New(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	callID, _ := NewExecutionID("call_")
	binding := Binding{CallID: callID}
	changed := true
	count := 2
	insertions, deletions := 3, 1
	details := &FileEditDetails{
		Action:        "patch",
		DryRun:        false,
		Executed:      true,
		Changed:       &changed,
		AffectedCount: &count,
		AffectedFiles: []AffectedFile{{Path: "a.go", Operation: "update"}, {Path: "b.go", Operation: "add"}},
		Insertions:    &insertions,
		Deletions:     &deletions,
		DiffPreview:   "@@ bounded @@",
	}
	if _, err = store.Append(ctx, Event{Binding: binding, Kind: "call.created", ToolName: "file_edit"}); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Append(ctx, Event{Binding: binding, Kind: "call.completed", Status: "succeeded", ToolName: "file_edit", FileEdit: details}); err != nil {
		t.Fatal(err)
	}
	compact, err := store.Calls(ctx, CallQuery{Unattributed: true, IncludeOutput: false})
	if err != nil || len(compact.Calls) != 1 {
		t.Fatalf("compact projection: %+v %v", compact, err)
	}
	if compact.Calls[0].FileEdit == nil || compact.Calls[0].FileEdit.DiffPreview != "" || len(compact.Calls[0].FileEdit.AffectedFiles) != 2 {
		t.Fatalf("compact file details: %+v", compact.Calls[0].FileEdit)
	}
	full, err := store.Calls(ctx, CallQuery{Unattributed: true, IncludeOutput: true})
	if err != nil || len(full.Calls) != 1 || full.Calls[0].FileEdit == nil || full.Calls[0].FileEdit.DiffPreview != "@@ bounded @@" {
		t.Fatalf("full file details: %+v %v", full, err)
	}
}

func timePointerForTest(value time.Time) *time.Time {
	return &value
}
