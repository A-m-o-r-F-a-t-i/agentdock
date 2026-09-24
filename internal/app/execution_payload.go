package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/uvwt/agentdock/internal/activity"
)

func (r *Runtime) recordExecutionPayload(binding activity.Binding, name, kind string, value any, redactor activity.Redactor) {
	if r.activity == nil || binding.CallID == "" || binding.ParentCallID != "" || binding.Visibility == "diagnostic" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	payload := r.activity.CapturePayload(ctx, value, "complete", redactor)
	event := activity.Event{Binding: binding, Kind: "call.payload", ToolName: name}
	if kind == "request" {
		event.Request = payload
	} else {
		event.Response = payload
	}
	if err := r.appendExecution(event); err != nil {
		slog.Warn("Activity payload reference could not be saved", "call_id", binding.CallID, "kind", kind)
	}
}

func bindResponseAudit(ctx context.Context, binding activity.Binding, name string, redactor activity.Redactor, received time.Time, status string) bool {
	response, _ := ctx.Value(toolResponseKey{}).(*ToolResponse)
	if response == nil || binding.ParentCallID != "" {
		return false
	}
	response.mu.Lock()
	defer response.mu.Unlock()
	response.auditBinding, response.auditName, response.auditRedactor = binding, name, redactor
	response.auditReceived, response.auditStatus = received, status
	return true
}

// RecordToolResponse is called only at the adapter boundary, after catalog and
// authenticated insertions are attached. It updates the same root call instead
// of emitting duplicate child output or inventing another external call.
func (r *Runtime) RecordToolResponse(response *ToolResponse, envelope any) {
	if response == nil {
		return
	}
	response.mu.Lock()
	if response.auditRecorded {
		response.mu.Unlock()
		return
	}
	response.auditRecorded = true
	binding, name, redactor := response.auditBinding, response.auditName, response.auditRedactor
	received, status := response.auditReceived, response.auditStatus
	response.mu.Unlock()
	r.recordExecutionPayload(binding, name, "response", envelope, redactor)
	// This server-side boundary includes final-envelope serialization and
	// observation storage. It does not invent downstream network delivery time.
	if !received.IsZero() {
		if err := r.recordRPCReturnStatus(binding, name, received, status); err != nil {
			slog.Warn("Final response timing could not be saved", "call_id", binding.CallID)
		}
	}
}
