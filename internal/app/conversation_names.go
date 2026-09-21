package app

import (
	"context"
	"strings"
	"time"

	"github.com/uvwt/agentdock/internal/activity"
)

func conversationFallbackTitle(item activity.Conversation, workspaceName string) string {
	if workspaceName == "" {
		workspaceName = "对话"
	}
	created := item.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}
	return workspaceName + " · " + created.Local().Format("01-02 15:04")
}

// Resolve proposals from the already loaded snapshot and cache shared task
// titles for this request. The registry performs one guarded commit, instead
// of rereading and rewriting its entire JSON file for each legacy conversation.
func (r *Runtime) nameConversationSnapshot(ctx context.Context, items []activity.Conversation, workspaceNames map[string]string) ([]activity.Conversation, error) {
	proposals := map[string]activity.AutomaticTitle{}
	taskTitles := map[string]string{}
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if item.Title != "" && item.Title != "新对话" && !(item.TitleSource == "fallback" && strings.HasPrefix(item.Title, "对话 · ")) {
			continue
		}
		proposal := activity.AutomaticTitle{}
		for _, taskID := range item.TaskIDs {
			title, checked := taskTitles[taskID]
			if !checked {
				if task, err := r.tasks.Get(taskID); err == nil {
					title = strings.TrimSpace(task.Title)
				}
				taskTitles[taskID] = title
			}
			if title != "" {
				proposal = activity.AutomaticTitle{Title: title, Source: "task"}
				break
			}
		}
		if proposal.Title == "" {
			workspaceID := item.State.WorkspaceID
			if workspaceID == "" && len(item.WorkspaceIDs) > 0 {
				workspaceID = item.WorkspaceIDs[0]
			}
			proposal = activity.AutomaticTitle{Title: conversationFallbackTitle(item, workspaceNames[workspaceID]), Source: "fallback"}
		}
		if item.Title != proposal.Title || item.TitleSource != proposal.Source {
			proposals[item.ID] = proposal
		}
	}
	if len(proposals) == 0 {
		return items, nil
	}
	resolved, err := r.conversations.AutoNameBatch(ctx, proposals)
	if err != nil {
		return nil, err
	}
	result := make([]activity.Conversation, 0, len(items))
	for _, item := range items {
		if _, proposed := proposals[item.ID]; proposed {
			current, found := resolved[item.ID]
			if !found {
				continue // Deleted while resolving names: never restore the stale snapshot.
			}
			item = current
		}
		result = append(result, item)
	}
	return result, nil
}
