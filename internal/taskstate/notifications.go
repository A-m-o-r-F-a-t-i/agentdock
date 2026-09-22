package taskstate

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/uvwt/agentdock/internal/fs/atomicfile"
)

const CompletionNotificationAge = 5 * time.Minute

// Completion identity is saved in the same transaction as task success. It can
// be recovered even when the secondary activity journal append fails. Legacy
// records without this field are never turned into newly completed notifications.
type TaskCompletion struct {
	ID             string `json:"id"`
	ConversationID string `json:"conversation_id,omitempty"`
	ThreadID       string `json:"thread_id,omitempty"`
}

type CompletionNotification struct {
	ID             string    `json:"id"`
	TaskID         string    `json:"task_id"`
	Title          string    `json:"title"`
	ConversationID string    `json:"conversation_id,omitempty"`
	ThreadID       string    `json:"thread_id,omitempty"`
	WorkspaceID    string    `json:"workspace_id,omitempty"`
	CompletedAt    time.Time `json:"completed_at"`
}

type notificationClaims struct {
	SchemaVersion int                  `json:"schema_version"`
	Claimed       map[string]time.Time `json:"claimed"`
}

func (s *Store) CompleteWithSource(id, conversation, thread string) (Task, error) {
	if conversation != "" && !validStepID(conversation) || thread != "" && !validStepID(thread) {
		return Task{}, errors.New("invalid completion source")
	}
	return s.mutate(id, func(task *Task, now time.Time) error {
		if task.FinalReview == nil || task.FinalReview.Status != FinalReviewPass {
			return errors.New("final_review must pass before complete")
		}
		if err := completeTask(task, task.FinalReview.Summary, now, true); err != nil {
			return err
		}
		task.Completion.ConversationID, task.Completion.ThreadID = conversation, thread
		return nil
	})
}

// Claims are shared by tray and observer processes. Marking before response
// prevents replayed popups. A crashed observer may lose its claimed popup; no
// claim is represented as proof that the user has viewed it.
func (s *Store) ClaimCompletionNotifications(ctx context.Context, limit int, now time.Time) ([]CompletionNotification, error) {
	if limit < 1 || limit > 3 {
		return nil, errors.New("notification limit must be 1–3")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	release, err := s.acquireStoreLock()
	if err != nil {
		return nil, err
	}
	defer release()
	path := filepath.Join(s.root, "notification-claims.json")
	claims := notificationClaims{SchemaVersion: 1, Claimed: map[string]time.Time{}}
	data, err := readTaskStateFile(path)
	if err == nil {
		if err = json.Unmarshal(data, &claims); err != nil {
			return nil, err
		}
		if claims.SchemaVersion != 1 || len(claims.Claimed) > 8192 || claims.Claimed == nil {
			return nil, errors.New("invalid notification claims; original preserved")
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil, err
	}
	candidates := []CompletionNotification{}
	cutoff := now.Add(-CompletionNotificationAge)
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "tsk_") || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		// Only recently written primary state can contain a new completion. Merely
		// reading/replaying a task does not modify this file or create a completion id.
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.ModTime().Before(cutoff.Add(-time.Second)) {
			continue
		}
		bytes, err := readTaskStateFile(filepath.Join(s.root, entry.Name()))
		if err != nil {
			continue
		}
		task, err := decodeTask(bytes, entry.Name())
		if err != nil {
			continue
		}
		task = s.applyManagementLocked(task)
		if task.Status != StatusCompleted || task.Outcome != "success" || task.CancelledAt != nil || task.TrashedAt != nil || task.CompletedAt == nil || task.Completion == nil || task.Completion.ID == "" {
			continue
		}
		if task.CompletedAt.Before(cutoff) || task.CompletedAt.After(now) {
			continue
		}
		if _, claimed := claims.Claimed[task.Completion.ID]; claimed {
			continue
		}
		candidates = append(candidates, CompletionNotification{ID: task.Completion.ID, TaskID: task.ID, Title: task.Title, ConversationID: task.Completion.ConversationID, ThreadID: task.Completion.ThreadID, WorkspaceID: task.WorkspaceID, CompletedAt: *task.CompletedAt})
		sort.Slice(candidates, func(i, j int) bool {
			if candidates[i].CompletedAt.Equal(candidates[j].CompletedAt) {
				return candidates[i].ID < candidates[j].ID
			}
			return candidates[i].CompletedAt.Before(candidates[j].CompletedAt)
		})
		if len(candidates) > limit {
			candidates = candidates[:limit]
		}
	}
	if len(candidates) == 0 {
		return []CompletionNotification{}, nil
	}
	for id, at := range claims.Claimed {
		if at.Before(cutoff.Add(-CompletionNotificationAge)) {
			delete(claims.Claimed, id)
		}
	}
	if len(claims.Claimed)+len(candidates) > 8192 {
		return nil, errors.New("notification claim capacity exceeded")
	}
	for _, item := range candidates {
		claims.Claimed[item.ID] = now
	}
	data, err = json.Marshal(claims)
	if err != nil {
		return nil, err
	}
	if err = atomicfile.Write(path, append(data, '\n'), 0600); err != nil {
		return nil, err
	}
	return candidates, nil
}
