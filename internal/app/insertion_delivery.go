package app

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/uvwt/agentdock/internal/activity"
	"github.com/uvwt/agentdock/internal/insertion"
)

type insertionTransportKey struct{}

// This entry is called only by the authenticated MCP metadata adapter after
// checking its initialized capabilities; there is no business-argument alias.
func (r *Runtime) RecordInsertionTransportReceipt(ctx context.Context, stage, outerID string, receipts []insertion.Receipt) error {
	host := InsertionTransportFromContext(ctx)
	if !host.Passthrough || host.HostType == "" || outerID == "" || len(outerID) > 160 {
		return errors.New("insertion passthrough was not negotiated")
	}
	by := "outer_forwarded"
	if stage == "context_committed" {
		if !host.ContextAcknowledgement {
			return errors.New("insertion context receipts were not negotiated")
		}
		by = "host_context_committed"
	} else if stage != "outer_forwarded" {
		return errors.New("invalid insertion host receipt stage")
	}
	_, err := r.receiveInsertionReceipts(ctx, receipts, by, outerID)
	return err
}

func (r *Runtime) RecordInsertionDeliveryFailure(ctx context.Context, response *ToolResponse, reason string) error {
	if response == nil {
		return errors.New("missing adapter response")
	}
	response.mu.Lock()
	binding := response.binding
	response.mu.Unlock()
	if binding.CallID == "" || activity.SourceOwnerKey(ctx) != binding.SourceOwnerKey {
		return insertion.ErrReceipt
	}
	finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	items, err := r.insertions.DeliveryFailed(finish, binding.CallID, reason)
	if err != nil {
		return err
	}
	for _, item := range items {
		r.recordInsertionStage(finish, item, "delivery_unknown")
	}
	return nil
}

// Only an authenticated adapter supplies transport declarations. Tool arguments
// and tool-result fields cannot establish these capabilities.
func WithInsertionTransport(ctx context.Context, host insertion.Transport) context.Context {
	return context.WithValue(ctx, insertionTransportKey{}, host)
}
func InsertionTransportFromContext(ctx context.Context) insertion.Transport {
	host, _ := ctx.Value(insertionTransportKey{}).(insertion.Transport)
	return host
}

func (r *Runtime) receiveInsertionReceipts(ctx context.Context, receipts []insertion.Receipt, by, outerID string) ([]insertion.Item, error) {
	scope, err := r.resolveExecutionScope(ctx)
	if err != nil {
		return nil, err
	}
	if scope.ConversationID == "" || scope.SourceOwnerKey == "" || activity.IsLocalManagement(ctx) || activity.IsDiagnostic(ctx) {
		return nil, insertion.ErrReceipt
	}
	r.executionMu.Lock()
	defer r.executionMu.Unlock()
	if err = r.checkConversationGate(ctx, scope.ConversationID); err != nil {
		return nil, err
	}
	// Re-read under the lifecycle lock: a binding switch while this receipt
	// was waiting must not acknowledge instructions for the previous task.
	record, err := r.conversations.Get(ctx, scope.ConversationID)
	if err != nil {
		return nil, err
	}
	target := insertion.Target{Owner: scope.SourceOwnerKey, Conversation: record.ID, Task: record.State.ActiveTaskID, Thread: record.State.ActiveTaskThreadID, Workspace: record.State.WorkspaceID}
	items, err := r.insertions.Receipt(ctx, target, receipts, by, outerID)
	if err != nil {
		return nil, toolError("INSERTION_RECEIPT_INVALID", insertion.ErrReceipt.Error(), "validation")
	}
	for _, item := range items {
		r.recordInsertionStage(ctx, item, item.Status)
	}
	return items, nil
}

// RecordInsertionHostReceipt accepts evidence from the adapter-owned response,
// not a user-generated projected map. The host must separately confirm that its
// final output was committed before requesting the acknowledged transition.
func (r *Runtime) RecordInsertionHostReceipt(ctx context.Context, response *ToolResponse, contextCommitted bool) error {
	if response == nil {
		return errors.New("missing adapter response")
	}
	response.mu.Lock()
	host := response.transport
	binding := response.binding
	receipts := make([]insertion.Receipt, 0, len(response.additions.UserMessages))
	for _, message := range response.additions.UserMessages {
		receipts = append(receipts, insertion.Receipt{InsertionID: message.InsertionID, Token: message.ReceiptToken})
	}
	response.mu.Unlock()
	if len(receipts) == 0 {
		return nil
	}
	if !host.Passthrough || host.OuterCallID == "" || contextCommitted && !host.ContextAcknowledgement {
		return errors.New("host receipt capability was not negotiated")
	}
	if activity.SourceOwnerKey(ctx) != binding.SourceOwnerKey {
		return insertion.ErrReceipt
	}
	by := "outer_forwarded"
	if contextCommitted {
		by = "host_context_committed"
	}
	_, err := r.receiveInsertionReceipts(ctx, receipts, by, host.OuterCallID)
	return err
}

func (r *Runtime) RuntimeRetryInsertion(ctx context.Context, conversation, id string) (Result, error) {
	if !activity.IsLocalManagement(ctx) {
		return nil, activity.ErrConversationOwner
	}
	r.executionMu.Lock()
	defer r.executionMu.Unlock()
	record, owner, err := r.conversations.LocalTarget(ctx, conversation)
	if err != nil {
		return nil, err
	}
	if record.TerminatedAt != nil {
		return nil, activity.ErrConversationTerminated
	}
	if record.TrashedAt != nil {
		return nil, activity.ErrConversationTrashed
	}
	target := insertion.Target{Owner: owner, Conversation: conversation, Task: record.State.ActiveTaskID, Thread: record.State.ActiveTaskThreadID, Workspace: record.State.WorkspaceID}
	item, err := r.insertions.Retry(ctx, target, id)
	if err != nil {
		return nil, err
	}
	r.recordInsertionStage(ctx, item, "retry_requested")
	items, err := r.insertions.Views(ctx, owner, conversation)
	if err != nil {
		return nil, err
	}
	for _, view := range items {
		if view.ID == id {
			return Result{"insertion": view, "server_now": time.Now().UTC()}, nil
		}
	}
	return nil, insertion.ErrNotFound
}

// The queue is the delivery record of truth. Observation contains IDs and
// timestamps only; no user message or acknowledgement secret is copied here.
// No call_id is invented, so these events cannot inflate root-call statistics.
func (r *Runtime) recordInsertionStage(ctx context.Context, item insertion.Item, stage string) {
	if r.activity == nil {
		return
	}
	metadata := map[string]any{"insertion_id": item.ID, "inner_call_id": item.CallID, "outer_call_id": item.OuterCallID, "host_type": item.HostType, "delivery_attempt": item.DeliveryAttempts, "acknowledged_by": item.AcknowledgedBy, "acknowledged_at": item.AcknowledgedAt, "reason": item.DeliveryReason}
	encoded, _ := json.Marshal(metadata)
	event := activity.Event{Binding: activity.Binding{ConversationID: item.Conversation, TaskID: item.Task, ThreadID: item.Thread, WorkspaceID: item.Workspace}, Kind: "insertion." + stage, Status: item.Status, Summary: string(encoded)}
	if _, err := r.activity.Append(ctx, event); err != nil {
		slog.Warn("Insertion stage audit unavailable; queue state preserved", "insertion_id", item.ID, "stage", stage)
	}
}
