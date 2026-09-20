package activity

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLegacyProjectionKeepsBindingAndOnlyReliableSessionsAggregate(t *testing.T) {
	s, err := New(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	binding := Binding{TaskID: "tsk_old", ThreadID: "thread_old", WorkspaceID: "wsp_old"}
	appendOld := func(kind, session string, b Binding, text string) Event {
		t.Helper()
		e, err := s.Append(ctx, Event{Binding: b, Kind: kind, SessionID: session, ToolName: "exec_command", OutputPreview: text, Status: "success"})
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	start := appendOld("command.started", "old-session", binding, "")
	appendOld("command.output", "old-session", binding, "reliable\n")
	appendOld("command.completed", "old-session", binding, "")
	// The same old session label can be reused later, but never merges commands.
	again := appendOld("command.started", "old-session", binding, "")
	appendOld("command.completed", "old-session", binding, "")
	orphan := appendOld("command.output", "orphan", binding, "unknown provenance")
	changed := binding
	changed.ThreadID = "other_thread"
	conflict := appendOld("command.started", "conflict", binding, "")
	separate := appendOld("command.output", "conflict", changed, "preserve other branch")
	appendOld("command.completed", "conflict", changed, "")
	plain := appendOld("tool.completed", "", binding, "legacy uncorrelated tool")
	appendOld("task.checkpoint", "", binding, "separate milestone")
	page, err := s.Calls(ctx, CallQuery{Unattributed: true, IncludeOutput: true, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Calls) != 7 {
		t.Fatalf("expected 7 reliable/individual legacy records, got %d: %+v", len(page.Calls), page.Calls)
	}
	byID := map[string]ExecutionCall{}
	for _, call := range page.Calls {
		byID[call.CallID] = call
		if !call.Legacy || !call.ReadOnlyLegacy || call.ConversationID != "" || call.TaskID != "tsk_old" {
			t.Fatalf("legacy identity altered: %+v", call)
		}
	}
	if c := byID[legacyEventID(start.Seq)]; c.EventCount != 3 || c.OutputPreview != "reliable\n" || c.Status != "succeeded" {
		t.Fatalf("reliable session did not aggregate: %+v", c)
	}
	if byID[legacyEventID(again.Seq)].EventCount != 2 {
		t.Fatal("session reuse merged two executions")
	}
	if !byID[legacyEventID(orphan.Seq)].HistoryIncomplete || !byID[legacyEventID(conflict.Seq)].HistoryIncomplete {
		t.Fatal("ambiguous legacy data looks complete")
	}
	if byID[legacyEventID(separate.Seq)].ThreadID != "other_thread" || byID[legacyEventID(plain.Seq)].OutputPreview != "legacy uncorrelated tool" {
		t.Fatal("unreliable event was discarded")
	}
	milestones, err := s.Query(ctx, Query{TaskID: "tsk_old", MilestonesOnly: true})
	if err != nil || len(milestones.Events) != 1 {
		t.Fatal("task checkpoint did not remain separate")
	}
	reopened, err := New(s.root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	restored, err := reopened.Calls(ctx, CallQuery{Unattributed: true, IncludeOutput: true, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(page.Calls)
	b, _ := json.Marshal(restored.Calls)
	if string(a) != string(b) {
		t.Fatal("legacy projection changed on reopen")
	}
}

func TestOversizedDamagedLegacyLineDoesNotBlockHealthyHistory(t *testing.T) {
	s, err := New(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err = s.Append(ctx, Event{Binding: Binding{TaskID: "tsk_old", CallID: "call_first"}, Kind: "call.created", ToolName: "read_file"}); err != nil {
		t.Fatal(err)
	}
	paths, err := s.segments()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	tail := Event{SchemaVersion: SchemaVersion, Seq: 3, Kind: "call.completed", Status: "success", ToolName: "list_dir", Binding: Binding{CallID: "call_healthy", TaskID: "tsk_other"}}
	encoded, _ := json.Marshal(tail)
	contents := append(append(append(raw, []byte(strings.Repeat("X", MaxEventBytes*3)+"\n")...), encoded...), '\n')
	if err = os.WriteFile(paths[0], contents, 0600); err != nil {
		t.Fatal(err)
	}
	if err = s.saveState(sequenceState{Seq: 3}); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(filepath.Dir(paths[0]), Options{})
	if err != nil {
		t.Fatal(err)
	}
	page, err := reopened.Calls(ctx, CallQuery{Unattributed: true})
	if err != nil || len(page.Calls) != 2 || !page.Gap || len(page.Warnings) == 0 {
		t.Fatalf("damaged record blocked healthy data: %+v %v", page, err)
	}
	after, err := os.ReadFile(paths[0])
	if err != nil || string(after) != string(contents) {
		t.Fatal("read migration rewrote original journal")
	}
}
