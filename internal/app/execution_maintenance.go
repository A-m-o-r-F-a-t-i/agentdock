package app

import (
	"context"
	"time"

	"github.com/uvwt/agentdock/internal/activity"
	"github.com/uvwt/agentdock/internal/taskstate"
)

// purgeExpiredManagement operates only on the two management stores. The same
// active-call guard as an explicit delete is retained for expiry processing.
func (r *Runtime) purgeExpiredManagement(ctx context.Context, now time.Time) (int, error) {
	type candidate struct{ kind, id string }
	selected := []candidate{}
	conversations, err := r.conversations.List(ctx)
	if err != nil {
		return 0, err
	}
	for _, item := range conversations {
		if item.TrashedAt != nil && item.PurgeAfter != nil && !now.Before(*item.PurgeAfter) {
			selected = append(selected, candidate{"conversation", item.ID})
		}
	}
	offset := 0
	for {
		page, err := r.tasks.ManagedTasks(ctx, taskstate.TaskQuery{View: "trash", Offset: offset, Limit: 200})
		if err != nil {
			return 0, err
		}
		for _, item := range page.Tasks {
			if item.PurgeAfter != nil && !now.Before(*item.PurgeAfter) {
				selected = append(selected, candidate{"task", item.ID})
			}
		}
		if !page.HasMore {
			break
		}
		offset = page.NextOffset
	}
	count := 0
	for _, item := range selected {
		if err = ctx.Err(); err != nil {
			return count, err
		}
		result := r.manageOne(ctx, item.kind, item.id, activity.MetadataChange{Action: "delete"})
		if result.Status == "succeeded" {
			count++
		}
	}
	return count, nil
}
func (r *Runtime) startExecutionMaintenance() {
	r.executionMaintenanceDone = make(chan struct{})
	go func() {
		defer close(r.executionMaintenanceDone)
		timer := time.NewTimer(10 * time.Second)
		defer timer.Stop()
		for {
			select {
			case <-r.commandCtx.Done():
				return
			case <-timer.C:
			}
			ctx, cancel := context.WithTimeout(r.commandCtx, 30*time.Second)
			_, _ = r.purgeExpiredManagement(ctx, time.Now().UTC())
			r.expirePendingApprovals(ctx)
			if r.insertions != nil {
				_ = r.insertions.Sweep(ctx)
			}
			cancel()
			timer.Reset(time.Minute)
		}
	}()
}
