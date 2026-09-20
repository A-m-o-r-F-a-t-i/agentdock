package activity

import (
	"fmt"
	"strings"
	"time"
)

// Legacy sessions are aggregated only after an observed start and while their
// explicit binding and event order remain consistent. Ambiguity never silently
// discards an event or assigns a host conversation.
type legacySession struct {
	id        string
	binding   Binding
	last      time.Time
	ambiguous bool
}

func legacyEventID(seq uint64) string { return fmt.Sprintf("legacy_%032x", seq) }
func (p *callProjection) legacyIdentity(event Event) (string, bool) {
	if strings.HasPrefix(event.Kind, "task.") || strings.HasPrefix(event.Kind, "step.") || strings.HasPrefix(event.Kind, "thread.") || strings.HasPrefix(event.Kind, "review.") || strings.HasPrefix(event.Kind, "activity.") {
		return "", false // These remain in the separately rendered milestone journal.
	}
	if event.SessionID == "" || !strings.HasPrefix(event.Kind, "command.") {
		return legacyEventID(event.Seq), true
	}
	current := p.legacySessions[event.SessionID]
	if event.Kind == "command.started" {
		ambiguous := current != nil
		if current != nil {
			if prior := p.calls[current.id]; prior != nil {
				prior.HistoryIncomplete = true
				prior.Status = "unknown"
			}
			p.warning("An old command session had overlapping starts; ambiguous records remain separate and read-only.")
		}
		id := legacyEventID(event.Seq)
		p.legacySessions[event.SessionID] = &legacySession{id: id, binding: event.Binding, last: event.CreatedAt, ambiguous: ambiguous}
		return id, ambiguous
	}
	if current == nil {
		return legacyEventID(event.Seq), true
	}
	same := event.TaskID == current.binding.TaskID && event.ThreadID == current.binding.ThreadID && event.WorkspaceID == current.binding.WorkspaceID && !event.CreatedAt.Before(current.last)
	if !same || current.ambiguous {
		current.ambiguous = true
		if prior := p.calls[current.id]; prior != nil {
			prior.HistoryIncomplete = true
			prior.Status = "unknown"
		}
		if event.Kind == "command.completed" {
			delete(p.legacySessions, event.SessionID)
		}
		p.warning("An old command session had inconsistent bindings or order; affected events remain separate and read-only.")
		return legacyEventID(event.Seq), true
	}
	current.last = event.CreatedAt
	id := current.id
	if event.Kind == "command.completed" {
		delete(p.legacySessions, event.SessionID)
	}
	return id, false
}
