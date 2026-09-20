package command

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/uvwt/agentdock/internal/activity"
	"github.com/uvwt/agentdock/internal/tool/command/session"
)

func (svc *Service) SetActivityStore(store *activity.Store) { svc.activity = store }

func (svc *Service) SessionBinding(id string) (activity.Binding, bool) {
	s, ok := svc.sessions.Get(id)
	if !ok {
		return activity.Binding{}, false
	}
	return s.Summary().Binding, true
}

func (svc *Service) trackCommandActivity(s *session.Session, request ExecRequest) <-chan struct{} {
	done := make(chan struct{})
	if svc.activity == nil {
		close(done)
		return done
	}
	secrets := make([]string, 0, len(request.Env))
	for _, value := range request.Env {
		secrets = append(secrets, value)
	}
	// Only in-memory values are used. Neither the environment map nor its keys are journaled.
	for _, pair := range s.Command.Env {
		key, value, ok := strings.Cut(pair, "=")
		if !ok {
			continue
		}
		upper := strings.ToUpper(key)
		for _, marker := range []string{"TOKEN", "PASSWORD", "PASSWD", "SECRET", "API_KEY", "APIKEY", "AUTHORIZATION", "PRIVATE_KEY"} {
			if strings.Contains(upper, marker) {
				secrets = append(secrets, value)
				break
			}
		}
	}
	redactor := activity.NewRedactor(secrets...)
	base := activity.Event{Binding: request.Binding, ToolName: "exec_command", SessionID: s.ID, DisplayCommand: request.Cmd, Title: request.Label}
	execution := s.Summary()
	base.Runtime, base.Workdir = execution.Runtime, execution.Workdir
	appendEvent := func(event activity.Event) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := svc.activity.Append(ctx, redactor.Event(event)); err != nil {
			s.SetActivityWarning("Command execution continues; its activity journal is incomplete. Inspect the local activity store.")
			slog.Warn("record command activity", "session_id", s.ID, "kind", event.Kind, "error", err)
		}
	}
	started := base
	started.Kind, started.Status = "command.started", "running"
	appendEvent(started)
	svc.activityWG.Add(1)
	go func() {
		defer svc.activityWG.Done()
		defer close(done)
		cursor := session.OutputCursor{}
		stdout, stderr := activity.LineBuffer{}, activity.LineBuffer{}
		outTruncated, errTruncated := false, false
		// An eviction may cut a private-key block or credential line. Stop previewing that stream
		// after a gap, while preserving the true completion status and explicit truncation marker.
		stdoutUnsafe, stderrUnsafe := false, false
		tick := time.NewTicker(300 * time.Millisecond)
		defer tick.Stop()
		for {
			snap := s.ActivitySnapshot(&cursor)
			stdoutUnsafe = stdoutUnsafe || snap.StdoutTruncated
			stderrUnsafe = stderrUnsafe || snap.StderrTruncated
			var out, errout string
			var outCut, errCut bool
			if !stdoutUnsafe {
				out, outCut = stdout.Feed(snap.Stdout, snap.Completed)
			}
			if !stderrUnsafe {
				errout, errCut = stderr.Feed(snap.Stderr, snap.Completed)
			}
			outTruncated = outTruncated || outCut || stdoutUnsafe
			errTruncated = errTruncated || errCut || stderrUnsafe
			if out != "" || errout != "" || outCut || errCut || snap.StdoutTruncated || snap.StderrTruncated {
				event := base
				event.Kind, event.Status = "command.output", "running"
				event.ElapsedMS = snap.ElapsedMS
				event.OutputPreview, event.StderrPreview = out, errout
				event.StdoutTruncated, event.StderrTruncated = outTruncated, errTruncated
				appendEvent(event)
			}
			if snap.Completed {
				event := base
				event.Kind, event.Status = "command.completed", snap.Status
				event.ExitCode, event.CommandOK = &snap.ExitCode, &snap.CommandOK
				event.TimedOut, event.ElapsedMS = snap.TimedOut, snap.ElapsedMS
				event.StdoutTruncated, event.StderrTruncated = outTruncated, errTruncated
				if snap.Status == "exited" {
					if snap.CommandOK {
						event.Status = "success"
					} else {
						event.Status = "failed"
					}
				}
				appendEvent(event)
				return
			}
			select {
			case <-s.Done:
			case <-tick.C:
			}
		}
	}()
	return done
}

func waitCommandActivity(done <-chan struct{}) {
	select {
	case <-done:
	case <-time.After(6 * time.Second):
	}
}

func (svc *Service) WaitActivity(ctx context.Context) error {
	done := make(chan struct{})
	go func() { svc.activityWG.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func addBindingResult(result Result, binding activity.Binding) {
	if binding.TaskID != "" {
		result["task_id"] = binding.TaskID
	}
	if binding.ThreadID != "" {
		result["thread_id"] = binding.ThreadID
	}
	if binding.StepID != "" {
		result["step_id"] = binding.StepID
	}
	if binding.WorkspaceID != "" {
		result["workspace_id"] = binding.WorkspaceID
	}
}
