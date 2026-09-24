package activity

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

func savePayloadCall(t *testing.T, s *Store, id string, payload *Payload) {
	t.Helper()
	if _, err := s.Append(context.Background(), Event{Binding: Binding{CallID: id}, Kind: "call.created", Status: "created"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(context.Background(), Event{Binding: Binding{CallID: id}, Kind: "call.payload", Response: payload}); err != nil {
		t.Fatal(err)
	}
}

func TestPayloadRoundTripRedactionBinaryMetadataAndExactIntegers(t *testing.T) {
	s := testStore(t, Options{})
	input := map[string]any{
		"content": []any{
			map[string]any{"type": "text", "text": strings.Repeat("中文🙂\n", 1200)},
			map[string]any{"type": "image", "mimeType": "image/png", "data": "not-raw-image-data"},
			map[string]any{"type": "resource", "resource": map[string]any{"uri": "local://fixture", "mimeType": "application/octet-stream", "blob": "not-raw-resource-data"}},
		},
		"structuredContent": map[string]any{"sequence": uint64(1152921504606846979), "password": "private-password", "Authorization": "Bearer private-bearer", "env": map[string]any{"SOMETHING": "private-env"}, "normal": "kept"},
	}
	payload := s.CapturePayload(context.Background(), input, "complete", NewRedactor())
	if payload.State != "complete" || payload.Ref == "" || !payload.Truncated || !utf8.ValidString(payload.Preview) {
		t.Fatalf("payload: %+v", payload)
	}
	savePayloadCall(t, s, "call_payload", payload)
	copyStore, err := New(s.root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	var offset int64
	for {
		page, err := copyStore.ReadCallPayload(context.Background(), "call_payload", "response", offset, 37)
		if err != nil {
			t.Fatal(err)
		}
		if !utf8.ValidString(page.Text) || page.Offset != offset || page.NextOffset < offset {
			t.Fatalf("invalid page %+v", page)
		}
		text.WriteString(page.Text)
		if !page.HasMore {
			break
		}
		if page.NextOffset == offset {
			t.Fatal("non-progressing cursor")
		}
		offset = page.NextOffset
	}
	got := text.String()
	if int64(len(got)) != payload.Bytes || !json.Valid([]byte(got)) {
		t.Fatal("full payload did not survive replay")
	}
	for _, secret := range []string{"private-password", "private-bearer", "private-env", "not-raw-image-data", "not-raw-resource-data"} {
		if strings.Contains(got, secret) {
			t.Fatalf("leaked %q", secret)
		}
	}
	if !strings.Contains(got, "1152921504606846979") || !strings.Contains(got, "BINARY CONTENT") || !strings.Contains(got, "local://fixture") || !strings.Contains(got, "kept") {
		t.Fatal("lost exact result fields")
	}
	call, err := copyStore.Call(context.Background(), "call_payload")
	if err != nil || !call.HasOutput || call.Response == nil {
		t.Fatalf("output projection lost %+v %v", call, err)
	}
	page, err := copyStore.Calls(context.Background(), CallQuery{Limit: 10})
	if err != nil || page.Calls[0].Response.Preview != "" {
		t.Fatal("list unnecessarily included output preview")
	}
	// Reading and replaying output do not manufacture new call events.
	events, err := copyStore.Query(context.Background(), Query{CallID: "call_payload"})
	if err != nil || len(events.Events) != 2 {
		t.Fatalf("read generated activity %+v %v", events, err)
	}
}

func TestPayloadEmptyUnknownBoundsAndChangedFiles(t *testing.T) {
	s := testStore(t, Options{})
	p := s.CapturePayload(context.Background(), "", "complete", NewRedactor())
	savePayloadCall(t, s, "call_empty", p)
	page, err := s.ReadCallPayload(context.Background(), "call_empty", "response", 0, 4)
	if err != nil || page.Text != "\"\"" || page.Payload.State != "complete" {
		t.Fatalf("real empty result misreported %+v %v", page, err)
	}
	if _, err := s.Append(context.Background(), Event{Kind: "call.completed", Binding: Binding{CallID: "call_legacy"}, Status: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	page, err = s.ReadCallPayload(context.Background(), "call_legacy", "response", 0, 10)
	if err != nil || page.Payload.State != "unknown" || !strings.Contains(page.Payload.Reason, "旧记录未保存输出") {
		t.Fatal("fabricated historical output")
	}
	for _, test := range []struct {
		kind   string
		offset int64
	}{{"wrong", 0}, {"response", -1}, {"response", p.Bytes + 1}} {
		if _, err := s.ReadCallPayload(context.Background(), "call_empty", test.kind, test.offset, 10); err == nil {
			t.Fatalf("invalid read accepted %+v", test)
		}
	}
	file := filepath.Join(s.root, "payloads", p.Ref+".json")
	if err := os.WriteFile(file, []byte("different size"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadCallPayload(context.Background(), "call_empty", "response", 0, 10); err == nil {
		t.Fatal("replaced payload accepted")
	}
}

func TestPayloadSizeAndEncodingFailureAreExplicit(t *testing.T) {
	s := testStore(t, Options{})
	for _, value := range []any{make(chan int), strings.Repeat("x", MaxPayloadBytes+1)} {
		got := s.CapturePayload(context.Background(), value, "complete", NewRedactor())
		if got.State != "not_stored" || got.Ref != "" || got.Reason == "" {
			t.Fatalf("missing failure reason %+v", got)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got := s.CapturePayload(ctx, map[string]any{"x": "y"}, "complete", NewRedactor())
	if got.State != "not_stored" || got.Ref != "" {
		t.Fatal("cancelled capture continued")
	}
}

func TestActivityMutexWaitUsesCallerDeadline(t *testing.T) {
	s := testStore(t, Options{})
	s.mu.Lock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := s.Append(ctx, Event{Kind: "call.created"}); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			s.mu.Unlock()
			t.Fatalf("wrong deadline result: %v", err)
		}
	case <-time.After(time.Second):
		s.mu.Unlock()
		t.Fatal("mutex ignored caller cancellation")
	}
	s.mu.Unlock()
	if _, err := s.Append(context.Background(), Event{Kind: "call.created"}); err != nil {
		t.Fatal("cancelled waiter retained lock", err)
	}
}

func TestAppendBatchLockContextWaitsForAllCallers(t *testing.T) {
	shortCtx, cancelShort := context.WithCancel(context.Background())
	longCtx, cancelLong := context.WithCancel(context.Background())
	lockCtx, cleanup := appendBatchLockContext([]*appendRequest{{ctx: shortCtx}, {ctx: longCtx}})
	defer cleanup()
	cancelShort()
	select {
	case <-lockCtx.Done():
		t.Fatal("one cancelled caller stopped a batch that still had an active waiter")
	case <-time.After(20 * time.Millisecond):
	}
	cancelLong()
	select {
	case <-lockCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("batch lock context stayed active after every caller cancelled")
	}
}

func TestPayloadDeduplicatesAcrossStoresAndPersistsQuota(t *testing.T) {
	first := testStore(t, Options{})
	second, err := New(first.root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan *Payload, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s := first
			if i%2 == 1 {
				s = second
			}
			results <- s.CapturePayload(context.Background(), map[string]any{"data": "same redacted body"}, "complete", NewRedactor())
		}(i)
	}
	wg.Wait()
	close(results)
	var expected *Payload
	for result := range results {
		if result.State != "complete" || result.Ref == "" {
			t.Fatalf("capture %+v", result)
		}
		if expected == nil {
			expected = result
		}
		if result.Ref != expected.Ref {
			t.Fatal("dedupe mismatch")
		}
	}
	files, err := os.ReadDir(filepath.Join(first.root, "payloads"))
	if err != nil || len(files) != 1 {
		t.Fatalf("unexpected blobs %v %v", files, err)
	}
	release, err := second.lockPayload(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	used, err := second.payloadUsageLocked(context.Background(), filepath.Join(second.root, "payloads"))
	release()
	if err != nil || used != expected.Bytes {
		t.Fatalf("cross-store quota=%d want=%d err=%v", used, expected.Bytes, err)
	}
}

func TestPayloadCollectionKeepsReferencedAndInFlightBlobs(t *testing.T) {
	s := testStore(t, Options{})
	referenced := s.CapturePayload(context.Background(), "retained", "complete", NewRedactor())
	savePayloadCall(t, s, "call_retained", referenced)
	orphan := s.CapturePayload(context.Background(), "expired unreferenced", "complete", NewRedactor())
	recent := s.CapturePayload(context.Background(), "publication in progress", "complete", NewRedactor())
	old := time.Now().Add(-10 * time.Minute)
	for _, p := range []*Payload{referenced, orphan} {
		path := filepath.Join(s.root, "payloads", p.Ref+".json")
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	release, err := s.lockPayload(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	used, err := s.prunePayloadsLocked(context.Background(), filepath.Join(s.root, "payloads"))
	release()
	if err != nil || used != referenced.Bytes+recent.Bytes {
		t.Fatalf("collection result=%d %v", used, err)
	}
	if _, err := os.Stat(filepath.Join(s.root, "payloads", orphan.Ref+".json")); !os.IsNotExist(err) {
		t.Fatal("unreferenced expired blob not collected")
	}
	if _, err := s.ReadCallPayload(context.Background(), "call_retained", "response", 0, 32768); err != nil {
		t.Fatal("retained output lost", err)
	}
}

func TestPayloadIOAndJournalHaveIndependentLocks(t *testing.T) {
	store := testStore(t, Options{})
	if got := store.CapturePayload(t.Context(), "prime quota", "complete", NewRedactor()); got.Ref == "" {
		t.Fatal(got)
	}
	journalRelease, err := store.lock(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	got := store.CapturePayload(ctx, "write while journal busy", "complete", NewRedactor())
	cancel()
	journalRelease()
	if got.Ref == "" {
		t.Fatalf("payload blocked on unrelated journal: %+v", got)
	}
	payloadRelease, err := store.lockPayload(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel = context.WithTimeout(t.Context(), time.Second)
	_, err = store.Append(ctx, Event{Kind: "call.created", Binding: Binding{CallID: "call_lock_independent"}})
	cancel()
	payloadRelease()
	if err != nil {
		t.Fatal("journal blocked on payload I/O", err)
	}
}

func TestPayloadReuseRenewsPublicationGrace(t *testing.T) {
	store := testStore(t, Options{})
	payload := store.CapturePayload(t.Context(), "reused orphan", "complete", NewRedactor())
	if payload.Ref == "" {
		t.Fatal(payload)
	}
	path := filepath.Join(store.root, "payloads", payload.Ref+".json")
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	second, err := New(store.root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	reused := second.CapturePayload(t.Context(), "reused orphan", "complete", NewRedactor())
	if reused.Ref != payload.Ref {
		t.Fatal(reused)
	}
	release, err := store.lockPayload(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	used, err := store.prunePayloadsLocked(t.Context(), filepath.Join(store.root, "payloads"))
	release()
	if err != nil || used != payload.Bytes {
		t.Fatal("unpublished reused blob was collected", used, err)
	}
}

func TestRecentPayloadReuseDoesNotRewriteMetadata(t *testing.T) {
	store := testStore(t, Options{})
	payload := store.CapturePayload(t.Context(), "recent reused payload", "complete", NewRedactor())
	if payload.Ref == "" {
		t.Fatal(payload)
	}
	path := filepath.Join(store.root, "payloads", payload.Ref+".json")
	recent := time.Now().Add(-30 * time.Second).Truncate(time.Second)
	if err := os.Chtimes(path, recent, recent); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	reused := store.CapturePayload(t.Context(), "recent reused payload", "complete", NewRedactor())
	if reused.Ref != payload.Ref {
		t.Fatal(reused)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("recent reuse rewrote metadata: before=%s after=%s", before.ModTime(), after.ModTime())
	}
}

func TestPayloadAncestorLinksAreRejected(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.MkdirAll(filepath.Join(target, "payloads"), 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("platform cannot create symlink: %v", err)
	}
	if err := rejectPayloadLink(filepath.Join(link, "payloads")); err == nil {
		t.Fatal("linked ancestor accepted")
	}
}
