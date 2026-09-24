package activity

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func testStore(t *testing.T, options Options) *Store {
	t.Helper()
	s, err := New(t.TempDir(), options)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func appendTest(t *testing.T, s *Store, task, thread string) Event {
	t.Helper()
	e, err := s.Append(context.Background(), Event{Binding: Binding{TaskID: task, ThreadID: thread}, Kind: "command.completed", OutputPreview: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	return e
}
func TestDurableOrderedReplayAndThreadIsolation(t *testing.T) {
	s := testStore(t, Options{})
	a := appendTest(t, s, "tsk_a", "main")
	appendTest(t, s, "tsk_a", "branch")
	c := appendTest(t, s, "tsk_a", "main")
	reopened, err := New(s.root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	page, err := reopened.Query(context.Background(), Query{TaskID: "tsk_a", ThreadID: "main", After: a.Seq, Limit: 1})
	if err != nil || len(page.Events) != 1 || page.Events[0].EventID != c.EventID || page.NextSeq != c.Seq {
		t.Fatalf("page=%+v error=%v", page, err)
	}
	empty, err := reopened.Query(context.Background(), Query{After: page.NextSeq})
	if err != nil || len(empty.Events) != 0 {
		t.Fatalf("replay duplicated: %+v %v", empty, err)
	}
}
func TestAppendBatchPersistsOrderedLifecycleGroup(t *testing.T) {
	s := testStore(t, Options{})
	callID, err := NewExecutionID("call_")
	if err != nil {
		t.Fatal(err)
	}
	binding := Binding{CallID: callID}
	appended, err := s.AppendBatch(t.Context(), []Event{
		{Binding: binding, Kind: "call.created", Status: "created", ToolName: "agentdock_context"},
		{Binding: binding, Kind: "call.payload", ToolName: "agentdock_context", Request: &Payload{State: "complete", Ref: strings.Repeat("a", 64)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(appended) != 2 || appended[0].Seq == 0 || appended[1].Seq != appended[0].Seq+1 || appended[0].EventID == "" || appended[1].EventID == "" {
		t.Fatalf("unexpected batch result: %+v", appended)
	}
	page, err := s.Query(t.Context(), Query{CallID: callID})
	if err != nil || len(page.Events) != 2 || page.Events[0].Kind != "call.created" || page.Events[1].Kind != "call.payload" {
		t.Fatalf("ordered batch was not replayed intact: %+v %v", page, err)
	}
}
func TestAppendBatchValidatesWholeGroupBeforeWriting(t *testing.T) {
	s := testStore(t, Options{})
	_, err := s.AppendBatch(t.Context(), []Event{{Kind: "call.created"}, {Kind: "call.payload", ToolName: strings.Repeat("x", 161)}})
	if err == nil {
		t.Fatal("invalid batch was accepted")
	}
	page, queryErr := s.Query(t.Context(), Query{})
	if queryErr != nil || len(page.Events) != 0 || page.LatestSeq != 0 {
		t.Fatalf("invalid batch partially wrote: %+v %v", page, queryErr)
	}
}
func TestConcurrentStoresAllocateUniqueSequence(t *testing.T) {
	s := testStore(t, Options{})
	other, err := New(s.root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errors := make(chan error, 40)
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			writer := s
			if i%2 == 0 {
				writer = other
			}
			_, e := writer.Append(context.Background(), Event{Kind: "tool.completed"})
			errors <- e
		}(i)
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	page, err := s.Query(context.Background(), Query{})
	if err != nil || len(page.Events) != 40 {
		t.Fatalf("%+v %v", page, err)
	}
	for i, e := range page.Events {
		if e.Seq != uint64(i+1) {
			t.Fatalf("out of order: %+v", page)
		}
	}
}
func TestCorruptTailRecoveryAndCounterNotReused(t *testing.T) {
	s := testStore(t, Options{})
	a := appendTest(t, s, "tsk_a", "main")
	files, _ := s.segments()
	f, err := os.OpenFile(files[0], os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("{\"partial\":")
	_ = f.Close()
	b := appendTest(t, s, "tsk_a", "main")
	page, err := s.Query(context.Background(), Query{})
	if err != nil || len(page.Events) != 2 || b.Seq <= a.Seq {
		t.Fatalf("%+v %v", page, err)
	}
	if err = os.Remove(filepath.Join(s.root, "sequence.json")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	c := appendTest(t, s, "tsk_a", "main")
	if c.Seq <= b.Seq {
		t.Fatal("counter reused")
	}
}
func TestJournalTailReconcilesStaleSequenceState(t *testing.T) {
	s := testStore(t, Options{})
	a := appendTest(t, s, "tsk_a", "main")
	b := appendTest(t, s, "tsk_a", "main")
	if a.Seq != 1 || b.Seq != 2 {
		t.Fatalf("unexpected initial sequence: %d %d", a.Seq, b.Seq)
	}
	if err := s.saveState(sequenceState{Seq: 1}); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(s.root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	c := appendTest(t, reopened, "tsk_a", "main")
	if c.Seq != 3 {
		t.Fatalf("stale state reused a committed sequence: got %d want 3", c.Seq)
	}
	page, err := reopened.Query(context.Background(), Query{})
	if err != nil || page.LatestSeq != 3 || len(page.Events) != 3 {
		t.Fatalf("journal/state reconciliation failed: %+v %v", page, err)
	}
}
func TestAheadSequenceReservationRemainsReserved(t *testing.T) {
	s := testStore(t, Options{})
	appendTest(t, s, "tsk_a", "main")
	if err := s.saveState(sequenceState{Seq: 50}); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(s.root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	next := appendTest(t, reopened, "tsk_a", "main")
	if next.Seq != 51 {
		t.Fatalf("previously reserved sequence was reused: got %d want 51", next.Seq)
	}
}
func TestRotationReportsRetentionGap(t *testing.T) {
	s := testStore(t, Options{SegmentBytes: 300, Segments: 2})
	for i := 0; i < 12; i++ {
		appendTest(t, s, "tsk_a", "main")
	}
	files, _ := s.segments()
	if len(files) != 2 {
		t.Fatalf("segments=%v", files)
	}
	page, err := s.Query(context.Background(), Query{After: 1})
	if err != nil || !page.Gap || page.PrunedThrough == 0 {
		t.Fatalf("%+v %v", page, err)
	}
}
func TestReadLimitAndCorruptRecordWarning(t *testing.T) {
	s := testStore(t, Options{})
	appendTest(t, s, "tsk_a", "main")
	files, _ := s.segments()
	f, _ := os.OpenFile(files[0], os.O_APPEND|os.O_WRONLY, 0600)
	_, _ = f.WriteString("not json\n")
	_ = f.Close()
	page, err := s.Query(context.Background(), Query{})
	if err != nil || len(page.Warnings) != 1 {
		t.Fatalf("%+v %v", page, err)
	}
	f, _ = os.OpenFile(files[0], os.O_APPEND|os.O_WRONLY, 0600)
	_, _ = f.WriteString(strings.Repeat("x", MaxEventBytes+50))
	_ = f.Close()
	page, err = s.Query(context.Background(), Query{})
	if err != nil || len(page.Events) != 1 || len(page.Warnings) == 0 {
		t.Fatalf("oversized corrupt record must be skipped with a warning while preserving the valid record: %+v %v", page, err)
	}
}
func TestSecretsAreAbsentFromPersistedEvents(t *testing.T) {
	s, err := New(t.TempDir(), Options{}, "raw-credential-value")
	if err != nil {
		t.Fatal(err)
	}
	input := `curl -H "Authorization: Bearer abcdefghijklmnop" --password="very private" https://alice:hidden@example.test; token=xyzsecret; ssh -i "C:\keys\personal.pem"; raw-credential-value`
	e, err := s.Append(context.Background(), Event{Kind: "command.started", DisplayCommand: input, OutputPreview: input, Summary: input})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(e)
	for _, secret := range []string{"abcdefghijklmnop", "very private", "hidden", "xyzsecret", "personal.pem", "raw-credential-value"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("leaked %q: %s", secret, data)
		}
	}
}
func TestLineBufferHandlesSplitSecretsAndPrivateKeys(t *testing.T) {
	var b LineBuffer
	r := NewRedactor("split-secret")
	if got, _ := b.Feed("token=split-", false); got != "" {
		t.Fatal(got)
	}
	got, _ := b.Feed("secret\nready\n-----BEGIN PRIVATE KEY-----\nABCDEF", false)
	if strings.Contains(r.Text(got, 1000), "split-secret") || strings.Contains(got, "ABCDEF") {
		t.Fatal(got)
	}
	got, _ = b.Feed("GHI\n-----END PRIVATE KEY-----\ndone", true)
	if got != "done" {
		t.Fatalf("key block leaked: %q", got)
	}
}
func TestInvalidBindingsAndCancelledWrites(t *testing.T) {
	s := testStore(t, Options{})
	if _, err := s.Append(context.Background(), Event{Kind: "tool.completed", Binding: Binding{TaskID: "../outside"}}); err == nil {
		t.Fatal("invalid id accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Append(ctx, Event{Kind: "tool.completed"}); err == nil {
		t.Fatal("cancelled append accepted")
	}
}
