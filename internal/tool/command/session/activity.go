package session

import (
	"time"

	"github.com/uvwt/agentdock/internal/activity"
)

// OutputCursor belongs to one observer. Absolute byte offsets survive ring-buffer eviction.
type OutputCursor struct{ Stdout, Stderr int }

func (s *Session) SetActivityBinding(binding activity.Binding) {
	s.mu.Lock()
	s.activityBinding = binding
	s.mu.Unlock()
}

func (s *Session) SetActivityWarning(message string) {
	s.mu.Lock()
	s.activityWarning = message
	s.mu.Unlock()
}

func (s *Session) ActivitySnapshot(cursor *OutputCursor) Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	out, outGap := activityOutput(s.stdout.Bytes(), s.stdoutTotalBytes, s.stdoutDroppedBytes, cursor.Stdout)
	errout, errGap := activityOutput(s.stderr.Bytes(), s.stderrTotalBytes, s.stderrDroppedBytes, cursor.Stderr)
	cursor.Stdout, cursor.Stderr = s.stdoutTotalBytes, s.stderrTotalBytes
	finished, status := time.Now(), "running"
	if s.completed {
		finished, status = s.FinishedAt, "exited"
		if s.terminationRequested {
			status = "killed"
		}
		if s.TimedOut {
			status = "timeout"
		}
	}
	return Snapshot{
		Binding: s.activityBinding, ActivityWarning: s.activityWarning, SessionID: s.ID,
		Status: status, Stdout: out, Stderr: errout, ElapsedMS: finished.Sub(s.StartedAt).Milliseconds(),
		TimedOut: s.TimedOut, Terminal: s.Terminal, Completed: s.completed, ExitCode: s.exitCode,
		CommandOK:            s.completed && s.exitCode == 0 && !s.TimedOut,
		TerminationRequested: s.terminationRequested,
		StdoutTotalBytes:     s.stdoutTotalBytes, StderrTotalBytes: s.stderrTotalBytes,
		StdoutDroppedBytes: s.stdoutDroppedBytes, StderrDroppedBytes: s.stderrDroppedBytes,
		StdoutTruncated: outGap, StderrTruncated: errGap,
		Runtime: s.execution.Runtime, WSLDistribution: s.execution.Distribution, Workdir: s.execution.Workdir,
	}
}

func activityOutput(buffer []byte, total, dropped, after int) (string, bool) {
	gap := after < dropped
	start := max(after, dropped) - dropped
	if start >= len(buffer) || after >= total {
		return "", gap
	}
	return string(buffer[start:]), gap
}
