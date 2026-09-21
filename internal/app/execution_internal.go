package app

import (
	"context"
	"errors"
	"time"

	"github.com/uvwt/agentdock/internal/activity"
)

// ObserveInternal records a real server dispatch, not inferred substeps. It is
// used only by trusted transport and executor code, after parent authorization.
func (r *Runtime) ObserveInternal(ctx context.Context, name, title string, run func(context.Context) (Result, error)) (result Result, err error) {
	parent := activity.FromContext(ctx)
	binding, err := r.resolveExecutionScope(ctx)
	if err != nil {
		return nil, err
	}
	if err := r.checkConversationGate(ctx, binding.ConversationID); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	id, err := activity.NewExecutionID("call_")
	if err != nil {
		return nil, err
	}
	binding.ParentCallID = parent.CallID
	binding.CallID = id
	binding.RetryOfCallID = ""
	binding.Label = title
	start := time.Now()
	if err = r.appendExecution(activity.Event{Binding: binding, Kind: "call.created", ToolName: name, Title: title, Status: "created"}); err != nil {
		return nil, err
	}
	if err = r.appendExecution(activity.Event{Binding: binding, Kind: "call.started", ToolName: name, Title: title, Status: "running"}); err != nil {
		return nil, err
	}
	result, err = run(activity.WithBinding(ctx, binding))
	status := "succeeded"
	summary := title
	if err != nil {
		status = "failed"
		summary = r.executionRedactor(nil).Text(err.Error(), 2048)
	} else if resultReportsFailure(result) {
		status = "failed"
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		status = "unknown"
		summary = "内部请求已发出但未取得完整结果，请核对实际副作用后再执行。"
	}
	if count, ok := result["count"]; ok {
		summary += " · " + summaryValue(count)
	}
	_ = r.appendExecution(activity.Event{Binding: binding, Kind: "call.completed", ToolName: name, Title: title, Status: status, ElapsedMS: time.Since(start).Milliseconds(), Summary: summary})
	return result, err
}
func summaryValue(value any) string {
	switch v := value.(type) {
	case string:
		return v
	default:
		return "已返回"
	}
}
func (r *Runtime) observeRemoteTool(ctx context.Context, name string, run func(context.Context) (map[string]any, error)) (map[string]any, error) {
	return r.ObserveInternal(ctx, name, name, func(child context.Context) (Result, error) { return run(child) })
}

type localUserActionKey struct{}

// Called by the local control API only after direct-loopback and authentication
// validation. A model cannot set this marker through public tool arguments.
func WithLocalUserAction(ctx context.Context) context.Context {
	return context.WithValue(ctx, localUserActionKey{}, true)
}
