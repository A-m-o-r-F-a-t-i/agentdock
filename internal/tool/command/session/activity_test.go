package session

import (
	"strings"
	"testing"
	"time"

	"github.com/uvwt/agentdock/internal/activity"
)

func TestActivityCursorDoesNotConsumeToolOutput(t *testing.T) {
	s := &Session{ID: "session-test", StartedAt: time.Now()}
	s.SetActivityBinding(activity.Binding{TaskID: "tsk_test", ThreadID: "main", StepID: "verify"})
	w := sessionOutputWriter{session: s}
	_, _ = w.Write([]byte("first\n"))
	var cursor OutputCursor
	observed := s.ActivitySnapshot(&cursor)
	tool := s.Snapshot("running", 100)
	if observed.Stdout != "first\n" || tool.Stdout != observed.Stdout || tool.ThreadID != "main" {
		t.Fatalf("activity=%+v tool=%+v", observed, tool)
	}
	_, _ = w.Write([]byte("second\n"))
	tool = s.Snapshot("running", 100)
	observed = s.ActivitySnapshot(&cursor)
	if tool.Stdout != "second\n" || observed.Stdout != tool.Stdout {
		t.Fatal("observers stole output")
	}
	if again := s.ActivitySnapshot(&cursor); again.Stdout != "" {
		t.Fatal("activity replayed duplicate")
	}
}

func TestActivityCursorMarksRingEvictionAndFixedElapsed(t *testing.T) {
	s := &Session{ID: "session-test", StartedAt: time.Now().Add(-time.Second)}
	w := sessionOutputWriter{session: s}
	_, _ = w.Write([]byte(strings.Repeat("x", 5<<20)))
	var cursor OutputCursor
	snap := s.ActivitySnapshot(&cursor)
	if !snap.StdoutTruncated || len(snap.Stdout) != 4<<20 {
		t.Fatalf("ring bounds: %+v", snap.StdoutTruncated)
	}
	s.completed = true
	s.FinishedAt = s.StartedAt.Add(42 * time.Millisecond)
	if s.ActivitySnapshot(&cursor).ElapsedMS != 42 || s.Snapshot("exited", 10).ElapsedMS != 42 {
		t.Fatal("completed elapsed still increases")
	}
}
