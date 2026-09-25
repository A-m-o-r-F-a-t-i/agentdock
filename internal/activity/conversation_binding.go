package activity

import (
	"context"
	"time"
)

// ConversationState contains continuation defaults, independently of the
// immutable scope already captured by each running or pending Call.
type ConversationState struct {
	ActiveTaskID       string    `json:"active_task_id,omitempty"`
	ActiveTaskThreadID string    `json:"active_task_thread_id,omitempty"`
	WorkspaceID        string    `json:"workspace_id,omitempty"`
	BindingRevision    uint64    `json:"binding_revision"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// UpdateBinding is a compare-and-swap. An older, slower management request may
// not overwrite a more recent completed binding change.
func (r *ConversationRegistry) UpdateBinding(ctx context.Context, id string, expected uint64, next ConversationState) (ConversationState, error) {
	state, _, err := r.updateBinding(ctx, id, expected, next, false)
	return state, err
}

// ConvergeBinding accepts an already-identical continuation without writing or
// changing its revision. Different targets still require the entry revision.
// Ownership and lifecycle checks run inside the same transaction in both cases.
func (r *ConversationRegistry) ConvergeBinding(ctx context.Context, id string, expected uint64, next ConversationState) (ConversationState, bool, error) {
	return r.updateBinding(ctx, id, expected, next, true)
}

func (r *ConversationRegistry) updateBinding(ctx context.Context, id string, expected uint64, next ConversationState, converge bool) (ConversationState, bool, error) {
	if err := (Binding{TaskID: next.ActiveTaskID, ThreadID: next.ActiveTaskThreadID, WorkspaceID: next.WorkspaceID}).Validate(); err != nil {
		return ConversationState{}, false, err
	}
	var result ConversationState
	changed := false
	err := r.state(ctx, func(state *conversationState) (bool, error) {
		record, found := state.Items[id]
		if !found {
			return false, ErrConversationNotFound
		}
		if record.OwnerKey != SourceOwnerKey(ctx) && !IsLocalManagement(ctx) {
			return false, ErrConversationOwner
		}
		if record.DeletedAt != nil {
			return false, ErrConversationDeleted
		}
		if record.TerminatedAt != nil {
			return false, ErrConversationTerminated
		}
		if record.TrashedAt != nil {
			return false, ErrConversationTrashed
		}
		if converge && record.State.ActiveTaskID == next.ActiveTaskID && record.State.ActiveTaskThreadID == next.ActiveTaskThreadID && record.State.WorkspaceID == next.WorkspaceID {
			result = record.State
			return false, nil
		}
		if record.State.BindingRevision != expected {
			return false, ErrConversationConflict
		}
		next.BindingRevision = expected + 1
		next.UpdatedAt = time.Now().UTC()
		record.State, record.UpdatedAt = next, next.UpdatedAt
		state.Items[id] = record
		result = next
		changed = true
		return true, nil
	})
	if err != nil {
		return ConversationState{}, false, err
	}
	return result, changed, nil
}

// ClearTask clears only matching active bindings. Concurrent switches to other
// tasks and all existing Call/Session snapshots remain untouched.
func (r *ConversationRegistry) ClearTask(ctx context.Context, taskID string) error {
	if taskID == "" {
		return nil
	}
	return r.state(ctx, func(state *conversationState) (bool, error) {
		dirty := false
		for id, record := range state.Items {
			if record.State.ActiveTaskID != taskID {
				continue
			}
			record.State.ActiveTaskID, record.State.ActiveTaskThreadID = "", ""
			record.State.BindingRevision++
			record.State.UpdatedAt = time.Now().UTC()
			state.Items[id] = record
			dirty = true
		}
		return dirty, nil
	})
}

// Only a directly connected, authenticated local control API may install this
// marker. Conversation identifiers select a target; they confer no authority.
type localManagementKey struct{}

func WithLocalManagement(ctx context.Context) context.Context {
	return context.WithValue(ctx, localManagementKey{}, true)
}
func IsLocalManagement(ctx context.Context) bool {
	value, _ := ctx.Value(localManagementKey{}).(bool)
	return value
}
