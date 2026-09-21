package app

import (
	"context"
	"errors"
	"github.com/uvwt/agentdock/internal/activity"
)

func (r *Runtime) RuntimeCallManagementBatch(ctx context.Context, request BatchRequest) (BatchResult, error) {
	result := BatchResult{Items: []BatchItem{}, Status: "succeeded"}
	if !activity.IsLocalManagement(ctx) {
		return result, toolError("LOCAL_ONLY", "Call management is available only to the authenticated local control panel.", "permission")
	}
	if len(request.IDs) == 0 || len(request.IDs) > 200 {
		return result, errors.New("provide 1–200 explicit call IDs")
	}
	switch request.Action {
	case "archive", "unarchive", "isolate", "unisolate", "trash", "restore", "delete":
	default:
		return result, errors.New("invalid call management action")
	}
	if request.Action == "delete" && !request.ConfirmPermanent {
		return result, errors.New("permanent removal requires confirmation")
	}
	seen := map[string]bool{}
	for _, id := range request.IDs {
		if seen[id] || (activity.Binding{CallID: id}).Validate() != nil {
			return result, errors.New("invalid or duplicate call ID")
		}
		seen[id] = true
	}
	for _, id := range request.IDs {
		r.executionMu.Lock()
		err := r.activity.ManageCall(ctx, id, activity.MetadataChange{Action: request.Action, RetentionDays: request.RetentionDays})
		r.executionMu.Unlock()
		item := BatchItem{ID: id, Status: "succeeded"}
		if err != nil {
			item.Status, item.Message = "failed", err.Error()
			result.Failed++
		} else {
			result.Succeeded++
		}
		result.Items = append(result.Items, item)
	}
	if result.Failed != 0 {
		result.Status = "partial"
	}
	return result, nil
}
