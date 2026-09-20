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

	"github.com/uvwt/agentdock/internal/textutil"
)

const MaxTaskIndexBytes = 16 << 10
const MaxTaskIndexItems = 8

type ThreadSummary struct {
	ID            string `json:"thread_id"`
	Title         string `json:"title"`
	Status        string `json:"status"`
	CurrentStepID string `json:"current_step_id,omitempty"`
	Summary       string `json:"summary,omitempty"`
	NextAction    string `json:"next_action,omitempty"`
	WorkspaceID   string `json:"workspace_id,omitempty"`
}

func SummarizeThread(thread *TaskThread) *ThreadSummary {
	if thread == nil {
		return nil
	}
	return &ThreadSummary{ID: thread.ID, Title: shortIndexText(thread.Title, 160), Status: thread.Status, CurrentStepID: thread.CurrentStepID, Summary: shortIndexText(thread.Summary, 768), NextAction: shortIndexText(thread.NextAction, 512), WorkspaceID: thread.WorkspaceID}
}

type TaskIndexItem struct {
	TaskID       string         `json:"task_id"`
	Title        string         `json:"title"`
	Status       Status         `json:"status"`
	Project      string         `json:"project,omitempty"`
	WorkspaceID  string         `json:"workspace_id,omitempty"`
	ActiveThread *ThreadSummary `json:"active_thread"`
	UpdatedAt    time.Time      `json:"updated_at"`
}

type TaskIndex struct {
	ActiveCount   int             `json:"active_count"`
	BlockedCount  int             `json:"blocked_count"`
	ReturnedCount int             `json:"returned_count"`
	Truncated     bool            `json:"truncated"`
	Items         []TaskIndexItem `json:"items"`
	Warnings      []string        `json:"warnings,omitempty"`
}

func EmptyTaskIndex() TaskIndex { return TaskIndex{Items: []TaskIndexItem{}} }
func shortIndexText(text string, limit int) string {
	return textutil.SafeTruncateString(text, limit).Text
}

// Discover counts all readable open tasks but retains at most eight candidates in memory.
// A corrupt file degrades only its own entry, never the overall context response.
func (s *Store) Discover(ctx context.Context) (TaskIndex, error) {
	index := EmptyTaskIndex()
	release, err := s.acquireStoreLock()
	if err != nil {
		return index, err
	}
	defer release()
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return index, err
	}
	candidates := []Task{}
	warn := func(message string) {
		if len(index.Warnings) < 8 {
			index.Warnings = append(index.Warnings, shortIndexText(message, 256))
		}
	}
	for _, entry := range entries {
		if err = ctx.Err(); err != nil {
			index.Truncated = true
			warn("Task index scan stopped: " + err.Error())
			break
		}
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "tsk_") || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, readErr := readTaskStateFile(filepath.Join(s.root, entry.Name()))
		if readErr != nil {
			warn("Could not read " + entry.Name())
			continue
		}
		task, decodeErr := decodeTask(data, entry.Name())
		if decodeErr != nil {
			warn("Invalid task " + entry.Name())
			continue
		}
		if task.ArchivedAt != nil {
			continue
		}
		switch task.Status {
		case StatusActive:
			index.ActiveCount++
		case StatusBlocked:
			index.BlockedCount++
		default:
			continue
		}
		candidates = append(candidates, task)
		sort.Slice(candidates, func(i, j int) bool { return candidates[i].UpdatedAt.After(candidates[j].UpdatedAt) })
		if len(candidates) > MaxTaskIndexItems {
			candidates = candidates[:MaxTaskIndexItems]
		}
	}
	for _, candidate := range candidates {
		task, loadErr := s.attachThreadLocked(candidate)
		if loadErr != nil {
			warn("Could not restore active thread of " + candidate.ID)
			continue
		}
		workspaceID := task.WorkspaceID
		if task.ActiveThread != nil && task.ActiveThread.WorkspaceID != "" {
			workspaceID = task.ActiveThread.WorkspaceID
		}
		index.Items = append(index.Items, TaskIndexItem{TaskID: task.ID, Title: shortIndexText(task.Title, 256), Status: task.Status, Project: shortIndexText(task.Project, 128), WorkspaceID: workspaceID, ActiveThread: SummarizeThread(task.ActiveThread), UpdatedAt: task.UpdatedAt})
	}
	for {
		index.ReturnedCount = len(index.Items)
		index.Truncated = index.Truncated || index.ReturnedCount < index.ActiveCount+index.BlockedCount
		data, marshalErr := json.Marshal(index)
		if marshalErr != nil {
			return EmptyTaskIndex(), marshalErr
		}
		if len(data) <= MaxTaskIndexBytes {
			return index, nil
		}
		if len(index.Items) == 0 {
			return EmptyTaskIndex(), errors.New("task index metadata exceeds output bound")
		}
		index.Items = index.Items[:len(index.Items)-1]
	}
}

func (s *Store) SetWorkspace(id, workspaceID string) (Task, error) {
	if workspaceID == "" || !validStepID(workspaceID) || len(workspaceID) > 80 {
		return Task{}, errors.New("invalid workspace_id")
	}
	return s.mutate(id, func(task *Task, now time.Time) error {
		if err := requireMutable(task); err != nil {
			return err
		}
		task.WorkspaceID = workspaceID
		if task.ActiveThread != nil {
			copy := *task.ActiveThread
			copy.WorkspaceID = workspaceID
			task.ActiveThread = &copy
		}
		return nil
	})
}
