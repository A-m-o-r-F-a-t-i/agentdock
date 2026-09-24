package app

import "github.com/uvwt/agentdock/internal/activity"

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
