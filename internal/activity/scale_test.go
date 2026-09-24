package activity

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestExecutionProjectionScale100k(t *testing.T) {
	if testing.Short() {
		t.Skip("100k retained-event scale fixture")
	}
	root := t.TempDir()
	store, err := New(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, fmt.Sprintf("%020d.jsonl", 1))
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	buffer := bufio.NewWriterSize(file, 1<<20)
	encoder := json.NewEncoder(buffer)
	base := time.Date(2026, 9, 20, 14, 0, 0, 0, time.UTC)
	for i := 0; i < 20000; i++ {
		binding := Binding{CallID: fmt.Sprintf("call_%032x", i+1), ConversationID: fmt.Sprintf("conv_%032x", i%1000+1), TaskID: fmt.Sprintf("tsk_%08x", i%1000+1), ThreadID: "main"}
		for j, kind := range []string{"call.created", "call.started", "command.output", "command.output", "call.completed"} {
			event := Event{SchemaVersion: SchemaVersion, Binding: binding, Seq: uint64(i*5 + j + 1), CreatedAt: base.Add(time.Duration(i*5+j) * time.Millisecond), Kind: kind, ToolName: "exec_command", Title: "验证执行中心", DisplayCommand: "go test ./internal/app", Status: "running"}
			if j == 0 {
				event.Status = "created"
			}
			if j == 4 {
				event.Status = "succeeded"
			}
			if j == 2 || j == 3 {
				event.OutputPreview = "PASS: representative bounded output line\n"
			}
			if err = encoder.Encode(event); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err = buffer.Flush(); err != nil {
		t.Fatal(err)
	}
	if err = file.Sync(); err != nil {
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	if err = store.saveState(sequenceState{Seq: 100000}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	started := time.Now()
	stats, byConversation, err := store.CallStatistics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	page, err := store.Calls(ctx, CallQuery{ConversationID: fmt.Sprintf("conv_%032x", 1), Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	cold := time.Since(started)
	if stats.Total != 20000 || len(byConversation) != 1000 || len(page.Calls) != 20 {
		t.Fatalf("scale projection dropped identities: %+v convs=%d calls=%d", stats, len(byConversation), len(page.Calls))
	}
	started = time.Now()
	for i := 0; i < 20; i++ {
		if _, err = store.Calls(ctx, CallQuery{TaskID: fmt.Sprintf("tsk_%08x", i+1), Limit: 100}); err != nil {
			t.Fatal(err)
		}
	}
	warm := time.Since(started) / 20
	latestID := fmt.Sprintf("call_%032x", 20001)
	started = time.Now()
	if _, err = store.Append(ctx, Event{Binding: Binding{CallID: latestID, ConversationID: fmt.Sprintf("conv_%032x", 1)}, Kind: "call.created", ToolName: "read_file", Status: "created"}); err != nil {
		t.Fatal(err)
	}
	changes, err := store.Calls(ctx, CallQuery{Updates: true, After: 100000, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	update := time.Since(started)
	if len(changes.Calls) != 1 || changes.Calls[0].CallID != latestID {
		t.Fatal("incremental projection missed a newly appended call")
	}
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	t.Logf("scale: events=100000 calls=20000 conversations=1000 task_ids=1000 cold_query_ms=%.3f warm_task_query_ms=%.3f append_to_query_ms=%.3f process_heap_mib=%.2f", float64(cold.Microseconds())/1000, float64(warm.Microseconds())/1000, float64(update.Microseconds())/1000, float64(memory.HeapAlloc)/(1<<20))
	coldLimit := 2 * time.Second
	if raceDetectorEnabled {
		// The race runtime instruments every map and synchronization access in the
		// 100k-event projection. Keep a bounded regression check without treating
		// that instrumentation cost as the normal production latency target.
		coldLimit = 20 * time.Second
	}
	if cold > coldLimit {
		t.Errorf("cold execution query exceeded the %s target: %s", coldLimit, cold)
	}
	if update > time.Second {
		t.Errorf("incremental projection exceeded the planned 1s local target: %s", update)
	}
}
