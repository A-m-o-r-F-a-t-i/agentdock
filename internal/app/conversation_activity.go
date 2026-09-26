package app

import (
	"time"

	"github.com/uvwt/agentdock/internal/activity"
)

func (r *Runtime) confirmedConversationActivity() map[string]bool {
	result := map[string]bool{}
	add := func(binding activity.Binding) {
		if binding.ConversationID != "" && binding.ParentCallID == "" && binding.Visibility != "diagnostic" {
			result[binding.ConversationID] = true
		}
	}
	r.executionMu.Lock()
	for _, call := range r.activeCalls {
		add(call.binding)
	}
	for _, pending := range r.pendingCalls {
		add(pending.state.binding)
	}
	r.executionMu.Unlock()
	if r.command != nil {
		for _, binding := range r.command.ActiveConversationBindings() {
			add(binding)
		}
	}
	return result
}

func projectedConversationInteraction(stats activity.CallStats, now time.Time, terminated bool) (last, expires *time.Time, recent bool) {
	if stats.LastInteractionAt == nil || stats.LastInteractionAt.IsZero() {
		return nil, nil, false
	}
	value := *stats.LastInteractionAt
	expiry := value.Add(SidebarRecentWindow)
	return &value, &expiry, activity.RecentlyActive(&value, now, terminated)
}
