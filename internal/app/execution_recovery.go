package app

import (
	"context"
	"errors"
	"time"

	"github.com/uvwt/agentdock/internal/activity"
	"github.com/uvwt/agentdock/internal/fs/filelock"
)

func (r *Runtime) recoverExecutionState(ctx context.Context) error {
	recovered, err := r.permissions.Recover(ctx)
	if err != nil {
		return err
	}
	for _, a := range recovered {
		status := "cancelled"
		if a.Status == "unknown" {
			status = "unknown"
		}
		if err = r.appendExecution(activity.Event{Binding: a.Binding, Kind: "call.recovered", ToolName: a.Tool, ApprovalID: a.ID, Status: status, Summary: a.Summary}); err != nil {
			return err
		}
	}
	for _, status := range []string{"created", "running", "pending_approval"} {
		before := uint64(0)
		for {
			page, err := r.activity.Calls(ctx, activity.CallQuery{Status: status, Before: before, Limit: 200})
			if err != nil {
				return err
			}
			for _, call := range page.Calls {
				if filelock.ProcessAlive(call.OwnerPID) {
					continue
				}
				event := activity.Event{Binding: call.Binding, Kind: "call.recovered", ToolName: call.ToolName, SessionID: call.SessionID, Status: "unknown", Summary: "原执行进程已不在当前服务中，无法确认最终副作用。请检查实际状态后再发起新操作。"}
				if status == "pending_approval" {
					event.Status = "cancelled"
					event.Summary = "原执行进程已结束，内存中的固定请求失效，未自动派发。"
				}
				if err = r.appendExecution(event); err != nil {
					return err
				}
			}
			if !page.HasMore {
				break
			}
			before = page.NextBefore
		}
	}
	return nil
}
func (r *Runtime) cancelPendingOnClose() {
	if r.permissions == nil {
		return
	}
	r.executionMu.Lock()
	defer r.executionMu.Unlock()
	for id, p := range r.pendingCalls {
		_, _ = r.permissions.Settle(context.Background(), id, "expired", "服务已关闭，原操作未执行。")
		_ = r.appendExecution(activity.Event{Binding: p.state.binding, Kind: "call.completed", ToolName: p.spec.Name, ApprovalID: id, Status: "cancelled", Summary: "服务已关闭，原操作未执行。"})
		delete(r.pendingCalls, id)
	}
	for _, live := range r.activeCalls {
		live.cancel()
	}
}
func (r *Runtime) drainExecutionsOnClose() error {
	if r.executionMaintenanceDone != nil {
		select {
		case <-r.executionMaintenanceDone:
		case <-time.After(5 * time.Second):
			return errors.New("execution maintenance did not stop in time")
		}
	}
	done := make(chan struct{})
	go func() { r.executionWG.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		return errors.New("some tool calls did not acknowledge shutdown; their side effects must be verified")
	}
	if r.permissions == nil {
		return nil
	}
	approvals, err := r.permissions.Approvals(context.Background())
	if err != nil {
		return err
	}
	for _, a := range approvals {
		if a.Status != "dispatched" {
			continue
		}
		call, err := r.activity.Call(context.Background(), a.CallID)
		if err != nil || call.OwnerInstance != r.executionInstance {
			continue
		}
		status := call.Status
		if !activity.CallTerminal(status) {
			status = "unknown"
		}
		if status == "partial" {
			status = "failed"
		}
		_, _ = r.permissions.Settle(context.Background(), a.ID, status, "服务已关闭，结果以关联调用状态为准。")
	}
	return nil
}
